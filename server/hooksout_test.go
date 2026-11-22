package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
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

	// The admin opt-in (#237): the address and the port must both be listed.
	pub := mkToken(t, s, "ci", []string{ScopePublish, ScopeRead}, []string{"alerts"})
	if code, _ := call(t, ts, "PUT", "/api/v1/hooks/allow", pub, map[string]any{"networks": []string{}, "ports": []int{}}); code != 403 {
		t.Fatalf("publish token set the allow list: %d", code)
	}
	for _, bad := range []map[string]any{{}, {"networks": []string{"lan"}, "ports": []int{}}, {"networks": []string{}, "ports": []int{0}}} {
		if code, _ := call(t, ts, "PUT", "/api/v1/hooks/allow", admin, bad); code != 400 {
			t.Fatalf("%v: %d", bad, code)
		}
	}
	port, _ := strconv.Atoi(url[strings.LastIndexByte(url, ':')+1:])
	allow := func(networks []string, ports []int) {
		t.Helper()
		if code, a := call(t, ts, "PUT", "/api/v1/hooks/allow", admin, map[string]any{"networks": networks, "ports": ports}); code != 200 {
			t.Fatalf("allow: %d %v", code, a)
		}
	}
	fire := func() int {
		t.Helper()
		_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "m"})
		call(t, ts, "POST", "/api/v1/notifications/"+n["id"].(string)+"/dismiss", admin, nil)
		if err := s.deliverHooks(context.Background()); err != nil {
			t.Fatal(err)
		}
		return len(hits())
	}
	allow([]string{"127.0.0.1"}, []int{})
	if fire() != 0 {
		t.Fatal("reached an unlisted port")
	}
	allow([]string{}, []int{port})
	if fire() != 0 {
		t.Fatal("reached an unlisted address")
	}
	allow([]string{"127.0.0.9/8"}, []int{port, port})
	if _, a := call(t, ts, "GET", "/api/v1/hooks/allow", admin, nil); fmt.Sprint(a["networks"], a["ports"]) != fmt.Sprintf("[127.0.0.0/8] [%d]", port) {
		t.Fatalf("stored list: %v", a)
	}
	if fire() != 1 {
		t.Fatal("listed address and port not reached")
	}
	// The favicon fetcher keeps its own guard and connections.
	if _, _, err := s.fetch.get(context.Background(), mustURL(url), 10); !errors.Is(err, errBlocked) {
		t.Fatalf("fetcher reached loopback: %v", err)
	}
	allow([]string{}, []int{})
	if fire() != 1 {
		t.Fatal("a pooled connection outlived the allow list")
	}

	// The operator lock (#498): a saved list is ignored, and no token changes it.
	allow([]string{"127.0.0.1"}, []int{port})
	s2, err := New(Options{DB: s.DBPath(), Logger: s.opt.Logger, LockHookAllow: true})
	if err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(s2)
	defer func() { ts2.Close(); s2.Close() }()
	_, n = call(t, ts2, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "m"})
	call(t, ts2, "POST", "/api/v1/notifications/"+n["id"].(string)+"/dismiss", admin, nil)
	if err := s2.deliverHooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hits()) != 1 {
		t.Fatal("a saved list opened the LAN while locked")
	}
	if _, a := call(t, ts2, "GET", "/api/v1/hooks/allow", admin, nil); a["locked"] != true || fmt.Sprint(a["networks"], a["ports"]) != "[] []" {
		t.Fatalf("GET while locked: %v", a)
	}
	if code, a := call(t, ts2, "PUT", "/api/v1/hooks/allow", admin, map[string]any{"networks": []string{}, "ports": []int{}}); code != 403 || a["code"] != "locked" {
		t.Fatalf("PUT while locked: %d %v", code, a)
	}
	if _, out := call(t, ts2, "GET", "/api/v1/server", admin, nil); out["env"].(map[string]any)["CLRD_HOOK_ALLOW"] != "off" {
		t.Fatalf("server settings while locked: %v", out["env"])
	}
}

