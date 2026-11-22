package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// formatOf returns the format of the newest notification in topic.
func formatOf(t *testing.T, ts *httptest.Server, tok, topic string) string {
	t.Helper()
	_, l := call(t, ts, "GET", "/api/v1/notifications?state=all&topic="+topic, tok, nil)
	ns, _ := l["notifications"].([]any)
	if len(ns) == 0 {
		t.Fatalf("nothing in %s: %v", topic, l)
	}
	return ns[0].(map[string]any)["format"].(string)
}

// Every publish path stores format; Markdown unless the publisher says text.
func TestFormatFromEveryPublishPath(t *testing.T) {
	_, ts, admin := newTest(t)
	post := func(body map[string]any) (int, map[string]any) {
		return call(t, ts, "POST", "/api/v1/notifications", admin, body)
	}
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"topic": "n1", "message": "*hi*"}, FormatMarkdown},
		{map[string]any{"topic": "n2", "message": "hi", "format": "text"}, FormatText},
		{map[string]any{"topic": "n3", "message": "hi", "markdown": false}, FormatText},
		{map[string]any{"topic": "n4", "message": "hi", "markdown": true}, FormatMarkdown},
		{map[string]any{"topic": "n5", "message": "hi", "format": "markdown", "markdown": false}, FormatMarkdown}, // format wins
	} {
		if code, n := post(c.body); code != 200 || n["format"] != c.want {
			t.Fatalf("native %v: %d %v", c.body, code, n)
		}
	}
	if code, _ := post(map[string]any{"topic": "n6", "message": "hi", "format": "html"}); code != 400 {
		t.Fatalf("bad format: want 400, got %d", code)
	}

	// Native multipart.
	if code, n := upload(t, ts, admin, map[string]any{"topic": "m1", "message": "f", "format": "text"}, "a.png", pngBytes, false); code != 200 || n["format"] != FormatText {
		t.Fatalf("multipart: %d %v", code, n)
	}

	// ntfy: headers on PUT and POST, query on GET /publish, a file upload, POST / JSON, a template.
	for _, c := range []struct {
		method, path, body string
		hdr                map[string]string
		topic, want        string
	}{
		{"PUT", "/t1", "hi", nil, "t1", FormatMarkdown},
		{"PUT", "/t2", "hi", map[string]string{"X-Markdown": "no"}, "t2", FormatText},
		{"POST", "/t3", "hi", map[string]string{"Markdown": "yes"}, "t3", FormatMarkdown},
		{"POST", "/t4", "hi", map[string]string{"X-Md": "0"}, "t4", FormatText},
		{"GET", "/t5/publish?message=hi&markdown=false", "", nil, "t5", FormatText},
		{"GET", "/t6/publish?message=hi", "", nil, "t6", FormatMarkdown},
		{"PUT", "/t7", "file body", map[string]string{"X-Filename": "a.txt", "X-Markdown": "false"}, "t7", FormatText},
		{"POST", "/", `{"topic":"t8","message":"hi","markdown":false}`, nil, "t8", FormatText},
		{"POST", "/", `{"topic":"t9","message":"hi"}`, nil, "t9", FormatMarkdown},
		{"POST", "/t10?template=alertmanager", `{"alerts":[{"status":"firing","fingerprint":"f","labels":{"alertname":"A"},"annotations":{"summary":"s"}}]}`, nil, "t10", FormatMarkdown},
		{"POST", "/t11?template=alertmanager", `{"alerts":[{"status":"firing","fingerprint":"f","labels":{"alertname":"A"},"annotations":{"summary":"s"}}]}`, map[string]string{"X-Markdown": "no"}, "t11", FormatText},
		{"POST", "/t12?template=alertmanager&md=0", `{"alerts":[{"status":"firing","fingerprint":"f","labels":{"alertname":"A"},"annotations":{"summary":"s"}}]}`, nil, "t12", FormatText},
	} {
		if code, out := raw(t, c.method, ts.URL+c.path, admin, c.body, c.hdr); code != 200 {
			t.Fatalf("%s %s: %d %s", c.method, c.path, code, out)
		}
		if got := formatOf(t, ts, admin, c.topic); got != c.want {
			t.Errorf("%s %s %v: format %s, want %s", c.method, c.path, c.hdr, got, c.want)
		}
	}

	// A re-publish under the same external_id takes the new format.
	post(map[string]any{"topic": "u", "external_id": "x", "message": "a"})
	if _, n := post(map[string]any{"topic": "u", "external_id": "x", "message": "a", "format": "text"}); n["format"] != FormatText {
		t.Fatalf("upsert kept the old format: %v", n)
	}

	// ntfy output: content_type for Markdown, nothing for text.
	_, out := raw(t, "GET", ts.URL+"/t1,t2/json?poll=1", admin, "", nil)
	got := map[string]string{}
	for _, l := range lines(out) {
		m := ntfyDecode(t, l)
		got[m.Topic] = m.ContentType
	}
	if got["t1"] != "text/markdown" || got["t2"] != "" {
		t.Fatalf("ntfy content_type: %v", got)
	}
}

