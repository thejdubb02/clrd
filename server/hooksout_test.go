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
	"net/url"
	"path/filepath"
	"slices"
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
	// Existing publishers can use both keys independently of the server notice.
	for _, key := range []string{"webhook-off-gh", "clrd-webhook-off-gh"} {
		send(key)
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
	if off := hookNotice(t, s, "alerts", "gh"); !strings.Contains(off.Message, "127.0.0.1") || !strings.Contains(off.Message, "HTTP 500") {
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
	if off := hookNotice(t, s, "alerts", "gh"); off.State != "resolved" {
		t.Fatalf("turned back on, notice still %s", off.State) // #893
	}
	for _, key := range []string{"webhook-off-gh", "clrd-webhook-off-gh"} {
		if n := one(t, s, "alerts", key); n.State != Active || n.Message != "m" {
			t.Fatalf("webhook lifecycle touched publisher alert %q: %+v", key, n)
		}
	}
	s.deliverHooks(ctx)
	if len(hits()) != before+2 { // the pending one, and the notice resolving
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
		if err := s.db.QueryRow(`SELECT MAX(d.id), h.rev FROM hook_deliveries d JOIN hooks_out h ON h.topic = d.topic AND h.name = d.name`).Scan(&j.id, &j.rev); err != nil {
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
	s.db.Exec(`DELETE FROM notifications WHERE hook_notice = 'gh'`)
	cut, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.recordHook(cut, c, 500, nil); err != nil {
		t.Fatal(err)
	}
	if on, _ := since(); on {
		t.Fatal("hook still on")
	}
	hookNotice(t, s, "alerts", "gh")

	// #893: a save lands between the turn-off and its notice (the trigger
	// plays it), so the notice it could not clear is cleared after.
	save()
	c = queued()
	s.db.Exec(`UPDATE hooks_out SET failing_since = ?`, ms(old))
	s.db.Exec(`DELETE FROM notifications WHERE hook_notice = 'gh'`)
	s.db.Exec(`CREATE TRIGGER resave AFTER UPDATE OF enabled ON hooks_out WHEN NEW.enabled = 0 BEGIN UPDATE hooks_out SET enabled = 1 WHERE rowid = NEW.rowid; END`)
	err := s.recordHook(ctx, c, 500, nil)
	s.db.Exec(`DROP TRIGGER resave`)
	if err != nil {
		t.Fatal(err)
	}
	if off := hookNotice(t, s, "alerts", "gh"); off.State != "resolved" {
		t.Fatalf("hook back on, notice still %s", off.State)
	}

	// #878: a last try to the old URL fails just before a save moves the hook.
	// The save keeps failed rows, but the new URL's streak still starts clean.
	save()
	d := queued()
	d.attempts = hookTries - 1
	s.db.Exec(`UPDATE hook_deliveries SET status = 'failed' WHERE id = ?`, d.id)
	moveTo := func(u string) {
		if code, _ := call(t, ts, "PUT", "/api/v1/topics/alerts/hooks/gh", admin, map[string]any{"url": u}); code != 200 {
			t.Fatalf("save %s: %d", u, code)
		}
	}
	moveTo("https://y.test/")
	if err := s.recordHook(ctx, d, 500, nil); err != nil {
		t.Fatal(err)
	}
	if _, failing := since(); failing {
		t.Fatal("the old URL's failure started the new URL's streak")
	}

	// #878: moved to another URL and back while a post was out: the post's id
	// belongs to a message the save forgot, so it is not kept.
	moveTo("https://x.test/")
	p := queued()
	moveTo("https://y.test/")
	moveTo("https://x.test/")
	if err := s.keepPost(ctx, p, "n1", "m1"); err != nil {
		t.Fatal(err)
	}
	var kept int
	s.db.QueryRow(`SELECT count(*) FROM hook_posts`).Scan(&kept)
	if kept != 0 {
		t.Fatal("a post from before the moves was kept")
	}
	// Saved again with the same URL: same channel, the id is kept.
	p = queued()
	save()
	if err := s.keepPost(ctx, p, "n2", "m2"); err != nil {
		t.Fatal(err)
	}
	if s.db.QueryRow(`SELECT count(*) FROM hook_posts`).Scan(&kept); kept != 1 {
		t.Fatal("a post to the same URL was dropped")
	}
}

// #892: the first of 9 sends moves the hook while the 9th waits for one of
// the 8 slots; the 9th then sends nothing to the old URL.
func TestHooksOutMovedWhileQueued(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	var move sync.Once
	var mu sync.Mutex
	var got []byte
	old, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, b...)
		mu.Unlock()
		move.Do(func() { // a send that ends frees a slot, so the 9th waits for the move
			if err := s.SaveHook(ctx, serverToken, "alerts", "gh", OutHook{URL: "https://y.test/"}, false, false); err != nil {
				t.Error(err)
			}
		})
	})
	call(t, ts, "PUT", "/api/v1/topics/alerts/hooks/gh", admin, map[string]any{"url": old.URL})
	var last string
	for range 9 {
		_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "m"})
		last = n["id"].(string)
		call(t, ts, "POST", "/api/v1/notifications/"+last+"/resolve", admin, nil)
	}
	if err := s.deliverHooks(ctx); err != nil {
		t.Fatal(err)
	}
	if mu.Lock(); len(got) == 0 {
		t.Fatal("no send reached the old URL, so the move never ran")
	} else if strings.Contains(string(got), last) {
		t.Fatal("the 9th send went to the old URL after the move")
	}
	mu.Unlock()
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

// A discord hook posts each new notification and edits that message when its
// state changes, in order, with the token in its URL kept out of errors (#166).
// A rate-limited send waits as long as the receiver asks, not the fixed
// backoff (#859); a longer ask than an hour is held to an hour.
func TestHooksOutRetryAfter(t *testing.T) {
	s, ts, admin := newTest(t)
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	wait := "600"
	rl, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", wait)
		w.WriteHeader(429)
	})
	if code, h := call(t, ts, "PUT", "/api/v1/topics/ops/hooks/rl", admin, map[string]any{"url": rl.URL}); code != 200 {
		t.Fatalf("save: %d %v", code, h)
	}
	for _, want := range []int64{600, 3600} {
		s.db.Exec(`DELETE FROM hook_deliveries`)
		s.db.Exec(`INSERT INTO hook_deliveries (topic, name, seq, event, body, next_at, updated_at) VALUES ('ops', 'rl', 1, 'dismissed', '{}', 0, 0)`)
		if err := s.deliverHooks(context.Background()); err != nil {
			t.Fatal(err)
		}
		var gap int64
		var msg string
		s.db.QueryRow(`SELECT (next_at - updated_at) / 1000, error FROM hook_deliveries`).Scan(&gap, &msg)
		if gap != want || !strings.Contains(msg, "retry after") {
			t.Fatalf("Retry-After %s: next try in %ds (%q), want %ds", wait, gap, msg, want)
		}
		wait = "9223372037" // would wrap a Duration if not capped first
	}
}

