package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/thejdubb02/clrd/server"
)

// TestSubPath runs the server under a sub-path (CLRD_BASE_URL with a path, as
// behind a proxy at example.com/clrd/) and checks every page only links,
// posts and redirects inside it, and the API, sign-in and WebSocket work there.
func TestSubPath(t *testing.T) {
	if _, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "x.db"), BaseURL: "http://clrd.example/api"}); err == nil {
		t.Fatal("a sub-path of /api accepted: it would hide the server's own /api/v1 calls")
	}
	const base = "http://clrd.example/clrd/"
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), BaseURL: base, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mountWeb(s, base, false)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	_, raw, err := s.CreateToken(context.Background(), "phone", []string{"admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := ts.URL + "/clrd"

	// ntfy style publish at the root of the sub-path, then the native API.
	req, _ := http.NewRequest("POST", root+"/home", strings.NewReader("disk full https://b.example/"))
	req.Header.Set("Authorization", "Bearer "+raw)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("ntfy publish under the sub-path: %v %v", err, res)
	}
	req, _ = http.NewRequest("POST", root+"/api/v1/notifications", strings.NewReader(`{"topic":"work","message":"done","attachment":{"url":"https://e.example/a.png"}}`))
	req.Header.Set("Authorization", "Bearer "+raw)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("publish under the sub-path: %v %v", err, res)
	}

	local := regexp.MustCompile(`(?:href|src|action)="(/[^"]*)"`)
	check := func(path, body string) {
		t.Helper()
		for _, m := range local.FindAllStringSubmatch(body, -1) {
			if !strings.HasPrefix(m[1], "/clrd/") && !strings.HasPrefix(m[1], "//") {
				t.Errorf("%s: %s leaves the sub-path", path, m[0])
			}
		}
	}
	c := browser()
	for path, want := range map[string]int{"/": 200, "/docs/": 200, "/docs/spec": 200, "/app": 401, "/static/app.css": 200, "/favicon.svg": 200} {
		code, body, _ := fetch(t, c, root+path)
		if code != want {
			t.Fatalf("GET /clrd%s: %d, want %d", path, code, want)
		}
		check(path, body)
	}

	// Sign in through the form, the way a browser does.
	_, page, _ := fetch(t, c, root+"/app")
	action := regexp.MustCompile(`<form class="signin-form" method="post" action="([^"]+)"`).FindStringSubmatch(page)
	if action == nil || action[1] != "/clrd/api/v1/session" {
		t.Fatalf("sign-in form posts to %v", action)
	}
	res, err := c.PostForm(ts.URL+action[1], url.Values{"token": {raw}, "next": {"/app"}})
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Request.URL.Path != "/clrd/app" || !strings.Contains(string(inbox), "disk full") || !strings.Contains(string(inbox), `data-base="/clrd"`) {
		t.Fatalf("sign-in: %d at %s, want the inbox at /clrd/app", res.StatusCode, res.Request.URL.Path)
	}
	check("/app signed in", string(inbox))
	if u, _ := url.Parse(root + "/app"); len(c.Jar.Cookies(u)) == 0 {
		t.Fatal("no session cookie under the sub-path")
	}
	if u, _ := url.Parse(ts.URL + "/app"); len(c.Jar.Cookies(u)) != 0 {
		t.Fatal("session cookie leaks outside the sub-path")
	}
	for _, path := range []string{"/app/snoozed", "/app/cleared", "/app/settings"} {
		_, body, _ := fetch(t, c, root+path)
		check(path, body)
	}

	// Redirects the site makes stay under the sub-path.
	nofollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if res, err := nofollow.Get(root + "/docs"); err != nil || res.Header.Get("Location") != "/clrd/docs/" {
		t.Fatalf("/clrd/docs redirects to %q (%v)", res.Header.Get("Location"), err)
	}

	ws, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(root, "http")+"/api/v1/ws?since=0", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + raw}}})
	if err != nil {
		t.Fatalf("WebSocket under the sub-path: %v", err)
	}
	ws.Close(websocket.StatusNormalClosure, "")
}