func TestBlockedIP(t *testing.T) {
	for _, c := range []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true}, {"127.8.9.10", true}, {"::1", true}, {"::ffff:127.0.0.1", true},
		{"10.0.0.1", true}, {"172.16.5.4", true}, {"192.168.1.1", true}, {"::ffff:192.168.1.1", true},
		{"169.254.169.254", true}, {"fe80::1", true}, {"fd00::1", true}, {"fc00::1", true},
		{"0.0.0.0", true}, {"0.1.2.3", true}, {"::", true}, {"100.64.0.1", true}, {"192.0.0.8", true},
		{"198.18.0.1", true}, {"224.0.0.1", true}, {"ff02::1", true}, {"240.0.0.1", true},
		{"255.255.255.255", true}, {"64:ff9b::7f00:1", true}, {"2002:7f00:1::", true}, {"2001::1", true},
		{"8.8.8.8", false}, {"1.1.1.1", false}, {"2606:4700:4700::1111", false}, {"93.184.216.34", false},
	} {
		if got := blockedIP(netip.MustParseAddr(c.ip)); got != c.blocked {
			t.Errorf("blockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

// counter is an origin that counts its hits.
func counter(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	var n atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1); h(w, r) }))
	t.Cleanup(ts.Close)
	return ts, &n
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func TestFetchGuard(t *testing.T) {
	ctx := context.Background()
	target, hits := counter(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) })

	// The default guard: a loopback address is refused before any byte is sent.
	f := newFetcher(t.TempDir())
	if _, _, err := f.get(ctx, mustURL(target.URL), fetchMax); err == nil {
		t.Fatal("default guard reached loopback")
	}
	// Even with any port allowed, the address check alone refuses it.
	f.allow = func(ip netip.Addr, _ string) bool { return !blockedIP(ip) }
	if _, _, err := f.get(ctx, mustURL(target.URL), fetchMax); err == nil {
		t.Fatal("address check reached loopback")
	}
	for _, u := range []string{"file:///etc/passwd", "gopher://x/", "ftp://example.com/", "http://user:pw@example.com/", "/relative"} {
		if _, _, err := f.get(ctx, mustURL(u), fetchMax); err == nil {
			t.Errorf("%s was fetched", u)
		}
	}

	// A redirect is checked again: an allowed hop cannot bounce to a blocked one.
	hop, _ := counter(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) })
	hopPort := mustURL(hop.URL).Port()
	f.allow = func(_ netip.Addr, port string) bool { return port == hopPort }
	if _, _, err := f.get(ctx, mustURL(hop.URL), fetchMax); err == nil {
		t.Fatal("redirect reached the blocked target")
	}
	if hits.Load() != 0 {
		t.Fatalf("blocked target was hit %d times", hits.Load())
	}

	// Size cap.
	big, _ := counter(t, func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, 2000)) })
	f.allow = func(netip.Addr, string) bool { return true }
	if _, _, err := f.get(ctx, mustURL(big.URL), 1000); err == nil {
		t.Fatal("size cap not applied")
	}
}