// A delivery queued for one destination never goes to another (#871).
func TestHooksOutURLChangeDropsPending(t *testing.T) {
	s, ts, admin := newTest(t)
	path := "/api/v1/topics/ops/hooks/h"
	pending := func() (n int) {
		s.db.QueryRow(`SELECT count(*) FROM hook_deliveries WHERE topic = 'ops' AND name = 'h'`).Scan(&n)
		return
	}
	save := func(url string) {
		t.Helper()
		if code, h := call(t, ts, "PUT", path, admin, map[string]any{"url": url}); code != 200 {
			t.Fatalf("save %s: %d %v", url, code, h)
		}
	}
	save("https://a.example/hook")
	s.db.Exec(`INSERT INTO hook_deliveries (topic, name, seq, event, body, next_at, updated_at) VALUES ('ops', 'h', 1, 'posted', '{}', 0, 0)`)
	save("https://a.example/hook")
	if n := pending(); n != 1 {
		t.Fatalf("same url dropped the pending delivery: %d", n)
	}
	save("https://b.example/hook")
	if n := pending(); n != 0 {
		t.Fatalf("%d deliveries still queued for the old url", n)
	}
}

func TestHooksOutDiscord(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	type hit struct {
		method, path, query string
		body                map[string]any
	}
	var mu sync.Mutex
	var hits []hit
	dc, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		h := hit{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
		json.NewDecoder(r.Body).Decode(&h.body)
		mu.Lock()
		hits = append(hits, h)
		n := len(hits)
		mu.Unlock()
		switch {
		case r.Method == "POST":
			fmt.Fprintf(w, `{"id":"m%d"}`, n)
		case strings.HasPrefix(r.URL.Path, "/gone/"):
			w.WriteHeader(404) // the webhook itself was deleted
			w.Write([]byte(`{"message": "Unknown Webhook", "code": 10015}`))
		case strings.HasSuffix(r.URL.Path, "/m2"):
			w.WriteHeader(404) // someone deleted it in Discord
			w.Write([]byte(`{"message": "Unknown Message", "code": 10008}`))
		}
	})
	path := "/api/v1/topics/ops/hooks/dc"
	for _, bad := range []map[string]any{{"url": dc.URL, "kind": "unknown"}, {"url": dc.URL, "kind": "discord", "body": `{}`}} {
		if code, _ := call(t, ts, "PUT", path, admin, bad); code != 400 {
			t.Fatalf("saved %v: %d", bad, code)
		}
	}
	if code, h := call(t, ts, "PUT", path, admin, map[string]any{"url": dc.URL + "/api/webhooks/1/tok?thread_id=9", "kind": "discord"}); code != 200 || h["kind"] != "discord" {
		t.Fatalf("save: %d %v", code, h)
	}
	_, a := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "Disk full", "message": "@everyone look"})
	_, b := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": "b"})
	call(t, ts, "POST", "/api/v1/notifications/"+a["id"].(string)+"/dismiss", admin, nil)
	call(t, ts, "POST", "/api/v1/notifications/"+b["id"].(string)+"/resolve", admin, nil)
	for range 4 { // one per tick: post a, post b, edit a, edit b
		if err := s.deliverHooks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(hits) != 4 {
		t.Fatalf("want 4 calls in order, got %+v", hits)
	}
	p, e := hits[0], hits[2]
	if p.method != "POST" || p.path != "/api/webhooks/1/tok" || p.query != "thread_id=9&wait=true" || p.body["content"] != "**Disk full**\n@everyone look" ||
		fmt.Sprint(p.body["allowed_mentions"]) != "map[parse:[]]" {
		t.Fatalf("post: %+v", p)
	}
	if e.method != "PATCH" || e.path != "/api/webhooks/1/tok/messages/m1" || e.query != "thread_id=9" ||
		!strings.HasPrefix(e.body["content"].(string), "~~**Disk full**~~\n@everyone look\n-# Cleared by ") {
		t.Fatalf("edit: %+v", e)
	}
	ds, _ := s.HookDeliveries(ctx, serverToken, "ops", "dc", 10)
	for _, d := range ds {
		if d.Status != "ok" { // the edit of the deleted message is done, not retried
			t.Fatalf("delivery: %+v", d)
		}
	}

	// A new URL forgets the old message ids.
	call(t, ts, "PUT", path, admin, map[string]any{"url": dc.URL + "/api/webhooks/2/tok", "kind": "discord"})
	var kept int
	s.db.QueryRow(`SELECT count(*) FROM hook_posts WHERE name = 'dc'`).Scan(&kept)
	if kept != 0 {
		t.Fatalf("%d message ids kept for another channel", kept)
	}
	call(t, ts, "DELETE", path, admin, nil)

	// A deleted webhook is a failure to retry, not a deleted message.
	call(t, ts, "PUT", "/api/v1/topics/ops/hooks/gone", admin, map[string]any{"url": dc.URL + "/gone/1/tok", "kind": "discord"})
	_, g := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": "g"})
	s.deliverHooks(ctx)
	call(t, ts, "POST", "/api/v1/notifications/"+g["id"].(string)+"/dismiss", admin, nil)
	s.deliverHooks(ctx)
	ds, _ = s.HookDeliveries(ctx, serverToken, "ops", "gone", 10)
	if len(ds) != 2 || ds[0].Status != "pending" || ds[0].Code != 404 {
		t.Fatalf("gone: %+v", ds)
	}
	call(t, ts, "DELETE", "/api/v1/topics/ops/hooks/gone", admin, nil)

	// A dead receiver's error leaves out the URL and its token.
	call(t, ts, "PUT", "/api/v1/topics/ops/hooks/dead", admin, map[string]any{"url": "http://127.0.0.1:1/api/webhooks/1/sekrit", "kind": "discord"})
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": "c"})
	s.deliverHooks(ctx)
	ds, _ = s.HookDeliveries(ctx, serverToken, "ops", "dead", 10)
	if len(ds) != 1 || ds[0].Error == "" || strings.Contains(ds[0].Error, "sekrit") {
		t.Fatalf("dead: %+v", ds)
	}
}

