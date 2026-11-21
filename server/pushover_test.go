package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"mime/multipart"
	"net/url"
	"testing"
)

func pushoverPost(t *testing.T, ts string, v url.Values) (int, map[string]any) {
	t.Helper()
	return gotifyPost(t, ts+"/1/messages.json", "application/x-www-form-urlencoded", v.Encode(), nil)
}

func TestPushoverPublish(t *testing.T) {
	s, ts, admin := newTest(t)
	code, app := call(t, ts, "POST", "/api/v1/pushover/apps", admin, map[string]any{"name": "Kuma", "topic": "up"})
	key, _ := app["token"].(string)
	user, _ := app["user"].(string)
	if code != 200 || len(key) != 30 || len(user) != 30 || app["token_id"] == nil {
		t.Fatalf("create: %d %v", code, app)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/pushover/apps", mkToken(t, s, "p", []string{ScopePublish}, nil), map[string]any{"name": "x", "topic": "up"}); code != 403 {
		t.Fatalf("create without admin: %d", code)
	}

	// A form, the way Apprise and curl send it. No title: the app name.
	code, m := pushoverPost(t, ts.URL, url.Values{"token": {key}, "user": {user}, "message": {"*down*"}, "url": {"https://example.com/s"}, "priority": {"1"}, "ttl": {"600"}})
	if code != 200 || m["status"] != 1.0 || m["request"] == nil || m["receipt"] != nil {
		t.Fatalf("form publish: %d %v", code, m)
	}
	n := latest(t, ts.URL, admin, "up")
	if n["title"] != "Kuma" || n["message"] != "*down*" || n["format"] != "text" || n["priority"] != 4.0 || n["click"] != "https://example.com/s" || n["expires_at"] == nil {
		t.Fatalf("form stored: %v", n)
	}

	// JSON, numbers as numbers.
	code, m = gotifyPost(t, ts.URL+"/1/messages.json", "application/json", `{"token":"`+key+`","user":"u","title":"J","message":"m","priority":-2}`, nil)
	if n = latest(t, ts.URL, admin, "up"); code != 200 || n["title"] != "J" || n["priority"] != 1.0 {
		t.Fatalf("json publish: %d %v %v", code, m, n)
	}

	// html=1 becomes Markdown.
	pushoverPost(t, ts.URL, url.Values{"token": {key}, "user": {"u"}, "html": {"1"}, "message": {`<b>Disk</b> at <i>91%</i><br><a href="https://x.example/d">open</a> <font color="red">&amp; now</font>`}})
	if n = latest(t, ts.URL, admin, "up"); n["format"] != "markdown" || n["message"] != "**Disk** at *91%*\n[open](https://x.example/d) & now" {
		t.Fatalf("html: %q %v", n["message"], n["format"])
	}

	// Emergency: repeats every retry (60s at least) until expire, and a receipt.
	code, m = pushoverPost(t, ts.URL, url.Values{"token": {key}, "user": {"u"}, "message": {"fire"}, "priority": {"2"}, "retry": {"30"}, "expire": {"600"}})
	r, _ := m["receipt"].(string)
	if n = latest(t, ts.URL, admin, "up"); code != 200 || len(r) != 30 || n["priority"] != 5.0 || n["repeat"] == nil {
		t.Fatalf("emergency: %d %v %v", code, m, n)
	}
	// A window shorter than clrd's 60s repeat still rings once more.
	pushoverPost(t, ts.URL, url.Values{"token": {key}, "user": {"u"}, "message": {"short"}, "priority": {"2"}, "retry": {"30"}, "expire": {"45"}})
	if n = latest(t, ts.URL, admin, "up"); n["repeat"] == nil {
		t.Fatalf("short emergency: %v", n)
	}
	for _, bad := range []url.Values{
		{"priority": {"2"}, "expire": {"600"}},
		{"priority": {"2"}, "retry": {"20"}, "expire": {"600"}},
		{"priority": {"2"}, "retry": {"60"}, "expire": {"10801"}},
		{"priority": {"3"}},
		{"ttl": {"-1"}},
		{"message": {""}},
		{"user": {""}},
	} {
		v := url.Values{"token": {key}, "user": {"u"}, "message": {"m"}}
		for k, x := range bad {
			v[k] = x
		}
		if code, m := pushoverPost(t, ts.URL, v); code != 400 || m["status"] != 0.0 || len(m["errors"].([]any)) != 1 {
			t.Fatalf("%v: %d %v", bad, code, m)
		}
	}
	if code, m := pushoverPost(t, ts.URL, url.Values{"token": {key}, "message": {"m"}}); code != 400 || m["user"] != "invalid" {
		t.Fatalf("no user: %d %v", code, m)
	}

	// A file, multipart, and one in base64.
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	mw.WriteField("token", key)
	mw.WriteField("user", "u")
	mw.WriteField("message", "snapshot")
	fw, _ := mw.CreateFormFile("attachment", "cam.png")
	fw.Write(pngBytes)
	mw.Close()
	if code, m = gotifyPost(t, ts.URL+"/1/messages.json", mw.FormDataContentType(), mp.String(), nil); code != 200 {
		t.Fatalf("multipart: %d %v", code, m)
	}
	if att, _ := latest(t, ts.URL, admin, "up")["attachment"].(map[string]any); att["name"] != "cam.png" {
		t.Fatalf("multipart attachment: %v", att)
	}
	// Bigger than the 1 MB a plain body may be, as Pushover takes up to 5 MB.
	big := append(append([]byte{}, pngBytes...), make([]byte, 1<<20)...)
	if code, m = pushoverPost(t, ts.URL, url.Values{"token": {key}, "user": {"u"}, "message": {"b64"}, "attachment_base64": {base64.StdEncoding.EncodeToString(big)}, "attachment_type": {"image/png"}}); code != 200 {
		t.Fatalf("base64: %d %v", code, m)
	}
	if att, _ := latest(t, ts.URL, admin, "up")["attachment"].(map[string]any); att["name"] != "attachment.png" || att["size"] != float64(len(big)) {
		t.Fatalf("base64 attachment: %v", att)
	}

	// validate.json
	code, m = gotifyPost(t, ts.URL+"/1/users/validate.json", "application/x-www-form-urlencoded", url.Values{"token": {key}, "user": {user}}.Encode(), nil)
	if code != 200 || m["status"] != 1.0 || m["devices"] == nil {
		t.Fatalf("validate: %d %v", code, m)
	}
}