func TestSettingsSync(t *testing.T) {
	s, ts, admin := newTest(t)
	_, st := call(t, ts, "GET", "/api/v1/settings", admin, nil)
	if st["render_formatting"] != true || st["autolink"] != true || st["favicons"] != true || st["link_previews"] != false {
		t.Fatalf("defaults: %v", st)
	}

	// A stream gets settings before ready, then again on every change.
	ws := dialWS(t, ts.URL, admin, 0)
	if m := ws.next(); m.Type != "settings" {
		t.Fatalf("want settings first, got %+v", m)
	}
	ws.until(func(m msg) bool { return m.Type == "ready" })
	if code, st := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"link_previews": true}); code != 200 || st["link_previews"] != true || st["autolink"] != true {
		t.Fatalf("put: %d %v", code, st)
	}
	ws.until(func(m msg) bool { return m.Type == "settings" })
	got, _ := s.Settings(context.Background(), &Token{Scopes: []string{ScopeAdmin}})
	if !got.LinkPreviews {
		t.Fatal("change not stored")
	}

	// A topic-limited token: only its own overrides, never the global switches.
	lim := mkToken(t, s, "phone", []string{ScopeRead}, []string{"mine"})
	call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"topic_previews": map[string]any{"other": false}})
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", lim, map[string]any{"autolink": false}); code != 403 {
		t.Fatalf("limited token changed a global switch: %d", code)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", lim, map[string]any{"topic_previews": map[string]any{"other": true}}); code != 403 {
		t.Fatalf("limited token changed another topic: %d", code)
	}
	code, st := call(t, ts, "PUT", "/api/v1/settings", lim, map[string]any{"topic_previews": map[string]any{"mine": false}})
	if tp := st["topic_previews"].(map[string]any); code != 200 || tp["mine"] != false || len(tp) != 1 {
		t.Fatalf("limited token view: %d %v", code, st)
	}
	_, st = call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"topic_previews": map[string]any{"mine": nil}})
	if tp := st["topic_previews"].(map[string]any); len(tp) != 1 || tp["other"] != false {
		t.Fatalf("null should drop the override: %v", st)
	}
}

func TestPreviewAndFavicon(t *testing.T) {
	s, ts, admin := newTest(t)
	page := `<html><head><title>Fallback</title>
<meta property="og:title" content="Disk &amp; CPU">
<meta content='Load is high' name="description">
<meta property="og:image" content="/img.png"></head></html>`
	origin, hits := counter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/img.png" {
			w.Write(pngBytes)
			return
		}
		w.Write([]byte(page))
	})

	// Off by default: no fetch at all.
	if code, _ := call(t, ts, "GET", "/api/v1/preview?url="+url.QueryEscape(origin.URL), admin, nil); code != 403 {
		t.Fatalf("preview while off: %d", code)
	}
	// On for one topic only.
	call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"topic_previews": map[string]any{"news": true}})
	// The real guard refuses the loopback origin and returns only the URL.
	code, p := call(t, ts, "GET", "/api/v1/preview?topic=news&url="+url.QueryEscape(origin.URL), admin, nil)
	if code != 200 || p["title"] != nil || hits.Load() != 0 {
		t.Fatalf("blocked preview: %d %v, hits %d", code, p, hits.Load())
	}

	// Allowed (test hook): Open Graph fields, image inlined as data:.
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	u := origin.URL + "/post"
	code, p = call(t, ts, "GET", "/api/v1/preview?topic=news&url="+url.QueryEscape(u), admin, nil)
	if code != 200 || p["title"] != "Disk & CPU" || p["description"] != "Load is high" ||
		!strings.HasPrefix(p["image"].(string), "data:image/png;base64,") {
		t.Fatalf("preview: %d %v", code, p)
	}
	n := hits.Load()
	call(t, ts, "GET", "/api/v1/preview?topic=news&url="+url.QueryEscape(u), admin, nil)
	if hits.Load() != n {
		t.Fatal("second preview was not cached")
	}
	if code, _ := call(t, ts, "GET", "/api/v1/preview?topic=news&url=file:///etc/passwd", admin, nil); code != 400 {
		t.Fatalf("file url: %d", code)
	}

	// Favicon: bad host is 400; a host that cannot be reached gets the fallback.
	if code, _ := raw(t, "GET", ts.URL+"/api/v1/favicon?host=a/b", admin, "", nil); code != 400 {
		t.Fatalf("bad host: %d", code)
	}
	s.fetch = newFetcher(t.TempDir())
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/favicon?host=127.0.0.1", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/svg+xml" || res.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("favicon fallback: %d %v", res.StatusCode, res.Header)
	}
}