// Discord follow-ups (#673): scheduled notifications post on wake (never twice),
// notification updates re-edit the Discord message (keeping acknowledged status),
// and SaveHook with keepSecret/keepBody handles kind changes safely.
func TestHooksOutDiscordFollowups(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	type hit struct {
		method, path string
		content      string
	}
	var mu sync.Mutex
	var hits []hit
	dc, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hits = append(hits, hit{r.Method, r.URL.Path, fmt.Sprint(body["content"])})
		n := len(hits)
		mu.Unlock()
		if r.Method == "POST" {
			fmt.Fprintf(w, `{"id":"m%d"}`, n)
		}
	})
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/ops/hooks/dc", admin, map[string]any{"url": dc.URL + "/api/webhooks/1/tok", "kind": "discord"}); code != 200 {
		t.Fatalf("save discord hook: %d", code)
	}

	// 1. Scheduled notification does not post while snoozed, posts once on wake,
	//    and snoozing + waking again does not post a second message.
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	code, sched := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{
		"topic": "ops", "title": "Backup", "message": "starting", "external_id": "bk-1", "snooze_until": soon,
	})
	if code != 200 {
		t.Fatalf("publish scheduled: %d", code)
	}
	sid := sched["id"].(string)
	s.deliverHooks(ctx)
	if len(hits) != 0 {
		t.Fatalf("scheduled notification posted before wake: %+v", hits)
	}
	// Update while still scheduled: queues nothing (never resets failing_since),
	// and wake posts the latest text.
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{
		"topic": "ops", "title": "Backup", "message": "ready", "external_id": "bk-1",
	})
	if ds, _ := s.HookDeliveries(ctx, serverToken, "ops", "dc", 10); len(ds) != 0 {
		t.Fatalf("updating unposted scheduled notification queued deliveries: %+v", ds)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET snooze_until = 1 WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	if err := s.wakeSnoozed(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.deliverHooks(ctx); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].method != "POST" || hits[0].content != "**Backup**\nready" {
		t.Fatalf("after wake: %+v", hits)
	}
	// Snooze the already-posted notification, then wake it again: must not
	// double-post, and must edit the message to drop the Snoozed status line.
	call(t, ts, "POST", "/api/v1/notifications/"+sid+"/snooze", admin, map[string]any{"for": "1h"})
	s.deliverHooks(ctx)
	if len(hits) != 2 || hits[1].method != "PATCH" || !strings.Contains(hits[1].content, "-# Snoozed by ") {
		t.Fatalf("after snooze: %+v", hits)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET snooze_until = 1 WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	if err := s.wakeSnoozed(ctx); err != nil {
		t.Fatal(err)
	}
	s.deliverHooks(ctx)
	if len(hits) != 3 || hits[2].method != "PATCH" || hits[2].content != "**Backup**\nready" {
		t.Fatalf("after wake from snooze: want PATCH without Snoozed status, got %+v", hits)
	}

	// A client passing via="snooze_end" on reopen must not spoof a server wake
	// and post an old notification that predated the hook.
	if _, err := s.db.Exec(`INSERT INTO notifications (id, topic, title, message, format, priority, state, created_at, updated_at) VALUES ('old1', 'ops', 'Old', 'msg', 'text', 3, 'dismissed', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications/old1/reopen", admin, map[string]any{"via": "snooze_end"}); code != 200 {
		t.Fatalf("reopen old1: %d", code)
	}
	s.deliverHooks(ctx)
	if len(hits) != 3 {
		t.Fatalf("client via=snooze_end on reopen posted to Discord: %+v", hits[3:])
	}

	// 2. Updating a posted notification (upsert by external_id, then PATCH)
	//    re-edits the Discord message, keeping Acknowledged status when acknowledged.
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{
		"topic": "ops", "title": "Backup", "message": "50% done", "external_id": "bk-1",
	})
	s.deliverHooks(ctx)
	if len(hits) != 4 || hits[3].method != "PATCH" || hits[3].path != "/api/webhooks/1/tok/messages/m1" || hits[3].content != "**Backup**\n50% done" {
		t.Fatalf("upsert edit: %+v", hits)
	}
	call(t, ts, "POST", "/api/v1/notifications/"+sid+"/ack", admin, nil)
	s.deliverHooks(ctx)
	call(t, ts, "PATCH", "/api/v1/notifications/"+sid, admin, map[string]any{"message": "90% done"})
	s.deliverHooks(ctx)
	if len(hits) != 6 || hits[5].method != "PATCH" || !strings.HasPrefix(hits[5].content, "**Backup**\n90% done\n-# Acknowledged by ") {
		t.Fatalf("patch edit while acknowledged: %+v", hits)
	}

	// 3. Web form SaveHook (keepSecret=true, keepBody=true) kind rules.
	if err := s.SaveHook(ctx, serverToken, "ops", "plain", OutHook{URL: dc.URL + "/p", Body: `{"x":1}`, Secret: "s3cr3t"}, false, false); err != nil {
		t.Fatal(err)
	}
	// Switching a signed/templated hook to discord drops secret and body.
	if err := s.SaveHook(ctx, serverToken, "ops", "plain", OutHook{URL: dc.URL + "/d", Kind: "discord"}, true, true); err != nil {
		t.Fatalf("switch to discord: %v", err)
	}
	hs, err := s.OutHooks(ctx, serverToken, "ops")
	if err != nil {
		t.Fatalf("OutHooks after switch to discord: %v", err)
	}
	found := false
	for _, h := range hs {
		if h.Name == "plain" {
			found = true
			if h.Kind != "discord" || h.HasSecret || h.Body != "" {
				t.Fatalf("switched discord hook kept secret or body: %+v", h)
			}
		}
	}
	if !found {
		t.Fatalf("hook %q missing after switch to discord: %+v", "plain", hs)
	}
	// Creating a new github hook via web form without a secret is refused.
	if err := s.SaveHook(ctx, serverToken, "ops", "newgh", OutHook{URL: githubAPI + "o/r", Kind: "github"}, true, true); err == nil {
		t.Fatal("new github hook without secret was allowed with keepSecret=true")
	}
	// Switching a github hook to a plain webhook drops the GitHub token even with keepSecret=true.
	if err := s.SaveHook(ctx, serverToken, "ops", "gh1", OutHook{URL: githubAPI + "o/r", Kind: "github", Secret: "ghp_secret"}, false, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHook(ctx, serverToken, "ops", "gh1", OutHook{URL: dc.URL + "/plain", Kind: ""}, true, true); err != nil {
		t.Fatalf("switch github to plain: %v", err)
	}
	hs, err = s.OutHooks(ctx, serverToken, "ops")
	if err != nil {
		t.Fatalf("OutHooks after switch to plain: %v", err)
	}
	found = false
	for _, h := range hs {
		if h.Name == "gh1" {
			found = true
			if h.Kind != "" || h.HasSecret {
				t.Fatalf("switched plain hook leaked GitHub token: %+v", h)
			}
		}
	}
	if !found {
		t.Fatalf("hook %q missing after switch to plain: %+v", "gh1", hs)
	}
}

// A Discord hook moved to a new URL posts a woken notification there: the
// old channel's done post must not block it (#795).
func TestHooksOutDiscordNewURLWake(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	var mu sync.Mutex
	var hits []string
	var during func() // runs inside a send, before the reply
	dc, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		n, f := len(hits), during
		mu.Unlock()
		if f != nil {
			f()
		}
		if r.Method == "POST" {
			fmt.Fprintf(w, `{"id":"m%d"}`, n)
		}
	})
	save := func(path string) {
		t.Helper()
		if err := s.SaveHook(ctx, serverToken, "ops", "dc", OutHook{URL: dc.URL + path, Kind: "discord"}, false, false); err != nil {
			t.Fatal(err)
		}
	}
	wake := func(id string) {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE notifications SET snooze_until = 1 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if err := s.wakeSnoozed(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.deliverHooks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	snooze := func(id string) {
		t.Helper()
		call(t, ts, "POST", "/api/v1/notifications/"+id+"/snooze", admin, map[string]any{"for": "1h"})
		s.deliverHooks(ctx)
	}
	save("/api/webhooks/1/tok")
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	code, sched := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "Backup", "snooze_until": soon})
	if code != 200 {
		t.Fatalf("publish scheduled: %d", code)
	}
	id := sched["id"].(string)
	wake(id)
	snooze(id)
	if want := []string{"POST /api/webhooks/1/tok", "PATCH /api/webhooks/1/tok/messages/m1"}; fmt.Sprint(hits) != fmt.Sprint(want) {
		t.Fatalf("first channel: %v, want %v", hits, want)
	}
	save("/api/webhooks/2/tok")
	wake(id)
	if len(hits) != 3 || hits[2] != "POST /api/webhooks/2/tok" {
		t.Fatalf("after moving the hook, wake: %v, want a POST to the new URL", hits)
	}
	// Saving the same URL again keeps the message: the next wake edits it.
	save("/api/webhooks/2/tok")
	snooze(id)
	wake(id)
	if len(hits) != 5 || hits[3] != "PATCH /api/webhooks/2/tok/messages/m3" || hits[4] != "PATCH /api/webhooks/2/tok/messages/m3" {
		t.Fatalf("same URL resaved, snooze and wake: %v, want two PATCHes of m3", hits)
	}
	// A save that moves the hook while a post is out: the old channel's
	// message id must not be kept for the new URL.
	mu.Lock()
	during = func() {
		if err := s.SaveHook(ctx, serverToken, "ops", "dc", OutHook{URL: dc.URL + "/api/webhooks/3/tok", Kind: "discord"}, false, false); err != nil {
			t.Error(err)
		}
	}
	mu.Unlock()
	code, live := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "Disk"})
	if code != 200 {
		t.Fatalf("publish: %d", code)
	}
	s.deliverHooks(ctx)
	var kept int
	s.db.QueryRow(`SELECT count(*) FROM hook_posts WHERE id = ?`, live["id"]).Scan(&kept)
	if len(hits) != 6 || hits[5] != "POST /api/webhooks/2/tok" || kept != 0 {
		t.Fatalf("moved during a post: %v, kept %d message ids, want the old id dropped", hits, kept)
	}
}

// Adding ?wait=true to a Discord hook's URL keeps its message ids, so the next
// wake edits the message; a new thread_id is another channel and posts (#816).
func TestHooksOutDiscordWaitKeepsPosts(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	var mu sync.Mutex
	var hits []string
	var during func() // runs inside a post, before the reply, once
	dc, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		n, f := len(hits), during
		if r.Method == "POST" {
			during = nil
		}
		mu.Unlock()
		if r.Method == "POST" {
			if f != nil {
				f()
			}
			fmt.Fprintf(w, `{"id":"m%d"}`, n)
		}
	})
	save := func(rest string) {
		t.Helper()
		if err := s.SaveHook(ctx, serverToken, "ops", "dc", OutHook{URL: dc.URL + "/api/webhooks/1/tok" + rest, Kind: "discord"}, false, false); err != nil {
			t.Fatal(err)
		}
	}
	cycle := func(id string) { // snooze, then wake: the wake posts or edits
		t.Helper()
		call(t, ts, "POST", "/api/v1/notifications/"+id+"/snooze", admin, map[string]any{"for": "1h"})
		s.deliverHooks(ctx)
		s.db.Exec(`UPDATE notifications SET snooze_until = 1 WHERE id = ?`, id)
		if err := s.wakeSnoozed(ctx); err != nil {
			t.Fatal(err)
		}
		s.deliverHooks(ctx)
	}
	save("")
	code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "Disk"})
	if code != 200 {
		t.Fatalf("publish: %d", code)
	}
	s.deliverHooks(ctx)
	id := n["id"].(string)
	save("?wait=true")
	var url string
	s.db.QueryRow(`SELECT url FROM hooks_out WHERE name = 'dc'`).Scan(&url)
	if strings.Contains(url, "wait") {
		t.Fatalf("saved url %q, want wait left out", url)
	}
	cycle(id)
	for _, h := range hits[1:] {
		if h != "PATCH /api/webhooks/1/tok/messages/m1" {
			t.Fatalf("after adding wait: %v, want only PATCHes of m1", hits)
		}
	}
	save("?thread_id=9")
	cycle(id)
	if last := hits[len(hits)-1]; last != "POST /api/webhooks/1/tok" {
		t.Fatalf("after a new thread_id: %v, want a POST", hits)
	}
	// A hook saved before #816, unsorted: adding wait, in any order, is no change.
	legacy := func(rest string) {
		s.db.Exec(`UPDATE hooks_out SET url = ? WHERE name = 'dc'`, dc.URL+"/api/webhooks/1/tok"+rest)
	}
	legacy("?with_components=true&thread_id=9")
	save("?thread_id=9&with_components=true&wait=true")
	cycle(id)
	if last := hits[len(hits)-1]; !strings.HasPrefix(last, "PATCH ") {
		t.Fatalf("legacy URL reordered with wait: %v, want a PATCH", hits)
	}
	// A legacy URL with wait, resaved without it while a post is out, keeps the new id.
	legacy("?wait=true")
	mu.Lock()
	during = func() {
		if err := s.SaveHook(ctx, serverToken, "ops", "dc", OutHook{URL: dc.URL + "/api/webhooks/1/tok", Kind: "discord"}, false, false); err != nil {
			t.Error(err)
		}
	}
	mu.Unlock()
	code, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "Load"})
	if code != 200 {
		t.Fatalf("publish: %d", code)
	}
	s.deliverHooks(ctx)
	var kept int
	s.db.QueryRow(`SELECT count(*) FROM hook_posts WHERE id = ?`, n["id"]).Scan(&kept)
	if kept != 1 {
		t.Fatalf("wait dropped during a post: kept %d message ids, want 1", kept)
	}
}

type slackTestTransport func(*http.Request) (*http.Response, error)

func (f slackTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestHooksOutSlack(t *testing.T) {
	s, err := New(Options{DB: filepath.Join(t.TempDir(), "clrd.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	admin := mkToken(t, s, "admin", []string{ScopeAdmin}, nil)
	api := func(method, path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	const token = "xoxb-test"
	type hit struct {
		method, path, auth string
		body               map[string]any
	}
	var mu sync.Mutex
	var hits []hit
	s.hookHTTP.Store(&http.Client{Transport: slackTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "slack.com" {
			return nil, errors.New("wrong Slack host")
		}
		h := hit{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		json.NewDecoder(r.Body).Decode(&h.body)
		mu.Lock()
		defer mu.Unlock()
		hits = append(hits, h)
		reply := `{"ok":true}`
		switch r.URL.Path {
		case "/api/chat.postMessage":
			reply = `{"ok":true,"channel":"C999","ts":"123.456"}`
			if h.body["channel"] == "Cbad" {
				reply = `{"ok":false,"error":"invalid_auth xoxb-test"}`
			}
		case "/api/chat.update":
			if len(hits) == 7 {
				reply = `{"ok":false,"error":"message_not_found"}`
			}
		case "/api/reactions.add":
			reply = `{"ok":false,"error":"already_reacted"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})})
	path := "/api/v1/topics/ops/hooks/slack"
	postURL := slackAPI + "chat.postMessage"
	for _, bad := range []map[string]any{
		{"url": postURL + "?channel=C0123", "kind": "slack"},
		{"url": postURL, "kind": "slack", "secret": token},
		{"url": postURL + "?channel=", "kind": "slack", "secret": token},
		{"url": postURL + "Other?channel=C0123", "kind": "slack", "secret": token},
		{"url": "https://example.com/api/chat.postMessage?channel=C0123", "kind": "slack", "secret": token},
		{"url": postURL + "?channel=C0123", "kind": "slack", "secret": token, "body": "{}"},
	} {
		if code, _ := api("PUT", path, bad); code != 400 {
			t.Fatalf("invalid Slack save returned %d, want 400", code)
		}
	}
	save := func(channel string) {
		t.Helper()
		if code, h := api("PUT", path, map[string]any{"url": postURL + "?channel=" + channel, "kind": "slack", "secret": token}); code != 200 ||
			h["kind"] != "slack" || h["secret"] != nil || h["has_secret"] != true {
			t.Fatalf("save: %d %v", code, h)
		}
	}
	deliver := func() {
		t.Helper()
		if err := s.deliverHooks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	checkHits := func(paths ...string) []hit {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(hits) != len(paths) {
			t.Fatalf("want %v, got %+v", paths, hits)
		}
		for i, path := range paths {
			if h := hits[i]; h.path != "/api/"+path || h.method != "POST" || h.auth != "Bearer "+token {
				t.Fatalf("request %d: %+v", i, h)
			}
		}
		return slices.Clone(hits)
	}
	save("C0123")
	_, a := api("POST", "/api/v1/notifications", map[string]any{"topic": "ops", "title": "Disk full", "message": "Check <!channel> src/**/backup\n-# Runbook"})
	deliver()
	p := checkHits("chat.postMessage")[0].body
	if p["channel"] != "C0123" || p["text"] != "*Disk full*\nCheck &lt;!channel&gt; src/**/backup\n-# Runbook" || p["unfurl_links"] != false {
		t.Fatalf("post: %+v", p)
	}
	var remote string
	if err := s.db.QueryRow(`SELECT remote_id FROM hook_posts WHERE name = 'slack' AND id = ?`, a["id"]).Scan(&remote); err != nil || remote != "C999/123.456" {
		t.Fatalf("kept post: %q, %v", remote, err)
	}
	// Only the channel matters: a save that changes anything else in the URL
	// keeps the message ids, so the dismiss below still edits the post (#870).
	save("C0123&unused=1")
	api("POST", "/api/v1/notifications/"+a["id"].(string)+"/dismiss", nil)
	deliver()
	got := checkHits("chat.postMessage", "chat.update", "reactions.add")
	u, r := got[1].body, got[2].body
	if u["channel"] != "C999" || u["ts"] != "123.456" || !strings.HasPrefix(fmt.Sprint(u["text"]), "~*Disk full*~") || !strings.Contains(fmt.Sprint(u["text"]), "\n-# Runbook\nCleared by ") {
		t.Fatalf("update: %+v", u)
	}
	if r["channel"] != "C999" || r["timestamp"] != "123.456" || r["name"] != "white_check_mark" {
		t.Fatalf("reaction: %+v", r)
	}
	_, b := api("POST", "/api/v1/notifications", map[string]any{"topic": "ops", "message": "second alert"})
	deliver()
	api("POST", "/api/v1/notifications/"+b["id"].(string)+"/ack", nil)
	deliver()
	got = checkHits("chat.postMessage", "chat.update", "reactions.add", "chat.postMessage", "chat.update", "reactions.add")
	if got[5].body["name"] != "eyes" {
		t.Fatalf("acknowledgment reaction: %+v", got[5])
	}
	api("POST", "/api/v1/notifications/"+b["id"].(string)+"/resolve", nil)
	deliver()
	checkHits("chat.postMessage", "chat.update", "reactions.add", "chat.postMessage", "chat.update", "reactions.add", "chat.update")
	ds, err := s.HookDeliveries(ctx, serverToken, "ops", "slack", 10)
	if err != nil || len(ds) != 5 || ds[0].Event != Resolved || ds[0].Code != 204 || ds[0].Status != "ok" || ds[0].Attempts != 1 || ds[1].Status != "ok" || ds[3].Status != "ok" {
		t.Fatalf("deleted message and already-reacted deliveries: %+v, %v", ds, err)
	}

	save("Cbad")
	api("POST", "/api/v1/notifications", map[string]any{"topic": "ops", "message": "bad auth"})
	deliver()
	ds, err = s.HookDeliveries(ctx, serverToken, "ops", "slack", 1)
	if err != nil || len(ds) != 1 || ds[0].Status != "pending" || ds[0].Code != 200 || ds[0].Attempts != 1 ||
		!strings.Contains(ds[0].Error, "invalid_auth") || strings.Contains(ds[0].Error, token) {
		t.Fatalf("invalid auth delivery: %+v, %v", ds, err)
	}
}

