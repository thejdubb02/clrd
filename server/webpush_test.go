package server

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestPushEncryptRFC8291 is the worked example of RFC 8291 section 5.
func TestPushEncryptRFC8291(t *testing.T) {
	d := func(s string) []byte {
		b, err := b64.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	as, err := ecdh.P256().NewPrivateKey(d("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encryptPush([]byte("When I grow up, I want to be a watermelon"),
		d("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		d("BTBZMqHH6r4Tts7J_aSIgg"), as, d("DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := d("DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")
	if !bytes.Equal(got, want) {
		t.Fatalf("body\n got %s\nwant %s", b64.EncodeToString(got), b64.EncodeToString(want))
	}
}

// decryptPush is the browser's side of encryptPush.
func decryptPush(t *testing.T, body []byte, ua *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt, n := body[:16], int(body[20])
	asPub := body[21 : 21+n]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := ua.ECDH(as)
	ikm, _ := hkdf.Key(sha256.New, secret, auth, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPub), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+n:], nil)
	if err != nil || len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatalf("decrypt: %v %q", err, plain)
	}
	return plain[:len(plain)-1]
}

type gotPush struct {
	h    http.Header
	body []byte
}

func TestWebPush(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	got := make(chan gotPush, 10)
	status := 201
	svc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- gotPush{r.Header.Clone(), b}
		w.WriteHeader(status)
	}))
	defer svc.Close()

	phone := mkToken(t, s, "phone", []string{ScopeRead}, []string{"alerts"})
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)

	// The real client never reaches a loopback push service.
	target := pushTarget{endpoint: svc.URL, p256dh: b64.EncodeToString(ua.PublicKey().Bytes()), auth: b64.EncodeToString(auth)}
	if _, err := s.push(ctx, target, []byte("{}"), &Notification{ID: "x"}); !errors.Is(err, errBlocked) {
		t.Fatalf("loopback endpoint: %v", err)
	}
	s.pushHTTP = svc.Client()
	sub := map[string]any{"endpoint": svc.URL + "/sub/1", "keys": map[string]any{"p256dh": b64.EncodeToString(ua.PublicKey().Bytes()), "auth": b64.EncodeToString(auth)}}
	for _, bad := range []map[string]any{
		{"endpoint": "http://push.example/1", "keys": sub["keys"]},
		{"endpoint": sub["endpoint"], "keys": map[string]any{"p256dh": b64.EncodeToString(auth), "auth": b64.EncodeToString(auth)}},
		{"endpoint": sub["endpoint"], "keys": map[string]any{"p256dh": sub["keys"].(map[string]any)["p256dh"], "auth": b64.EncodeToString(auth[:15])}},
	} {
		if code, out := call(t, ts, "PUT", "/api/v1/push", phone, bad); code != 400 {
			t.Fatalf("bad subscription %v: %d %v", bad, code, out)
		}
	}
	ci := mkToken(t, s, "ci", []string{ScopePublish}, nil)
	if code, _ := call(t, ts, "PUT", "/api/v1/push", ci, sub); code != 403 {
		t.Fatalf("publish-only token subscribed: %d", code)
	}
	if code, _ := call(t, ts, "DELETE", "/api/v1/push", ci, nil); code != 403 {
		t.Fatalf("publish-only token unsubscribed: %d", code)
	}
	if code, out := call(t, ts, "PUT", "/api/v1/push", phone, sub); code != 204 {
		t.Fatalf("subscribe: %d %v", code, out)
	}
	code, info := call(t, ts, "GET", "/api/v1/push", phone, nil)
	if code != 200 || info["subscribed"] != true {
		t.Fatalf("get: %d %v", code, info)
	}

	// pushes runs the worker over what is queued and returns what arrived.
	pushes := func() []gotPush {
		t.Helper()
		for len(s.pushq) > 0 {
			s.sendPushes(ctx, <-s.pushq)
		}
		var out []gotPush
		for len(got) > 0 {
			out = append(out, <-got)
		}
		return out
	}
	publish := func(body map[string]any) map[string]any {
		t.Helper()
		code, n := call(t, ts, "POST", "/api/v1/notifications", admin, body)
		if code != 200 && code != 201 {
			t.Fatalf("publish: %d %v", code, n)
		}
		return n
	}
	pushes()

	n := publish(map[string]any{"topic": "alerts", "title": "Disk", "message": "full", "priority": 5})
	ps := pushes()
	if len(ps) != 1 {
		t.Fatalf("pushes: %d", len(ps))
	}
	p := ps[0]
	var m pushMsg
	if err := json.Unmarshal(decryptPush(t, p.body, ua, auth), &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != EventCreated || m.ID != n["id"] || m.Title != "Disk" || m.Message != "full" || m.Topic != "alerts" {
		t.Fatalf("payload: %+v", m)
	}
	if p.h.Get("Content-Encoding") != "aes128gcm" || p.h.Get("TTL") == "" || p.h.Get("Urgency") != "high" || p.h.Get("Topic") != n["id"] {
		t.Fatalf("headers: %v", p.h)
	}
	// The VAPID token is signed by the key GET gives, for the service's origin.
	a, ok := strings.CutPrefix(p.h.Get("Authorization"), "vapid t=")
	jwt, k, ok2 := strings.Cut(a, ", k=")
	if !ok || !ok2 || k != info["public_key"] {
		t.Fatalf("authorization: %q", p.h.Get("Authorization"))
	}
	parts := strings.Split(jwt, ".")
	kb, _ := b64.DecodeString(k)
	x, y := elliptic.Unmarshal(elliptic.P256(), kb)
	sig, _ := b64.DecodeString(parts[2])
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(parts) != 3 || x == nil || len(sig) != 64 ||
		!ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("vapid signature does not verify")
	}
	var claims map[string]any
	cb, _ := b64.DecodeString(parts[1])
	json.Unmarshal(cb, &claims)
	if claims["aud"] != svc.URL || claims["sub"] == "" {
		t.Fatalf("claims: %v", claims)
	}

	// The same rules as the inbox ringing: a topic it may not read, a send to
	// other devices, a silence and a scheduled send push nothing.
	publish(map[string]any{"topic": "secret", "message": "m"})
	publish(map[string]any{"topic": "alerts", "message": "m", "devices": []string{"admin"}})
	publish(map[string]any{"topic": "alerts", "message": "m", "snooze_until": time.Now().Add(time.Hour).Format(time.RFC3339)})
	_, sl := call(t, ts, "POST", "/api/v1/silences", admin, map[string]any{"topic": "alerts", "for": "1h"})
	publish(map[string]any{"topic": "alerts", "message": "m"})
	if ps := pushes(); len(ps) != 0 {
		t.Fatalf("pushed %d that should not ring", len(ps))
	}
	if code, _ := call(t, ts, "DELETE", "/api/v1/silences/"+sl["id"].(string), admin, nil); code != 204 && code != 200 {
		t.Fatalf("unsilence: %d", code)
	}

	// A message too big for one push is cut to fit, on a rune boundary.
	publish(map[string]any{"topic": "alerts", "title": "big", "message": strings.Repeat("é<", 3000)})
	ps = pushes()
	if len(ps) != 1 || len(ps[0].body) > 4096 {
		t.Fatalf("big push: %d", len(ps))
	}
	json.Unmarshal(decryptPush(t, ps[0].body, ua, auth), &m)
	if m.Title != "big" || m.Message == "" || !utf8.ValidString(m.Message) {
		t.Fatalf("big payload: title %q message %d bytes", m.Title, len(m.Message))
	}

	// A 500 records a Web Push failure for delivery health (#661), while a 410
	// drops the stale subscription without counting as a failure.
	status = 500
	publish(map[string]any{"topic": "alerts", "message": "m"})
	if len(pushes()) != 1 {
		t.Fatal("no push before the 500")
	}
	var fails int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM push_failures WHERE topic = 'alerts'`).Scan(&fails); err != nil || fails != 1 {
		t.Fatalf("push_failures after 500: %v %d, want 1", err, fails)
	}

	// A 410 drops the subscription.
	status = 410
	publish(map[string]any{"topic": "alerts", "message": "m"})
	if len(pushes()) != 1 {
		t.Fatal("no push before the 410")
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM push_failures WHERE topic = 'alerts'`).Scan(&fails); err != nil || fails != 1 {
		t.Fatalf("push_failures after 410: %v %d, want 1", err, fails)
	}
	if _, info := call(t, ts, "GET", "/api/v1/push", phone, nil); info["subscribed"] != false {
		t.Fatal("a 410 kept the subscription")
	}

	// DELETE and revoking the token both drop it.
	status = 201
	call(t, ts, "PUT", "/api/v1/push", phone, sub)
	if code, _ := call(t, ts, "DELETE", "/api/v1/push", phone, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if _, info := call(t, ts, "GET", "/api/v1/push", phone, nil); info["subscribed"] != false {
		t.Fatal("DELETE kept the subscription")
	}
	call(t, ts, "PUT", "/api/v1/push", phone, sub)
	tok := s.lookupToken(ctx, phone)
	if err := s.DeleteToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	s.db.QueryRow(`SELECT COUNT(*) FROM push_subs`).Scan(&left)
	if left != 0 {
		t.Fatalf("revoked token kept %d subscriptions", left)
	}
}
