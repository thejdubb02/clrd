package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type hookHit struct {
	event, sig string
	body       map[string]any
	raw        []byte
}

// receiver records every POST and answers with status.
func receiver(t *testing.T, status *atomic.Int64) (string, func() []hookHit) {
	var mu sync.Mutex
	var hits []hookHit
	ts, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		h := hookHit{event: r.Header.Get("X-Clrd-Event"), sig: r.Header.Get("X-Clrd-Signature"), raw: raw}
		json.Unmarshal(raw, &h.body)
		mu.Lock()
		hits = append(hits, h)
		mu.Unlock()
		w.WriteHeader(int(status.Load()))
	})
	return ts.URL, func() []hookHit { mu.Lock(); defer mu.Unlock(); return append([]hookHit(nil), hits...) }
}

func TestHooksOut(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	var status atomic.Int64
	status.Store(200)
	url, hits := receiver(t, &status)

	// Only an admin manages hooks, and only http(s) URLs and known events.
	pub := mkToken(t, s, "ci", []string{ScopePublish, ScopeRead}, []string{"alerts"})
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/alerts/hooks/gh", pub, map[string]any{"url": url}); code != 403 {
		t.Fatalf("publish token saved a hook: %d", code)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/alerts/hooks/gh", admin, map[string]any{"url": "ftp://x.test/"}); code != 400 {
		t.Fatalf("ftp url: %d", code)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/alerts/hooks/gh", admin, map[string]any{"url": url, "events": []string{"opened"}}); code != 400 {
		t.Fatalf("unknown event: %d", code)
	}
	code, h := call(t, ts, "PUT", "/api/v1/topics/alerts/hooks/gh", admin, map[string]any{"url": url, "events": []string{"resolved", "acknowledged"}, "secret": "s3"})
	if code != 200 || h["has_secret"] != true || h["secret"] != nil {
		t.Fatalf("save: %d %v", code, h)
	}

	send := func(key string, actions ...map[string]any) string {
		_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "external_id": key, "message": "m", "actions": actions})
		return n["id"].(string)
	}
	a := send("a")
	b := send("b", map[string]any{"label": "Yes", "reply": "yes"})
	c := send("c")
	call(t, ts, "POST", "/api/v1/notifications/"+a+"/dismiss", admin, nil)                                                                     // not subscribed
	call(t, ts, "POST", "/api/v1/notifications/"+b+"/reply", mkToken(t, s, "phone", []string{ScopeRead}, nil), map[string]any{"reply": "yes"}) // not the sender (#187)
	call(t, ts, "POST", "/api/v1/notifications/"+c+"/resolve", admin, nil)
	if err := s.deliverHooks(ctx); err != nil {
		t.Fatal(err)
	}
	got := hits()
	if len(got) != 2 {
		t.Fatalf("want the reply and the resolve, got %d: %v", len(got), got)
	}
	byEvent := map[string]hookHit{got[0].event: got[0], got[1].event: got[1]}
	reply, res := byEvent["action"], byEvent["resolved"]
	n, _ := reply.body["notification"].(map[string]any)
	if reply.body["type"] != EventState || n["reply"] != "yes" || n["external_id"] != "b" || reply.body["seq"] == nil {
		t.Fatalf("reply body: %s", reply.raw)
	}
	mac := hmac.New(sha256.New, []byte("s3"))
	mac.Write(reply.raw)
	if reply.sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("bad signature %q", reply.sig)
	}
	if n, _ := res.body["notification"].(map[string]any); n["state"] != Resolved {
		t.Fatalf("resolve body: %s", res.raw)
	}
	// Sent once only.
	s.deliverHooks(ctx)
	if len(hits()) != 2 {
		t.Fatalf("resent: %d", len(hits()))
	}
	_, log := call(t, ts, "GET", "/api/v1/topics/alerts/hooks/gh/deliveries", admin, nil)
	if ds, _ := log["deliveries"].([]any); len(ds) != 2 || ds[0].(map[string]any)["status"] != "ok" {
		t.Fatalf("delivery log: %v", log)
	}

	// A failing receiver: the delivery waits for a retry, then a streak
	// longer than hookDisableAfter turns the hook off and tells the topic.
	status.Store(500)
	call(t, ts, "POST", "/api/v1/notifications/"+send("d")+"/resolve", admin, nil)
	s.deliverHooks(ctx)
	_, log = call(t, ts, "GET", "/api/v1/topics/alerts/hooks/gh/deliveries", admin, nil)
	d := log["deliveries"].([]any)[0].(map[string]any)
	if d["status"] != "pending" || d["attempts"] != 1.0 || d["code"] != 500.0 || d["next_at"] == nil {
		t.Fatalf("failed delivery: %v", d)
	}
	s.deliverHooks(ctx) // not due yet
	if len(hits()) != 3 {
		t.Fatalf("retried before the backoff: %d", len(hits()))
	}
	s.db.Exec(`UPDATE hook_deliveries SET next_at = 0`)
	s.db.Exec(`UPDATE hooks_out SET failing_since = ?`, ms(time.Now().Add(-25*time.Hour)))
	s.deliverHooks(ctx)
	_, list := call(t, ts, "GET", "/api/v1/topics/alerts/hooks", admin, nil)
	if hk := list["hooks"].([]any)[0].(map[string]any); hk["enabled"] != false {
		t.Fatalf("hook still on: %v", hk)
	}
	if off := one(t, s, "alerts", "webhook-off-gh"); !strings.Contains(off.Message, "127.0.0.1") || !strings.Contains(off.Message, "HTTP 500") {
		t.Fatalf("notice: %q", off.Message)
	}
	// Off means nothing is queued or sent; saving again turns it back on.
	call(t, ts, "POST", "/api/v1/notifications/"+send("e")+"/resolve", admin, nil)
	s.db.Exec(`UPDATE hook_deliveries SET next_at = 0`)
	before := len(hits())
	s.deliverHooks(ctx)
	if len(hits()) != before {
		t.Fatal("a turned-off hook was sent")
	}
	status.Store(200)
	call(t, ts, "PUT", "/api/v1/topics/alerts/hooks/gh", admin, map[string]any{"url": url})
	s.deliverHooks(ctx)
	if len(hits()) != before+1 {
		t.Fatalf("pending delivery not sent after turning back on: %d", len(hits())-before)
	}
	if code, _ := call(t, ts, "DELETE", "/api/v1/topics/alerts/hooks/gh", admin, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	var left int
	s.db.QueryRow(`SELECT COUNT(*) FROM hook_deliveries`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d deliveries left after delete", left)
	}
}