func TestIconLink(t *testing.T) {
	ts := tags([]byte(`<link rel="stylesheet" href="/a.css"><LINK REL="Shortcut Icon" HREF="/i.png?a=1&amp;b=2"><meta content="CPU > 90%" property="og:title">`))
	if len(ts) != 3 || ts[1]["rel"] != "Shortcut Icon" || ts[1]["href"] != "/i.png?a=1&b=2" || ts[2]["content"] != "CPU > 90%" {
		t.Fatalf("tags: %v", ts)
	}
}

func TestSnoozeSettings(t *testing.T) {
	s, ts, admin := newTest(t)
	// A row saved before the snooze fields existed still gets their defaults.
	if _, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES ('display', '{"render_formatting":true,"link_previews":true}')`); err != nil {
		t.Fatal(err)
	}
	_, st := call(t, ts, "GET", "/api/v1/settings", admin, nil)
	if fmt.Sprint(st["snooze_intervals"]) != "[1h 4h]" || st["snooze_default"] != "1h" || st["link_previews"] != true {
		t.Fatalf("defaults: %v", st)
	}

	code, st := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"snooze_intervals": []string{"15m", "2h", "48h"}, "snooze_default": "2h"})
	if code != 200 || fmt.Sprint(st["snooze_intervals"]) != "[15m 2h 48h]" || st["snooze_default"] != "2h" {
		t.Fatalf("put: %d %v", code, st)
	}
	// A list without the old default falls back to its first entry.
	if _, st = call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"snooze_intervals": []string{"30m", "3h"}}); st["snooze_default"] != "30m" {
		t.Fatalf("default not moved: %v", st)
	}

	for _, bad := range []map[string]any{
		{"snooze_intervals": []string{}},
		{"snooze_intervals": []string{"1h", "2h", "3h", "4h", "5h", "6h", "7h", "8h", "9h"}},
		{"snooze_intervals": []string{"tomorrow"}},
		{"snooze_intervals": []string{"0s"}},
		{"snooze_intervals": []string{"30s"}},
		{"snooze_intervals": []string{"745h"}},
		{"snooze_intervals": []string{"60m", "1h"}},
		{"snooze_default": "5h"},
	} {
		if code, _ := call(t, ts, "PUT", "/api/v1/settings", admin, bad); code != 400 {
			t.Errorf("%v: got %d, want 400", bad, code)
		}
	}
	lim := mkToken(t, s, "phone", []string{ScopeRead}, []string{"mine"})
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", lim, map[string]any{"snooze_default": "3h"}); code != 403 {
		t.Fatalf("limited token changed the snooze default: %d", code)
	}
	threeH := "3h"
	granted := &Token{Name: "ntfy-user", Scopes: []string{ScopeRead}, Grants: []Grant{{Topic: "mine", Scopes: []string{ScopeRead}}}}
	if _, err := s.UpdateSettings(context.Background(), granted, SettingsPatch{SnoozeDefault: &threeH}); err == nil {
		t.Fatal("a token limited by grants changed the snooze default")
	}
	if _, st = call(t, ts, "GET", "/api/v1/settings", admin, nil); fmt.Sprint(st["snooze_intervals"]) != "[30m 3h]" || st["snooze_default"] != "30m" {
		t.Fatalf("a rejected change was stored: %v", st)
	}
}

func TestSwipeSettings(t *testing.T) {
	s, ts, admin := newTest(t)
	_, st := call(t, ts, "GET", "/api/v1/settings", admin, nil)
	if fmt.Sprint(st["swipe_start"]) != "[clear snooze]" || fmt.Sprint(st["swipe_end"]) != "[ack]" {
		t.Fatalf("defaults on a fresh server: %v %v", st["swipe_start"], st["swipe_end"])
	}
	// Moving ack to the start side is fine once the end side gives it up.
	code, st := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"swipe_start": []string{"ack", "resolve", "clear"}, "swipe_end": []string{}})
	if code != 200 || fmt.Sprint(st["swipe_start"]) != "[ack resolve clear]" || fmt.Sprint(st["swipe_end"]) != "[]" {
		t.Fatalf("put: %d %v %v", code, st["swipe_start"], st["swipe_end"])
	}
	for _, c := range []struct {
		key  string // the key the error must name
		body map[string]any
	}{
		{"swipe_end", map[string]any{"swipe_end": []string{"clear"}}},                                     // repeat against the stored start side
		{"swipe_end", map[string]any{"swipe_start": []string{"snooze"}, "swipe_end": []string{"snooze"}}}, // repeat across the two
		{"swipe_start", map[string]any{"swipe_start": []string{"ack", "ack"}}},                            // repeat within one
		{"swipe_start", map[string]any{"swipe_start": []string{"archive"}}},                               // unknown action
		{"swipe_start", map[string]any{"swipe_start": []string{"clear", "snooze", "ack", "resolve"}, "swipe_end": []string{}}},
		{"swipe_start", map[string]any{"swipe_start": nil}},
		{"swipe_start", map[string]any{"swipe_start": "clear"}},
		{"swipe_end", map[string]any{"swipe_end": []any{1}}},
		{"snooze_default", map[string]any{"swipe_start": []string{"snooze"}, "snooze_default": "5h"}}, // valid swipe, bad snooze
	} {
		code, body := call(t, ts, "PUT", "/api/v1/settings", admin, c.body)
		if msg, _ := body["error"].(string); code != 400 || !strings.Contains(msg, c.key) {
			t.Errorf("%v: got %d %q, want 400 naming %s", c.body, code, msg, c.key)
		}
	}
	lim := mkToken(t, s, "phone", []string{ScopeRead}, []string{"mine"})
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", lim, map[string]any{"swipe_end": []string{"snooze"}}); code != 403 {
		t.Fatalf("limited token changed a swipe side: %d", code)
	}
	if _, st = call(t, ts, "GET", "/api/v1/settings", admin, nil); fmt.Sprint(st["swipe_start"]) != "[ack resolve clear]" || fmt.Sprint(st["swipe_end"]) != "[]" || st["snooze_default"] != "1h" {
		t.Fatalf("a rejected change was stored: %v", st)
	}
	// A row saved before the swipe keys existed still gets their defaults.
	if _, err := s.db.Exec(`UPDATE settings SET value = '{"render_formatting":true}' WHERE key = 'display'`); err != nil {
		t.Fatal(err)
	}
	if _, st = call(t, ts, "GET", "/api/v1/settings", admin, nil); fmt.Sprint(st["swipe_start"]) != "[clear snooze]" || fmt.Sprint(st["swipe_end"]) != "[ack]" {
		t.Fatalf("defaults on an old row: %v %v", st["swipe_start"], st["swipe_end"])
	}
}
