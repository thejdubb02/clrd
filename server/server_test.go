package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// newTest starts a server on a temp database and returns it with an admin token.
func newTest(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	s, err := New(Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(func() { ts.Close(); s.Close() })
	return s, ts, mkToken(t, s, "admin", []string{ScopeAdmin}, nil)
}

func mkToken(t *testing.T, s *Server, name string, scopes, topics []string) string {
	t.Helper()
	_, raw, err := s.CreateToken(context.Background(), name, scopes, topics)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// call does one request and decodes a JSON reply into a map.
func call(t *testing.T, ts *httptest.Server, method, path, tok string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestPublishUpsertAndStates(t *testing.T) {
	_, ts, admin := newTest(t)
	code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "external_id": "disk", "title": "Disk 91%"})
	if code != 200 || n["state"] != "active" || n["seq"] != 1.0 {
		t.Fatalf("publish: %d %v", code, n)
	}
	for _, f := range []string{"tags", "actions"} {
		if _, ok := n[f].([]any); !ok {
			t.Errorf("%s should be [], got %v", f, n[f])
		}
	}
	id := n["id"].(string)

	// Same external_id while open updates the same row.
	_, n2 := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "external_id": "disk", "title": "Disk 95%"})
	if n2["id"] != id || n2["title"] != "Disk 95%" || n2["seq"] != 2.0 {
		t.Fatalf("upsert: %v", n2)
	}

	step := func(action string, body any, wantState string, wantSeq float64) {
		t.Helper()
		code, n := call(t, ts, "POST", "/api/v1/notifications/"+id+"/"+action, admin, body)
		if code != 200 || n["state"] != wantState || n["seq"] != wantSeq {
			t.Fatalf("%s: %d %v", action, code, n)
		}
	}
	step("ack", nil, "acknowledged", 3)
	step("ack", nil, "acknowledged", 3) // same state: no new event
	step("snooze", map[string]string{"for": "1h"}, "snoozed", 4)
	step("dismiss", nil, "dismissed", 5)
	step("dismiss", nil, "dismissed", 5) // two phones at once
	step("resolve", nil, "dismissed", 5) // already final
	if code, _ := call(t, ts, "POST", "/api/v1/notifications/"+id+"/ack", admin, nil); code != 409 {
		t.Fatalf("ack after dismiss: want 409, got %d", code)
	}
	step("reopen", nil, "active", 6)

	// Resolve by key, then a new publish with the same key is a new row.
	if code, n := call(t, ts, "POST", "/infra/disk/resolve", admin, nil); code != 200 || n["state"] != "resolved" {
		t.Fatalf("resolve by key: %d %v", code, n)
	}
	if code, _ := call(t, ts, "POST", "/infra/disk/resolve", admin, nil); code != 404 {
		t.Fatalf("resolve by key with nothing open: want 404, got %d", code)
	}
	_, n3 := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "external_id": "disk", "message": "again"})
	if n3["id"] == id {
		t.Fatal("publish after resolve should create a new row")
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications/"+id+"/reopen", admin, nil); code != 409 {
		t.Fatalf("reopen into a reused external_id: want 409, got %d", code)
	}

	_, list := call(t, ts, "GET", "/api/v1/notifications?topic=infra", admin, nil)
	if got := len(list["notifications"].([]any)); got != 1 || list["seq"] != 8.0 {
		t.Fatalf("list open: %d rows, seq %v", got, list["seq"])
	}
	_, list = call(t, ts, "GET", "/api/v1/notifications?state=all", admin, nil)
	if got := len(list["notifications"].([]any)); got != 2 {
		t.Fatalf("list all: %d rows", got)
	}
	if code, c := call(t, ts, "POST", "/api/v1/notifications/clear", admin, map[string]string{"topic": "infra"}); code != 200 || c["cleared"] != 1.0 {
		t.Fatalf("clear: %d %v", code, c)
	}
	_, top := call(t, ts, "GET", "/api/v1/topics", admin, nil)
	if tc := top["topics"].([]any)[0].(map[string]any); tc["topic"] != "infra" || tc["open"] != 0.0 {
		t.Fatalf("topics: %v", top)
	}
}