func TestHooksOutGitHub(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	type hit struct {
		method, path, auth string
		body               map[string]any
	}
	var mu sync.Mutex
	var hits []hit
	var opened []map[string]any // newest first, as GitHub lists them
	lose := false
	gh, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		h := hit{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		json.NewDecoder(r.Body).Decode(&h.body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/issues"): // the look before each open (#680)
			if h.auth != "Bearer ghtok" || r.URL.Query().Get("state") != "all" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(opened)
			return
		case strings.HasSuffix(r.URL.Path, "/issues"):
			hits = append(hits, h)
			body := strings.ReplaceAll(h.body["body"].(string), "\n", "\r\n") // as an edit on GitHub's website saves it (#827)
			opened = append([]map[string]any{{"number": 7 + len(opened), "body": body}}, opened...)
			if lose {
				w.WriteHeader(502) // the issue is made, the answer is lost
				return
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"id": 123456789, "number": 7}`))
		case strings.Contains(r.URL.Path, "/issues/8"):
			hits = append(hits, h)
			w.WriteHeader(410) // someone deleted the issue
		default:
			hits = append(hits, h)
			w.Write([]byte(`{}`))
		}
	})
	path := "/api/v1/topics/ops/hooks/gh"
	// A pending Discord delivery was never checked for GitHub: switching kind drops it.
	if code, _ := call(t, ts, "PUT", path, admin, map[string]any{"url": gh.URL, "kind": "discord"}); code != 200 {
		t.Fatal("discord save")
	}
	s.db.Exec(`INSERT INTO hook_deliveries (topic, name, seq, event, body, next_at, updated_at) VALUES ('ops', 'gh', 1, 'posted', '{}', 0, 0)`)
	// The token goes only to GitHub, even when a new url keeps the saved one.
	for _, bad := range []map[string]any{{"url": gh.URL + "/repos/o/r", "kind": "github", "secret": "x"}, {"url": gh.URL, "kind": "github"}, {"url": gh.URL, "kind": "github", "secret": "x", "body": `{}`}} {
		if code, _ := call(t, ts, "PUT", path, admin, bad); code != 400 {
			t.Fatalf("saved %v: %d", bad, code)
		}
		githubAPI = gh.URL + "/repos/"
	}
	t.Cleanup(func() { githubAPI = "https://api.github.com/repos/" })
	if code, h := call(t, ts, "PUT", path, admin, map[string]any{"url": gh.URL + "/repos/o/r/", "kind": "github", "secret": "ghtok"}); code != 200 || h["kind"] != "github" || h["secret"] != nil {
		t.Fatalf("save: %d %v", code, h)
	}
	var stale int
	if s.db.QueryRow(`SELECT count(*) FROM hook_deliveries`).Scan(&stale); stale != 0 {
		t.Fatalf("%d Discord deliveries left for the github hook", stale)
	}

	// An anonymous sender never opens an issue.
	tx, _ := s.db.Begin()
	queueHooks(tx, &Event{Type: EventCreated, Raw: []byte(`{}`), Notification: &Notification{Topic: "ops", State: Active, sender: anonymousID + "-x"}}, now())
	var queued int
	tx.QueryRow(`SELECT count(*) FROM hook_deliveries`).Scan(&queued)
	tx.Rollback()
	if queued != 0 {
		t.Fatal("an anonymous publish opened an issue")
	}

	_, a := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "Disk full", "message": "@octocat ```x```"})
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": "Deploy?", "actions": []map[string]any{{"label": "Yes", "reply": "yes"}}})
	call(t, ts, "POST", "/api/v1/notifications/"+a["id"].(string)+"/dismiss", admin, nil)
	for range 2 {
		s.deliverHooks(ctx)
	}
	if len(hits) != 3 { // a question opens no issue
		t.Fatalf("want post, close, comment; got %+v", hits)
	}
	p, c, m := hits[0], hits[1], hits[2]
	if p.method != "POST" || p.path != "/repos/o/r/issues" || p.auth != "Bearer ghtok" || p.body["title"] != "[clrd ops] Disk full" ||
		fmt.Sprint(p.body["labels"]) != "[clrd priority:default]" ||
		!strings.HasSuffix(p.body["body"].(string), "\n\n````text\nDisk full\n\n@octocat ```x```\n````") {
		t.Fatalf("post: %+v", p)
	}
	if c.method != "PATCH" || c.path != "/repos/o/r/issues/7" || c.body["state"] != "closed" {
		t.Fatalf("close: %+v", c)
	}
	if m.method != "POST" || m.path != "/repos/o/r/issues/7/comments" || !strings.Contains(m.body["body"].(string), "```text\nCleared by ") {
		t.Fatalf("comment: %+v", m)
	}

	// A deleted issue is done, not retried.
	_, b := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": "b"})
	s.db.Exec(`INSERT INTO hook_posts (topic, name, id, remote_id) VALUES ('ops', 'gh', ?, '8')`, b["id"])
	call(t, ts, "POST", "/api/v1/notifications/"+b["id"].(string)+"/resolve", admin, nil)
	s.deliverHooks(ctx)
	s.deliverHooks(ctx)
	ds, _ := s.HookDeliveries(ctx, serverToken, "ops", "gh", 10)
	for _, d := range ds {
		if d.Status != "ok" {
			t.Fatalf("delivery: %+v", d)
		}
	}

	// At most githubPerHour issues an hour.
	for i := range githubPerHour + 2 {
		call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": fmt.Sprint(i)})
	}
	s.db.QueryRow(`SELECT count(*) FROM hook_deliveries WHERE name = 'gh' AND event = 'posted'`).Scan(&queued)
	if queued != githubPerHour {
		t.Fatalf("%d issues queued in an hour", queued)
	}
	// A stall older than an hour still holds its place: once it clears, no
	// more than githubPerHour open in the next hour (#680).
	s.db.Exec(`UPDATE hook_deliveries SET updated_at = 0`)
	for i := range 3 {
		call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": fmt.Sprint("late ", i)})
	}
	s.db.QueryRow(`SELECT count(*) FROM hook_deliveries WHERE name = 'gh' AND event = 'posted' AND status = 'pending'`).Scan(&queued)
	if queued != githubPerHour {
		t.Fatalf("%d issues pending after a stall", queued)
	}

	// A lost reply is not a second issue: the retry finds the first by its mark (#680).
	s.db.Exec(`DELETE FROM hook_deliveries WHERE status = 'pending'`)
	mu.Lock()
	hits, lose = nil, true
	mu.Unlock()
	_, c2 := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": "lost"})
	s.deliverHooks(ctx)
	mu.Lock()
	lose = false
	mu.Unlock()
	s.db.Exec(`UPDATE hook_deliveries SET next_at = 0`)
	s.deliverHooks(ctx)
	var remote string
	s.db.QueryRow(`SELECT remote_id FROM hook_posts WHERE id = ?`, c2["id"]).Scan(&remote)
	if len(hits) != 1 || remote != fmt.Sprint(6+len(opened)) || !strings.HasPrefix(hits[0].body["body"].(string), githubMark("gh", &Notification{Topic: "ops", ID: c2["id"].(string)})) {
		t.Fatalf("want one issue, kept as %d; got %q and %+v", 6+len(opened), remote, hits)
	}
}

// GitHub issue hook follow-ups (#680): priority labels, [clrd topic] title
// prefix, notification link when CLRD_BASE_URL is set, and github_hooks_paused.
func TestHooksOutGitHubFollowups(t *testing.T) {
	s, err := New(Options{DB: filepath.Join(t.TempDir(), "gh.db"), BaseURL: "https://user:pass@clrd.example.com/clrd?ignored=1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	admin := mkToken(t, s, "admin", []string{ScopeAdmin}, nil)
	reader := mkToken(t, s, "reader", []string{ScopeRead}, nil)
	scoped := mkToken(t, s, "scoped", []string{ScopeAdmin}, []string{"ops"})
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }

	var mu sync.Mutex
	var bodies []map[string]any
	var dcHits int
	gh, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/issues") {
			w.Write([]byte(`[]`))
			return
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		bodies = append(bodies, b)
		num := len(bodies)
		mu.Unlock()
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"number":%d}`, num)
	})
	dc, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dcHits++
		mu.Unlock()
		w.Write([]byte(`{"id":"d1"}`))
	})
	githubAPI = gh.URL + "/repos/"
	t.Cleanup(func() { githubAPI = "https://api.github.com/repos/" })

	if code, _ := call(t, ts, "PUT", "/api/v1/topics/ops/hooks/gh", admin, map[string]any{"url": gh.URL + "/repos/o/r", "kind": "github", "secret": "ghtok"}); code != 200 {
		t.Fatalf("save gh: %d", code)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/ops/hooks/dc", admin, map[string]any{"url": dc.URL + "/api/webhooks/1/tok", "kind": "discord"}); code != 200 {
		t.Fatalf("save dc: %d", code)
	}

	// 1. Priority labels, [clrd topic] title prefix (with and without title,
	//    stripping leading newlines/CR), and credential-free notification link.
	_, n5 := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "DB down", "message": "unreachable", "priority": 5})
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": "\nquiet note\r\nline 2", "priority": 1})
	s.deliverHooks(ctx)
	s.deliverHooks(ctx)
	if len(bodies) != 2 {
		t.Fatalf("want 2 opened issues, got %d", len(bodies))
	}
	wantLink := "Notification: https://clrd.example.com/clrd/app/n/" + n5["id"].(string) + "\n" // its own page, any state (#814)
	if bodies[0]["title"] != "[clrd ops] DB down" || fmt.Sprint(bodies[0]["labels"]) != "[clrd priority:urgent]" || !strings.Contains(bodies[0]["body"].(string), wantLink) || strings.Contains(bodies[0]["body"].(string), "user") || strings.Contains(bodies[0]["body"].(string), "pass") {
		t.Fatalf("priority 5 issue: %+v", bodies[0])
	}
	if bodies[1]["title"] != "[clrd ops] quiet note" || fmt.Sprint(bodies[1]["labels"]) != "[clrd priority:min]" {
		t.Fatalf("priority 1 issue: %+v", bodies[1])
	}

	// 2. Without BaseURL (default), no localhost link is emitted in the issue body.
	reqs := githubReqs(url.URL{Scheme: "https", Host: "api.github.com", Path: "/repos/o/r"},
		&Notification{ID: "01J", Topic: "ops", Title: "T", Message: "M", Priority: 4, CreatedAt: time.Now()}, "posted", "", "<!-- m -->\n", "")
	bodyMap := reqs[0].body.(map[string]any)
	if strings.Contains(bodyMap["body"].(string), "Notification:") || fmt.Sprint(bodyMap["labels"]) != "[clrd priority:high]" {
		t.Fatalf("no-baseURL req: %+v", bodyMap)
	}

	// 3. Server-wide pause switch: full admin only, stops new issues and pauses pending ones,
	//    while leaving Discord hooks running.
	if _, st := call(t, ts, "GET", "/api/v1/settings", admin, nil); st["github_hooks_paused"] != false {
		t.Fatalf("default github_hooks_paused: %v", st["github_hooks_paused"])
	}
	for _, tok := range []string{reader, scoped} {
		if code, _ := call(t, ts, "PUT", "/api/v1/settings", tok, map[string]any{"github_hooks_paused": true}); code != 403 {
			t.Fatalf("non-full-admin pause: %d, want 403", code)
		}
	}
	// Queue a GitHub issue before pausing, then pause before delivering.
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "Before pause", "message": "pending"})
	if code, st := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"github_hooks_paused": true}); code != 200 || st["github_hooks_paused"] != true {
		t.Fatalf("pause: %d %v", code, st)
	}
	// Publish and dismiss another while paused: must not queue post or state-change
	// deliveries for GitHub, must still deliver to Discord.
	dcBefore := dcHits
	_, pausedNotif := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "While paused", "message": "skipped"})
	call(t, ts, "POST", "/api/v1/notifications/"+pausedNotif["id"].(string)+"/dismiss", admin, nil)
	s.deliverHooks(ctx)
	s.deliverHooks(ctx)
	if len(bodies) != 2 {
		t.Fatalf("github hook delivered while paused: %d", len(bodies))
	}
	if dcHits <= dcBefore {
		t.Fatalf("discord hook did not deliver while github hooks paused: before=%d after=%d", dcBefore, dcHits)
	}
	var ghPending int
	s.db.QueryRow(`SELECT count(*) FROM hook_deliveries WHERE name = 'gh' AND status = 'pending'`).Scan(&ghPending)
	if ghPending != 1 {
		t.Fatalf("expected only 1 pending pre-pause github delivery (no state-change pileup), got %d", ghPending)
	}
	// Unpause: only the pre-pause pending issue delivers; the while-paused alert was not queued.
	if code, st := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"github_hooks_paused": false}); code != 200 || st["github_hooks_paused"] != false {
		t.Fatalf("unpause: %d %v", code, st)
	}
	s.deliverHooks(ctx)
	s.deliverHooks(ctx)
	if len(bodies) != 3 || bodies[2]["title"] != "[clrd ops] Before pause" {
		t.Fatalf("after unpause: %+v", bodies)
	}

	// 4. Time spent paused does not count toward the 24h auto-disable window (#680).
	// Seed a failing github hook with 1h of unpaused failure, pause it for 30h,
	// unpause, and fail one retry: it must stay enabled (only 1h unpaused, < 24h).
	deadHook := "/api/v1/topics/ops/hooks/ghdead"
	if code, _ := call(t, ts, "PUT", deadHook, admin, map[string]any{"url": githubAPI + "o/dead", "kind": "github", "secret": "ghtok"}); code != 200 {
		t.Fatalf("save ghdead: %d", code)
	}
	// Point ghdead at a failing URL via status 500 on /repos/o/dead by using a closed server URL prefix.
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	t.Cleanup(badSrv.Close)
	githubAPI = badSrv.URL + "/repos/"
	if code, _ := call(t, ts, "PUT", deadHook, admin, map[string]any{"url": badSrv.URL + "/repos/o/dead", "kind": "github", "secret": "ghtok"}); code != 200 {
		t.Fatalf("save ghdead 500: %d", code)
	}
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "Flake", "message": "err"})
	s.deliverHooks(ctx)
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"github_hooks_paused": true}); code != 200 {
		t.Fatalf("pause for window test: %d", code)
	}
	// Backdate both failing_since (31h ago) and paused_at (30h ago): 1h failing before a 30h pause.
	tNow := time.Now()
	if _, err := s.db.Exec(`UPDATE hooks_out SET failing_since = ? WHERE topic = 'ops' AND name = 'ghdead'`, ms(tNow.Add(-31*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE settings SET value = ? WHERE key = 'github_hooks_paused_at'`, fmt.Sprint(ms(tNow.Add(-30*time.Hour)))); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"github_hooks_paused": false}); code != 200 {
		t.Fatalf("unpause after 30h: %d", code)
	}
	if _, err := s.db.Exec(`UPDATE hook_deliveries SET next_at = 0 WHERE name = 'ghdead' AND status = 'pending'`); err != nil {
		t.Fatal(err)
	}
	s.deliverHooks(ctx)
	hs, err := s.OutHooks(ctx, serverToken, "ops")
	if err != nil {
		t.Fatal(err)
	}
	foundDead := false
	for _, h := range hs {
		if h.Name == "ghdead" {
			foundDead = true
			if !h.Enabled {
				t.Fatalf("ghdead was disabled after 1h unpaused + 30h paused failure: %+v", h)
			}
		}
	}
	if !foundDead {
		t.Fatalf("ghdead missing from OutHooks: %+v", hs)
	}
}