func TestHookAllowPermits(t *testing.T) {
	if _, err := cleanHookAllow([]string{"::ffff:0:0/95"}, nil); err == nil {
		t.Fatal("a mapped range wider than IPv4 was accepted")
	}
	a, err := cleanHookAllow([]string{"10.0.0.0/8", "::ffff:192.168.1.0/120", "fd00::1", "fe80::/64"}, []int{8080})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(a.Networks) != "[10.0.0.0/8 192.168.1.0/24 fd00::1/128 fe80::/64]" {
		t.Fatalf("networks: %v", a.Networks)
	}
	for _, c := range []struct {
		ip, port string
		want     bool
	}{
		{"10.1.2.3", "8080", true}, {"10.1.2.3", "443", true}, {"::ffff:10.1.2.3", "80", true},
		{"192.168.1.5", "8080", true}, {"fd00::1", "8080", true}, {"8.8.8.8", "8080", true}, {"fe80::1%eth0", "80", true},
		{"10.1.2.3", "22", false}, {"192.168.2.5", "80", false}, {"169.254.169.254", "80", false},
		{"127.0.0.1", "8080", false}, {"fd00::2", "80", false},
	} {
		if got := a.permits(netip.MustParseAddr(c.ip), c.port); got != c.want {
			t.Errorf("%s:%s = %v", c.ip, c.port, got)
		}
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

// A body template (#239): the receiver gets its own JSON shape, signed as
// sent; a template that does not make JSON sends nothing and says why.
func TestHooksOutBody(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	var status atomic.Int64
	status.Store(200)
	url, hits := receiver(t, &status)
	path := "/api/v1/topics/alerts/hooks/slack"
	if code, _ := call(t, ts, "PUT", path, admin, map[string]any{"url": url, "body": "{{range .}}{{end}}"}); code != 400 {
		t.Fatalf("range template saved: %d", code)
	}
	slack := `{"text": {{json .notification.title}}, "seq": {{.seq}}}`
	if code, h := call(t, ts, "PUT", path, admin, map[string]any{"url": url, "body": slack, "secret": "s3"}); code != 200 || h["body"] != slack {
		t.Fatalf("save: %d %v", code, h)
	}
	resolve := func(title string) {
		_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "title": title, "message": "m"})
		call(t, ts, "POST", "/api/v1/notifications/"+n["id"].(string)+"/resolve", admin, nil)
		if err := s.deliverHooks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	title := "He said \"hi\"\nthen left"
	resolve(title)
	got := hits()
	if len(got) != 1 || got[0].body["text"] != title || got[0].body["seq"] == nil || len(got[0].body) != 2 {
		t.Fatalf("templated body: %v", got)
	}
	mac := hmac.New(sha256.New, []byte("s3"))
	mac.Write(got[0].raw)
	if got[0].sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature does not match the sent body")
	}

	// The web form has no body field: its save keeps the template, even
	// when it clears the secret.
	if err := s.SaveHook(ctx, serverToken, "alerts", "slack", OutHook{URL: url}, false, true); err != nil {
		t.Fatal(err)
	}
	if hs, _ := s.OutHooks(ctx, serverToken, "alerts"); hs[0].Body != slack || hs[0].HasSecret {
		t.Fatalf("after web save: %+v", hs[0])
	}

	// Not JSON: nothing is sent and the delivery log says why.
	call(t, ts, "PUT", path, admin, map[string]any{"url": url, "body": `{"text": {{.notification.title}}}`})
	resolve("plain")
	if len(hits()) != 1 {
		t.Fatalf("a body that is not JSON was sent")
	}
	_, log := call(t, ts, "GET", path+"/deliveries", admin, nil)
	if d := log["deliveries"].([]any)[0].(map[string]any); d["status"] != "pending" || !strings.Contains(d["error"].(string), "not valid JSON") {
		t.Fatalf("delivery: %v", d)
	}

	// Big numbers print as digits.
	if out, err := renderHookBody(`{"seq": {{.seq}}}`, `{"seq": 1000000}`); err != nil || out != `{"seq": 1000000}` {
		t.Fatalf("seq: %q %v", out, err)
	}
}

// TestHooksOutPreview: the dry run renders like a delivery and sends nothing (#241).
func TestHooksOutPreview(t *testing.T) {
	_, ts, admin := newTest(t)
	path := "/api/v1/topics/alerts/hooks/preview"
	tmpl := `{"text": {{json .notification.title}}, "topic": {{json .notification.topic}}}`
	tmpl2 := `{"text": {{json .notification.title}}, "topic": {{json .notification.topic}}, "p": {{.notification.priority}}}`
	if code, out := call(t, ts, "POST", path, admin, map[string]any{"body": tmpl2}); code != 200 ||
		out["body"] != `{"text": "Disk full", "topic": "alerts", "p": 3}` {
		t.Fatalf("sample: %d %v", code, out)
	}
	ev := map[string]any{"seq": 7, "notification": map[string]any{"title": "a <b>", "topic": "x"}}
	if code, out := call(t, ts, "POST", path, admin, map[string]any{"body": tmpl, "event": ev}); code != 200 ||
		out["body"] != `{"text": "a \u003cb\u003e", "topic": "x"}` {
		t.Fatalf("event: %d %v", code, out)
	}
	if code, out := call(t, ts, "POST", path, admin, map[string]any{"body": `{"text": {{.notification.title}}}`}); code != 400 ||
		!strings.Contains(fmt.Sprint(out), "not valid JSON") {
		t.Fatalf("bad output: %d %v", code, out)
	}
	if code, out := call(t, ts, "POST", path, admin, map[string]any{}); code != 400 || !strings.Contains(fmt.Sprint(out), "body is required") {
		t.Fatalf("no body: %d %v", code, out)
	}
}