func TestScopesAndAllowList(t *testing.T) {
	s, ts, admin := newTest(t)
	reader := mkToken(t, s, "phone", []string{ScopeRead}, []string{"home"})
	sender := mkToken(t, s, "script", []string{ScopePublish}, nil)
	_, home := call(t, ts, "POST", "/api/v1/notifications", sender, map[string]any{"topic": "home", "message": "door"})
	_, infra := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "disk"})

	cases := []struct {
		method, path, tok string
		want              int
	}{
		{"GET", "/api/v1/notifications", "", 401},
		{"GET", "/api/v1/notifications", "clrd_wrong", 401},
		{"POST", "/api/v1/notifications", reader, 403},                        // read cannot publish
		{"GET", "/api/v1/notifications", sender, 403},                         // publish cannot read
		{"GET", "/api/v1/notifications/" + infra["id"].(string), reader, 403}, // outside allow-list
		{"POST", "/api/v1/notifications/" + infra["id"].(string) + "/dismiss", reader, 403},
		{"GET", "/api/v1/notifications?topic=infra", reader, 403},
		{"POST", "/api/v1/notifications/" + home["id"].(string) + "/resolve", reader, 403}, // resolve is the sender's
		{"POST", "/api/v1/notifications/" + home["id"].(string) + "/dismiss", reader, 200},
		{"GET", "/api/v1/tokens", reader, 403},
		{"GET", "/api/v1/devices", sender, 403}, // publish-only learns nothing about devices
		{"POST", "/api/v1/notifications/clear", admin, 400},
		{"GET", "/api/v1/tokens", admin, 200},
	}
	for _, c := range cases {
		var body any
		if c.method == "POST" && strings.HasSuffix(c.path, "notifications") {
			body = map[string]any{"topic": "home", "message": "x"}
		}
		if strings.HasSuffix(c.path, "/clear") { // both selectors: refused, not widened to the topic
			body = map[string]any{"topic": "infra", "ids": []string{infra["id"].(string)}}
		}
		if code, out := call(t, ts, c.method, c.path, c.tok, body); code != c.want {
			t.Errorf("%s %s: want %d, got %d %v", c.method, c.path, c.want, code, out)
		}
	}
	_, list := call(t, ts, "GET", "/api/v1/notifications?state=all", reader, nil)
	if ns := list["notifications"].([]any); len(ns) != 1 || ns[0].(map[string]any)["topic"] != "home" {
		t.Fatalf("allow-listed list leaked: %v", list)
	}
	_, top := call(t, ts, "GET", "/api/v1/topics", reader, nil)
	if len(top["topics"].([]any)) != 1 {
		t.Fatalf("allow-listed topics leaked: %v", top)
	}
	_, dev := call(t, ts, "GET", "/api/v1/devices", reader, nil)
	if d := dev["devices"].([]any); len(d) != 1 || d[0].(map[string]any)["name"] != "phone" {
		t.Fatalf("limited token saw other devices: %v", dev)
	}
	if _, dev = call(t, ts, "GET", "/api/v1/devices", admin, nil); len(dev["devices"].([]any)) != 3 {
		t.Fatalf("admin devices: %v", dev)
	}
}

func TestMounts(t *testing.T) {
	s, ts, admin := newTest(t)
	page := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, name) })
	}
	s.Mount("/", page("root"))
	s.Mount("/docs", page("docs"))
	s.Mount("/app/", page("app"))
	s.Mount("/mcp", page("mcp"))
	for path, want := range map[string]string{"/": "root", "/docs": "docs", "/docs/api": "docs", "/app": "app",
		"/app/x/y": "app", "/mcp": "mcp"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(b) != want {
			t.Errorf("%s: want %q, got %q", path, want, b)
		}
	}
	if code, _ := call(t, ts, "GET", "/api/v1/topics", admin, nil); code != 200 {
		t.Errorf("api behind mounts: %d", code)
	}
	if code, _ := call(t, ts, "GET", "/healthz", "", nil); code != 200 {
		t.Errorf("healthz behind mounts: %d", code)
	}
	if code, _ := call(t, ts, "GET", "/docsx", "", nil); code == 200 {
		t.Error("/docsx must not hit the /docs mount")
	}
	for _, own := range []string{"/api", "/api/v1/x", "/healthz", "/setup"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Mount(%q) should panic", own)
				}
			}()
			s.Mount(own, page("x"))
		}()
	}
}