// A pause that lands after a batch is picked still stops its send, and the
// prune keeps paused github deliveries, during the pause and after (#796).
func TestHooksOutGitHubPauseHolds(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	var pauseOnFind, findFails, findWait atomic.Bool
	var posts, closes atomic.Int64
	gh, _ := counter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if pauseOnFind.Load() { // the pause lands mid-send, after the batch read
				s.db.Exec(`UPDATE settings SET value = json_set(value, '$.github_hooks_paused', json('true')) WHERE key = 'display'`)
			}
			if findWait.Load() {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(429)
				return
			}
			if findFails.Load() {
				w.WriteHeader(502)
				return
			}
			w.Write([]byte(`[]`))
			return
		}
		if r.Method == "PATCH" {
			closes.Add(1)
		}
		if !strings.HasSuffix(r.URL.Path, "/issues") {
			w.Write([]byte(`{}`))
			return
		}
		n := posts.Add(1)
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"number":%d}`, n)
	})
	githubAPI = gh.URL + "/repos/"
	t.Cleanup(func() { githubAPI = "https://api.github.com/repos/" })
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/ops/hooks/gh", admin, map[string]any{"url": gh.URL + "/repos/o/r", "kind": "github", "secret": "ghtok"}); code != 200 {
		t.Fatalf("save gh: %d", code)
	}
	setPaused := func(p bool) {
		t.Helper()
		if code, st := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"github_hooks_paused": p}); code != 200 || st["github_hooks_paused"] != p {
			t.Fatalf("pause %v: %d %v", p, code, st)
		}
	}
	pending := func() (n, attempts int) {
		s.db.QueryRow(`SELECT count(*), coalesce(sum(attempts), 0) FROM hook_deliveries WHERE name = 'gh' AND status = 'pending'`).Scan(&n, &attempts)
		return
	}
	setPaused(false) // the settings row the mid-send pause edits

	// 1. Paused between the batch read and the POST: nothing opens, and the
	//    delivery waits untouched for the resume.
	_, na := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "A", "message": "a"})
	pauseOnFind.Store(true)
	if err := s.deliverHooks(ctx); err != nil {
		t.Fatal(err)
	}
	pauseOnFind.Store(false)
	if p, _ := githubHooksPaused(ctx, s.db); !p {
		t.Fatal("mid-send pause did not land")
	}
	if n, a := pending(); posts.Load() != 0 || n != 1 || a != 0 {
		t.Fatalf("paused mid-batch: posts=%d pending=%d attempts=%d", posts.Load(), n, a)
	}
	// The same when the lookup itself fails after the pause lands.
	setPaused(false)
	pauseOnFind.Store(true)
	findFails.Store(true)
	s.deliverHooks(ctx)
	pauseOnFind.Store(false)
	findFails.Store(false)
	if n, a := pending(); posts.Load() != 0 || n != 1 || a != 0 {
		t.Fatalf("paused mid-batch, lookup failed: posts=%d pending=%d attempts=%d", posts.Load(), n, a)
	}
	// The same with a Retry-After: the try still does not count, but the
	// delivery waits as GitHub asked, past the resume (#881).
	setPaused(false)
	pauseOnFind.Store(true)
	findWait.Store(true)
	s.deliverHooks(ctx)
	pauseOnFind.Store(false)
	findWait.Store(false)
	var next int64
	s.db.QueryRow(`SELECT next_at FROM hook_deliveries WHERE name = 'gh' AND status = 'pending'`).Scan(&next)
	if n, a := pending(); n != 1 || a != 0 || next < ms(time.Now().Add(100*time.Second)) {
		t.Fatalf("paused mid-batch, Retry-After: pending=%d attempts=%d, next_at %d ms from now", n, a, next-ms(time.Now()))
	}
	s.db.Exec(`UPDATE hook_deliveries SET next_at = 0 WHERE name = 'gh'`)
	setPaused(false)
	s.deliverHooks(ctx)
	if n, _ := pending(); posts.Load() != 1 || n != 0 {
		t.Fatalf("after resume: posts=%d pending=%d", posts.Load(), n)
	}

	// 2. Deliveries paused past retention are kept, and still kept by a prune
	//    after the resume; an old finished one goes as before. A's queued
	//    close keeps A's issue number though A itself is pruned.
	call(t, ts, "POST", "/api/v1/notifications/"+na["id"].(string)+"/dismiss", admin, nil)
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "title": "B", "message": "b"})
	setPaused(true)
	s.opt.Retention = 7 * 24 * time.Hour
	old := ms(time.Now().Add(-60 * 24 * time.Hour))
	if _, err := s.db.Exec(`UPDATE hook_deliveries SET updated_at = ? WHERE name = 'gh'`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET updated_at = ? WHERE id = ?`, old, na["id"]); err != nil {
		t.Fatal(err)
	}
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	var all, notes, mapped int
	s.db.QueryRow(`SELECT count(*) FROM hook_deliveries WHERE name = 'gh'`).Scan(&all)
	s.db.QueryRow(`SELECT count(*) FROM notifications WHERE id = ?`, na["id"]).Scan(&notes)
	s.db.QueryRow(`SELECT count(*) FROM hook_posts WHERE id = ?`, na["id"]).Scan(&mapped)
	if n, _ := pending(); n != 2 || all != 2 || notes != 0 || mapped != 1 {
		t.Fatalf("prune while paused: pending=%d all=%d, want the 2 pending only; A kept=%d, A's issue kept=%d", n, all, notes, mapped)
	}
	setPaused(false)
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := pending(); n != 2 {
		t.Fatalf("prune after resume dropped the backlog: pending=%d", n)
	}
	s.deliverHooks(ctx) // one github delivery a tick per hook
	s.deliverHooks(ctx)
	if posts.Load() != 2 || closes.Load() != 1 {
		t.Fatalf("backlog after resume: posts=%d closes=%d", posts.Load(), closes.Load())
	}
}