// The key picks the topic, every native check holds, and the key works
// nowhere else.
func TestPushoverAuth(t *testing.T) {
	s, ts, admin := newTest(t)
	before := s.seq.Load()
	post := func(key string) (int, map[string]any) {
		return pushoverPost(t, ts.URL, url.Values{"token": {key}, "user": {"u"}, "message": {"m"}})
	}
	if code, m := post("azGDORePK8gMaC0QOYAMyEEuzJnyUi"); code != 400 || m["token"] != "invalid" || m["status"] != 0.0 {
		t.Fatalf("bad key: %d %v", code, m)
	}
	if code, m := post(""); code != 400 || m["token"] != "invalid" {
		t.Fatalf("no key: %d %v", code, m)
	}
	appTokenChecks(t, s, admin, post)
	// A stored key that is not a Pushover app key (an imported Gotify key,
	// kept in its public form) never sends here.
	stored := "azGDORePK8gMaC0QOYAMyEEuzJnyUj"
	other := &Token{ID: randomString(12), Name: "g", Scopes: []string{ScopePublish}, Topics: []string{"up"}, CreatedAt: now()}
	if err := insertToken(context.Background(), s.db, other, hashToken(stored)); err != nil {
		t.Fatal(err)
	}
	if code, _ := post(stored); code != 400 {
		t.Fatalf("other stored key: %d", code)
	}
	// validate.json says valid only for a key that could send.
	if code, m := gotifyPost(t, ts.URL+"/1/users/validate.json", "application/x-www-form-urlencoded", url.Values{"token": {mkToken(t, s, "r2", []string{ScopeRead}, []string{"up"})}, "user": {"u"}}.Encode(), nil); code != 403 || m["status"] != 0.0 {
		t.Fatalf("validate read-only: %d %v", code, m)
	}
	if s.seq.Load() != before {
		t.Fatal("a refused Pushover publish stored something")
	}

	// A clrd token limited to one topic works.
	if code, m := post(mkToken(t, s, "one", []string{ScopePublish}, []string{"up"})); code != 200 {
		t.Fatalf("clrd token: %d %v", code, m)
	}

	_, app := call(t, ts, "POST", "/api/v1/pushover/apps", admin, map[string]any{"name": "Kuma", "topic": "up"})
	key := app["token"].(string)
	if code, _ := call(t, ts, "GET", "/api/v1/notifications?topic=up", key, nil); code != 401 {
		t.Fatalf("native API: %d", code)
	}
	if code, _ := raw(t, "PUT", ts.URL+"/up", key, "x", nil); code != 401 {
		t.Fatalf("ntfy publish: %d", code)
	}
	if code, _ := gotifyPost(t, ts.URL+"/message", "application/json", `{"message":"m"}`, map[string]string{"X-Gotify-Key": key}); code != 401 {
		t.Fatalf("gotify publish: %d", code)
	}
	if code, _ := call(t, ts, "DELETE", "/api/v1/tokens/"+app["token_id"].(string), admin, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := post(key); code != 400 {
		t.Fatalf("revoked key: %d", code)
	}
}