func TestSessionCookieAndSetup(t *testing.T) {
	s, ts, _ := newTest(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Setup: no admin yet in a fresh server, so a code exists.
	s2, err := New(Options{DB: filepath.Join(t.TempDir(), "fresh.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.bootstrap(context.Background()); err != nil || s2.setupCode == "" {
		t.Fatalf("bootstrap: %v %q", err, s2.setupCode)
	}
	ts2 := httptest.NewServer(s2)
	defer ts2.Close()
	code := s2.setupCode
	for _, m := range []string{"GET", "HEAD", "GET"} { // viewing (or a link checker) must not use the code up
		req, _ := http.NewRequest(m, ts2.URL+"/setup?code="+code, nil)
		res, _ := http.DefaultClient.Do(req)
		if res.StatusCode != 200 {
			t.Fatalf("setup %s: %d", m, res.StatusCode)
		}
	}
	if res, _ := http.Get(ts2.URL + "/setup?code=wrong"); res.StatusCode != 404 {
		t.Fatalf("setup wrong code: %d", res.StatusCode)
	}
	res, _ := noRedirect.PostForm(ts2.URL+"/setup", url.Values{"code": {code}, "name": {"laptop"}})
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(b), "clrd_") || len(res.Cookies()) == 0 {
		t.Fatalf("setup POST: %d %s", res.StatusCode, b)
	}
	if res, _ := noRedirect.PostForm(ts2.URL+"/setup", url.Values{"code": {code}}); res.StatusCode != 404 {
		t.Fatalf("setup code reused: %d", res.StatusCode)
	}

	// An admin made on the CLI kills a pending setup code.
	s3, err := New(Options{DB: filepath.Join(t.TempDir(), "cli.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	s3.bootstrap(context.Background())
	mkToken(t, s3, "cli", []string{ScopeAdmin}, nil)
	ts3 := httptest.NewServer(s3)
	defer ts3.Close()
	if res, _ := http.Get(ts3.URL + "/setup?code=" + s3.setupCode); res.StatusCode != 404 {
		t.Fatalf("setup after CLI admin: want 404, got %d", res.StatusCode)
	}

	// Session: form sign-in redirects and sets the cookie.
	raw := mkToken(t, s, "browser", []string{ScopeRead, ScopePublish}, nil)
	res, _ = noRedirect.PostForm(ts.URL+"/api/v1/session", url.Values{"token": {raw}, "next": {"//evil.example"}})
	if res.StatusCode != 303 || res.Header.Get("Location") != "/app" {
		t.Fatalf("session form: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	cookie := res.Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure { // Secure only over https
		t.Fatalf("session cookie flags: %+v", cookie)
	}
	// next stays on this site: a tab that url.Parse rejects would reach the
	// browser uncleaned, and the browser drops it into "//evil.example".
	for next, want := range map[string]string{"/\t/evil.example": "/app", "/\\evil.example": "/app", "/./\\evil.example": "/app", "//evil.example": "/app",
		"https://evil.example/app": "/app", "/app?topic=infra": "/app?topic=infra"} {
		res, err := noRedirect.PostForm(ts.URL+"/api/v1/session", url.Values{"token": {raw}, "next": {next}})
		if err != nil { // Go's client refuses a Location with a tab; a browser follows it
			t.Errorf("next %q: %v", next, err)
		} else if res.StatusCode != 303 || res.Header.Get("Location") != want {
			t.Errorf("next %q: %d %q, want %q", next, res.StatusCode, res.Header.Get("Location"), want)
		}
	}
	// Behind a TLS proxy the cookie is Secure.
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/session", strings.NewReader(url.Values{"token": {raw}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	if res, _ := noRedirect.Do(req); res.StatusCode != 303 || len(res.Cookies()) != 1 || !res.Cookies()[0].Secure {
		t.Errorf("cookie behind TLS proxy: %d %+v", res.StatusCode, res.Cookies())
	}
	// A wrong token on the form goes back to the page with a flag, not to raw JSON.
	res, _ = noRedirect.PostForm(ts.URL+"/api/v1/session", url.Values{"token": {"clrd_nope"}, "next": {"/app?topic=infra"}})
	if res.StatusCode != 303 || res.Header.Get("Location") != "/app?error=token&topic=infra" || len(res.Cookies()) != 0 {
		t.Fatalf("bad token on form: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	// Login forgery: another site cannot sign this browser in as someone else.
	req, _ = http.NewRequest("POST", ts.URL+"/api/v1/session", strings.NewReader(url.Values{"token": {raw}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if res, _ := noRedirect.Do(req); res.StatusCode != 403 || len(res.Cookies()) != 0 {
		t.Fatalf("cross-origin sign-in: %d", res.StatusCode)
	}
	with := func(method, path, origin string) int {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(`{"topic":"home","message":"hi"}`))
		req.AddCookie(cookie)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := with("GET", "/api/v1/topics", ""); c != 200 {
		t.Errorf("cookie GET: %d", c)
	}
	if c := with("POST", "/api/v1/notifications", ts.URL); c != 200 {
		t.Errorf("cookie same-origin POST: %d", c)
	}
	if c := with("POST", "/api/v1/notifications", "https://evil.example"); c != 401 {
		t.Errorf("cookie cross-origin POST: want 401, got %d", c)
	}
	if c := with("POST", "/home/x/resolve", ""); c != 401 {
		t.Errorf("cookie on resolve-by-key: want 401, got %d", c)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/session", "", map[string]string{"token": "clrd_nope"}); code != 401 {
		t.Errorf("bad session token: %d", code)
	}
}

// TestOpenAPIMatchesRoutes keeps openapi.json honest: every route the server
// registers is documented, and nothing documented is missing.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	s, ts, _ := newTest(t)
	code, doc := call(t, ts, "GET", "/openapi.json", "", nil)
	if code != 200 {
		t.Fatalf("openapi.json: %d", code)
	}
	documented := map[string]bool{}
	for path, ops := range doc["paths"].(map[string]any) {
		for method := range ops.(map[string]any) {
			if path == "/" {
				path = "/{$}"
			}
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}
	for _, r := range s.routes {
		if !documented[r] {
			t.Errorf("route %q is not in openapi.json", r)
		}
		delete(documented, r)
	}
	for r := range documented {
		t.Errorf("openapi.json documents %q, which the server does not serve", r)
	}
}

// A key of . or .. is dropped from every by-key URL, so it is refused.
func TestDotKeysRejected(t *testing.T) {
	_, ts, admin := newTest(t)
	for _, k := range []string{".", ".."} {
		if code, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "external_id": k}); code != 400 {
			t.Errorf("external_id %q: want 400, got %d", k, code)
		}
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "external_id": "..."}); code != 200 {
		t.Errorf("external_id ...: want 200, got %d", code)
	}
}

// Get honours its context, so a caller's deadline (MCP wait_for_reply) holds.
func TestGetHonoursContext(t *testing.T) {
	s, ts, admin := newTest(t)
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "title": "x"})
	tok, _, err := s.CreateToken(context.Background(), "reader", []string{ScopeRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), tok, n["id"].(string)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Get(ctx, tok, n["id"].(string)); err == nil {
		t.Fatal("Get with a cancelled context succeeded")
	}
}

// The last-seen write is part of the request: it stops when the request does (#32).
func TestTouchHonoursContext(t *testing.T) {
	s, _, _ := newTest(t)
	tok, _, err := s.CreateToken(context.Background(), "phone", []string{ScopeRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := func() (v sql.NullInt64) {
		if err := s.db.QueryRow(`SELECT last_seen_at FROM tokens WHERE id = ?`, tok.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.touch(ctx, tok)
	if v := seen(); v.Valid {
		t.Fatalf("touch wrote with a cancelled context: %v", v)
	}
	s.seen.Delete(tok.ID) // the failed write above still counts for the once-a-minute limit
	s.touch(context.Background(), tok)
	if v := seen(); !v.Valid {
		t.Fatal("touch did not write with a live context")
	}
}

// Reply actions (issue 8): validation, first answer wins, one event per
// answer, republish and reopen clear it, ntfy never sees a reply button.
func TestReplyActions(t *testing.T) {
	s, ts, admin := newTest(t)
	pub := func(actions any) (int, map[string]any) {
		return call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "agents", "external_id": "q", "message": "deploy?", "actions": actions})
	}
	r := func(v string) map[string]any { return map[string]any{"label": v, "reply": v} }
	for name, bad := range map[string]any{
		"url and reply": []any{map[string]any{"label": "x", "url": "https://example.com", "reply": "x"}},
		"neither":       []any{map[string]any{"label": "x"}},
		"bad chars":     []any{r("no way")},
		"too long":      []any{r(strings.Repeat("a", 65))},
		"duplicate":     []any{r("yes"), r("yes")},
		"four":          []any{r("a"), r("b"), r("c"), r("d")},
	} {
		if code, _ := pub(bad); code != 400 {
			t.Errorf("%s: want 400, got %d", name, code)
		}
	}
	code, n := pub([]any{r("yes"), r("no"), r(strings.Repeat("a", 64)), map[string]any{"label": "Docs", "url": "https://example.com"}})
	if code != 200 || n["reply"] != "" {
		t.Fatalf("publish: %d %v", code, n)
	}
	id := n["id"].(string)
	phone := mkToken(t, s, "pixel-9", []string{ScopeRead}, nil)
	// Answering needs read on the topic: not publish alone, not another topic.
	for name, tok := range map[string]string{
		"publish-only": mkToken(t, s, "sender", []string{ScopePublish}, nil),
		"other topic":  mkToken(t, s, "elsewhere", []string{ScopeRead}, []string{"infra"}),
	} {
		if code, _ := call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", tok, map[string]any{"reply": "yes"}); code != 403 {
			t.Errorf("%s token: want 403, got %d", name, code)
		}
	}
	reply := func(v string) (int, map[string]any) {
		return call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", phone, map[string]any{"reply": v})
	}
	for _, v := range []string{"maybe", "", "Docs"} {
		if code, _ := reply(v); code != 400 {
			t.Errorf("reply %q: want 400, got %d", v, code)
		}
	}
	seq := s.CurrentSeq()
	if code, n := call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", phone, map[string]any{"reply": "yes", "seq": seq}); code != 200 || n["reply"] != "yes" || n["state"] != "acknowledged" || n["state_by"] != "pixel-9" {
		t.Fatalf("reply yes: %d %v", code, n)
	}
	if s.CurrentSeq() != seq+1 {
		t.Errorf("reply logged %d events, want 1", s.CurrentSeq()-seq)
	}
	if code, _ := reply("no"); code != 409 {
		t.Errorf("second answer: want 409, got %d", code)
	}
	if code, n := pub([]any{r("yes"), r("no")}); code != 200 || n["id"] != id || n["reply"] != "" {
		t.Fatalf("republish kept the answer: %d %v", code, n)
	}
	// A button still showing the old question must not answer the new one.
	if code, _ := call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", phone, map[string]any{"reply": "maybe", "seq": seq + 1}); code != 409 {
		t.Errorf("tap on the replaced question: want 409, got %d", code)
	}
	if code, n := reply("no"); code != 200 || n["reply"] != "no" {
		t.Fatalf("answer after republish: %d %v", code, n)
	}
	if code, n := call(t, ts, "POST", "/api/v1/notifications/"+id+"/reopen", phone, nil); code != 200 || n["reply"] != "" {
		t.Fatalf("reopen kept the answer: %d %v", code, n)
	}
	// A new question with reopen goes back to active in one write, so no tap
	// can land between the republish and the reset.
	if code, n := reply("yes"); code != 200 || n["state"] != "acknowledged" {
		t.Fatalf("answer before reopen: %d %v", code, n)
	}
	seq = s.CurrentSeq()
	if code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "agents", "external_id": "q", "message": "again?", "reopen": true,
		"actions": []any{r("yes"), r("no")}}); code != 200 || n["id"] != id || n["state"] != "active" || n["reply"] != "" || s.CurrentSeq() != seq+1 {
		t.Fatalf("republish with reopen: %d %v, %d events", code, n, s.CurrentSeq()-seq)
	}
	call(t, ts, "POST", "/api/v1/notifications/"+id+"/dismiss", phone, nil)
	if code, _ := reply("yes"); code != 409 {
		t.Errorf("answer on a dismissed row: want 409, got %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications/nope/reply", phone, map[string]any{"reply": "yes"}); code != 404 {
		t.Errorf("unknown id: want 404, got %d", code)
	}

	// ntfy subscribers get the link, never a view action with no url.
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ntq", "message": "m",
		"actions": []any{r("yes"), map[string]any{"label": "Docs", "url": "https://example.com"}}})
	_, out := raw(t, "GET", ts.URL+"/ntq/json?poll=1", admin, "", nil)
	if l := lines(out); len(l) != 1 || len(ntfyDecode(t, l[0]).Actions) != 1 || ntfyDecode(t, l[0]).Actions[0].URL != "https://example.com" {
		t.Errorf("ntfy actions: %s", out)
	}
}

// A database made before reply existed gains the column on start.
func TestReplyColumnAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clrd.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := schema
	for _, col := range []string{"reply", "icon"} {
		line := "  " + col + " TEXT NOT NULL DEFAULT '',\n"
		if !strings.Contains(old, line) {
			t.Fatalf("schema has no %s line to drop", col)
		}
		old = strings.Replace(old, line, "", 1)
	}
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for i := 0; i < 2; i++ { // the second start finds the column already there
		s, err := New(Options{DB: path, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		tok, _, _ := s.CreateToken(context.Background(), "a", []string{ScopeAdmin}, nil)
		if n, err := s.Publish(context.Background(), tok, PublishInput{Topic: "infra", Message: "m",
			Icon: "https://example.com/i.png"}); err != nil || n.Icon != "https://example.com/i.png" {
			t.Fatalf("publish on start %d: %v", i, err)
		}
		s.Close()
	}
}

func TestIcons(t *testing.T) {
	s, ts, admin := newTest(t)
	reader := mkToken(t, s, "reader", []string{ScopeRead}, nil)
	other := mkToken(t, s, "other", []string{ScopePublish}, []string{"other"})
	const icon = "https://example.com/disk.png"

	for _, tc := range []struct {
		tok  string
		body any
		want int
	}{
		{reader, map[string]string{"icon": icon}, 403},
		{other, map[string]string{"icon": icon}, 403},
		{admin, map[string]string{"icon": "javascript:alert(1)"}, 400},
		{admin, map[string]string{"icon": "javascript://example.com/%0aalert(1)"}, 400},
		{admin, map[string]string{"icon": "data:image/png;base64,AA=="}, 400},
		{admin, map[string]string{"icon": "/rel.png"}, 400},
		{admin, map[string]string{"icon": "https://:443/x.png"}, 400},
		{admin, map[string]string{"icon": "https://example.com:65536/x.png"}, 400},
		{admin, map[string]string{"icon": icon}, 200},
		{admin, map[string]string{}, 400}, // a missing key must not wipe it
		{admin, nil, 400},
	} {
		if code, out := call(t, ts, "PUT", "/api/v1/topics/infra", tc.tok, tc.body); code != tc.want {
			t.Fatalf("PUT topic icon %v: %d %v", tc.body, code, out)
		}
	}
	_, out := call(t, ts, "GET", "/api/v1/topics", admin, nil)
	if ts := out["topics"].([]any); len(ts) != 1 || ts[0].(map[string]any)["icon"] != icon {
		t.Fatalf("topic list: %v", out)
	}

	// The topic's icon fills in; an explicit one wins, and an upsert stores it.
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "a", "external_id": "k"})
	if n["icon"] != icon {
		t.Fatalf("default icon: %v", n)
	}
	_, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "b", "external_id": "k",
		"icon": "https://example.com/own.png"})
	_, n = call(t, ts, "GET", "/api/v1/notifications/"+n["id"].(string), admin, nil)
	if n["icon"] != "https://example.com/own.png" {
		t.Fatalf("explicit icon on upsert: %v", n)
	}
	// A bad icon is refused before a resolve can act.
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "external_id": "k",
		"status": "resolved", "icon": "javascript:x"}); code != 400 {
		t.Fatalf("bad icon on resolve: %d", code)
	}
	if _, n = call(t, ts, "GET", "/api/v1/notifications/"+n["id"].(string), admin, nil); n["state"] != Active {
		t.Fatalf("bad icon resolved the row: %v", n)
	}

	// ntfy: X-Icon maps over; a bad one is dropped, not refused.
	code, body := raw(t, "PUT", ts.URL+"/infra", admin, "m", map[string]string{"X-Icon": "https://example.com/n.png"})
	if m := ntfyDecode(t, body); code != 200 || m.Icon != "https://example.com/n.png" {
		t.Fatalf("ntfy icon: %d %s", code, body)
	}
	code, body = raw(t, "PUT", ts.URL+"/infra", admin, "m", map[string]string{"X-Icon": "javascript:x"})
	if m := ntfyDecode(t, body); code != 200 || m.Icon != icon {
		t.Fatalf("ntfy bad icon: %d %s", code, body)
	}
	code, body = raw(t, "POST", ts.URL+"/", admin, `{"topic":"infra","message":"m","icon":"https://example.com/j.png"}`, nil)
	if m := ntfyDecode(t, body); code != 200 || m.Icon != "https://example.com/j.png" {
		t.Fatalf("ntfy JSON icon: %d %s", code, body)
	}
	code, body = raw(t, "POST", ts.URL+"/", admin, `{"topic":"infra","message":"m","icon":123}`, nil)
	if m := ntfyDecode(t, body); code != 200 || m.Icon != icon {
		t.Fatalf("ntfy JSON non-string icon: %d %s", code, body)
	}

	// Removing the topic's icon stops the fill.
	call(t, ts, "PUT", "/api/v1/topics/infra", admin, map[string]string{"icon": ""})
	if _, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "d"}); n["icon"] != "" {
		t.Fatalf("icon after removal: %v", n)
	}
}