func TestHookOffLegacyNotice(t *testing.T) {
	s, ts, _ := newTest(t)
	ctx := context.Background()
	pub := mkToken(t, s, "publisher", []string{ScopePublish}, nil)
	const legacyKey = "webhook-off-gh"
	code, _ := call(t, ts, "POST", "/api/v1/notifications", pub, map[string]any{
		"topic": "alerts", "external_id": legacyKey, "title": hookOffTitle, "message": "Publisher alert",
	})
	if code != 200 {
		t.Fatalf("ordinary legacy key: %d", code)
	}
	if err := s.clearHookOff(ctx, "alerts", "gh"); err != nil {
		t.Fatal(err)
	}
	if n := one(t, s, "alerts", legacyKey); n.State != Active || n.Message != "Publisher alert" {
		t.Fatalf("hook touched publisher alert: %+v", n)
	}

	if _, err := s.Publish(ctx, serverToken, PublishInput{Topic: "alerts", ExternalID: "webhook-off-old",
		Title: hookOffTitle, Message: "Existing server notice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.clearHookOff(ctx, "alerts", "old"); err != nil {
		t.Fatal(err)
	}
	if n := one(t, s, "alerts", "webhook-off-old"); n.State != Resolved {
		t.Fatalf("legacy notice state: %s", n.State)
	}
}

func hookNotice(t *testing.T, s *Server, topic, name string) *Notification {
	t.Helper()
	n, err := getNotification(context.Background(), s.db, "topic = ? AND hook_notice = ?", topic, name)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