// The default guard refuses a private address: the receiver is never hit and
// the delivery log says why.
func TestHooksOutGuard(t *testing.T) {
	s, ts, admin := newTest(t)
	var status atomic.Int64
	status.Store(200)
	url, hits := receiver(t, &status)
	call(t, ts, "PUT", "/api/v1/topics/alerts/hooks/local", admin, map[string]any{"url": url})
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "m"})
	call(t, ts, "POST", "/api/v1/notifications/"+n["id"].(string)+"/dismiss", admin, nil)
	if err := s.deliverHooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hits()) != 0 {
		t.Fatal("default guard reached loopback")
	}
	_, log := call(t, ts, "GET", "/api/v1/topics/alerts/hooks/local/deliveries", admin, nil)
	if d := log["deliveries"].([]any)[0].(map[string]any); !strings.Contains(d["error"].(string), errBlocked.Error()) {
		t.Fatalf("delivery: %v", d)
	}
}

// one returns the notification with that key on the topic.
func one(t *testing.T, s *Server, topic, key string) *Notification {
	t.Helper()
	all, err := s.List(context.Background(), serverToken, ListQuery{Topics: []string{topic}, State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range all {
		if n.ExternalID == key {
			return n
		}
	}
	t.Fatalf("no %s/%s", topic, key)
	return nil
}

// Races from the #112 review (#240): a send whose outcome lands after the hook
// changed under it.
func TestHooksOutRaces(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	save := func() {
		if code, _ := call(t, ts, "PUT", "/api/v1/topics/alerts/hooks/gh", admin, map[string]any{"url": "https://x.test/"}); code != 200 {
			t.Fatalf("save: %d", code)
		}
	}
	queued := func() hookJob {
		_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "m"})
		call(t, ts, "POST", "/api/v1/notifications/"+n["id"].(string)+"/dismiss", admin, nil)
		j := hookJob{topic: "alerts", name: "gh", url: "https://x.test/"}
		if err := s.db.QueryRow(`SELECT MAX(id) FROM hook_deliveries`).Scan(&j.id); err != nil {
			t.Fatal(err)
		}
		return j
	}
	since := func() (on bool, failing bool) {
		var f sql.NullInt64
		s.db.QueryRow(`SELECT enabled, failing_since FROM hooks_out WHERE topic = 'alerts' AND name = 'gh'`).Scan(&on, &f)
		return on, f.Valid
	}
	old := time.Now().Add(-25 * time.Hour)

	// Deleted and saved again while a send was out: the late 2xx does not
	// clear the new hook's failure streak.
	save()
	j := queued()
	call(t, ts, "DELETE", "/api/v1/topics/alerts/hooks/gh", admin, nil)
	save()
	s.db.Exec(`UPDATE hooks_out SET failing_since = ?`, ms(old))
	if err := s.recordHook(ctx, j, 200, nil); err != nil {
		t.Fatal(err)
	}
	if _, failing := since(); !failing {
		t.Fatal("a send to the deleted hook cleared the new one's streak")
	}

	// Another send turned the hook off: a success that lands after it keeps
	// the streak, so the off hook still says since when it failed.
	save()
	a, b := queued(), queued()
	s.db.Exec(`UPDATE hooks_out SET failing_since = ?`, ms(old))
	if err := s.recordHook(ctx, a, 500, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.recordHook(ctx, b, 200, nil); err != nil {
		t.Fatal(err)
	}
	if on, failing := since(); on || !failing {
		t.Fatalf("after off then success: enabled %v, failing %v", on, failing)
	}

	// Shutdown starts while a failure is recorded: the hook still turns off
	// with its notice.
	save()
	c := queued()
	s.db.Exec(`UPDATE hooks_out SET failing_since = ?`, ms(old))
	s.db.Exec(`DELETE FROM notifications WHERE external_id = 'webhook-off-gh'`)
	cut, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.recordHook(cut, c, 500, nil); err != nil {
		t.Fatal(err)
	}
	if on, _ := since(); on {
		t.Fatal("hook still on")
	}
	one(t, s, "alerts", "webhook-off-gh")
}
