package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thejdubb02/clrd/server"
)

// TestWebOnRealServer drives the mounted pages the way a browser does:
// sign in with a token, see a notification, dismiss it, see it move.
func TestWebOnRealServer(t *testing.T) {
	s, ts, raw := realServer(t)
	c := browser()
	get := func(path string) (int, string) {
		t.Helper()
		code, body, _ := fetch(t, c, ts.URL+path)
		return code, body
	}
	for _, p := range []string{"/", "/docs/", "/favicon.svg", "/robots.txt", "/sitemap.xml"} {
		if code, _ := get(p); code != 200 {
			t.Fatalf("GET %s: %d", p, code)
		}
	}
	publish(t, ts, raw, "home", "disk full")
	publish(t, ts, raw, "work", "deploy done")
	if code, body := get("/app"); code != 401 || strings.Contains(body, "disk full") {
		t.Fatalf("/app signed out: %d", code)
	}

	// Sign in through the form the page renders, first with a bad token.
	_, page := get("/app?topic=home")
	if code, body := submit(t, c, ts.URL, page, "clrd_wrong"); code != 401 || !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `value="/app?topic=home"`) {
		t.Fatalf("bad token: %d, want 401 with the error shown and next kept without error=", code)
	}
	if _, body := get("/app?topic=home"); strings.Contains(body, `role="alert"`) {
		t.Fatal("error shown before any sign-in attempt")
	}
	if code, body := submit(t, c, ts.URL, page, raw); code != 200 || !strings.Contains(body, "disk full") || !strings.Contains(body, `data-me="phone"`) {
		t.Fatalf("good token: %d, want the inbox with this device's name", code)
	}
	for path, want := range map[string]string{"/app": "disk full", "/app?topic=home": "disk full", "/app?topic=work": "deploy done", "/app?q=deploy": "deploy done"} {
		other := map[string]string{"disk full": "deploy done", "deploy done": "disk full"}[want]
		if _, body := get(path); !strings.Contains(body, want) || (path != "/app" && strings.Contains(body, other)) {
			t.Errorf("%s: want %q and not %q", path, want, other)
		}
	}
	if _, body := get("/app/settings"); !strings.Contains(body, "<span>phone</span>") {
		t.Fatal("settings misses the device")
	}
	if _, body := get("/app/cleared"); strings.Contains(body, "disk full") {
		t.Fatal("cleared shows an active notification")
	}
	list, _ := s.List(context.Background(), mustTok(t, s, raw), server.ListQuery{State: "active", Topics: []string{"home"}})
	// The page offers Undo only when the answer says this device cleared it.
	if n := act(t, c, ts.URL, list[0].ID, "dismiss"); n.State != "dismissed" || n.StateBy != "phone" {
		t.Fatalf("dismiss answered %+v, want dismissed by phone", n)
	}
	if _, body := get("/app"); strings.Contains(body, "disk full") {
		t.Fatal("inbox still shows a dismissed notification")
	}
	if _, body := get("/app/cleared"); !strings.Contains(body, "disk full") {
		t.Fatal("cleared misses the dismissed notification")
	}
}

func mustTok(t *testing.T, s *server.Server, raw string) *server.Token {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	tok := s.Authenticate(r)
	if tok == nil {
		t.Fatal("token not accepted")
	}
	return tok
}

// A link with ?auth= signs the browser in and lands on the same page without
// the token; the cookie it leaves can act, which ?auth= alone could not.
func TestSignInLink(t *testing.T) {
	_, ts, raw := realServer(t)
	publish(t, ts, raw, "home", "disk full")
	enc := base64.RawURLEncoding.EncodeToString
	for name, auth := range map[string]string{"bearer": enc([]byte("Bearer " + raw)), "basic": enc([]byte("Basic " + base64.StdEncoding.EncodeToString([]byte(":"+raw))))} {
		c := browser()
		code, body, u := fetch(t, c, ts.URL+"/app/?topic=home&auth="+auth)
		if code != 200 || u.RequestURI() != "/app/?topic=home" || !strings.Contains(body, "disk full") {
			t.Fatalf("%s: %d at %s, want the inbox at /app/?topic=home", name, code, u.RequestURI())
		}
		id := regexp.MustCompile(`data-id="([^"]+)"`).FindStringSubmatch(body)[1]
		if n := act(t, c, ts.URL, id, "ack"); n.State != "acknowledged" {
			t.Fatalf("%s: cookie from the link cannot act: %+v", name, n)
		}
	}
	// From another site the link only drops the token: no session, sign-in page.
	req, _ := http.NewRequest("GET", ts.URL+"/app?topic=home&auth="+enc([]byte("Bearer "+raw)), nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	c := browser()
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 || res.Request.URL.RequestURI() != "/app?topic=home" {
		t.Errorf("cross-site link: %d at %s, want the sign-in page without the token", res.StatusCode, res.Request.URL.RequestURI())
	}
	for _, auth := range []string{enc([]byte("Bearer clrd_wrong")), "not base64!"} {
		code, body, u := fetch(t, browser(), ts.URL+"/app?auth="+url.QueryEscape(auth))
		if code != 401 || u.RequestURI() != "/app?error=token" || !strings.Contains(body, `role="alert"`) {
			t.Errorf("auth=%q: %d at %s, want the sign-in error without the token in the URL", auth, code, u.RequestURI())
		}
	}
}

func realServer(t *testing.T) (*server.Server, *httptest.Server, string) {
	t.Helper()
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mountWeb(s, "")
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	_, raw, err := s.CreateToken(context.Background(), "phone", []string{"admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, ts, raw
}

func browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// fetch follows redirects like a browser and returns where it landed.
func fetch(t *testing.T, c *http.Client, u string) (int, string, *url.URL) {
	t.Helper()
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Request.URL
}

// submit posts the page's sign-in form to its own action, as a browser would.
func submit(t *testing.T, c *http.Client, base, page, token string) (int, string) {
	t.Helper()
	action := regexp.MustCompile(`<form class="signin-form" method="post" action="([^"]+)"`).FindStringSubmatch(page)
	next := regexp.MustCompile(`name="next" value="([^"]*)"`).FindStringSubmatch(page)
	if action == nil || next == nil {
		t.Fatal("no sign-in form on the page")
	}
	unesc := strings.NewReplacer("&amp;", "&", "&#43;", "+")
	res, err := c.PostForm(base+action[1], url.Values{"token": {token}, "next": {unesc.Replace(next[1])}})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func publish(t *testing.T, ts *httptest.Server, raw, topic, msg string) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/notifications", strings.NewReader(`{"topic":"`+topic+`","message":"`+msg+`"}`))
	req.Header.Set("Authorization", "Bearer "+raw)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("publish: %v %v", err, res)
	}
}

// TestActForm posts the card form the way a browser without JavaScript does.
func TestActForm(t *testing.T) {
	s, ts, raw := realServer(t)
	c := browser()
	publish(t, ts, raw, "home", "disk full")
	_, page, _ := fetch(t, c, ts.URL+"/app")
	submit(t, c, ts.URL, page, raw)
	list, _ := s.List(context.Background(), mustTok(t, s, raw), server.ListQuery{State: "active"})
	id := list[0].ID
	form := func(origin string, v url.Values) (int, string) {
		t.Helper()
		v.Set("id", id)
		req, _ := http.NewRequest("POST", ts.URL+"/app/act", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode, res.Request.URL.RequestURI()
	}
	state := func() *server.Notification {
		n, err := s.Get(context.Background(), mustTok(t, s, raw), id)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if code, at := form("https://evil.example", url.Values{"act": {"dismiss"}}); state().State != "active" {
		t.Fatalf("cross-origin form post changed it (%d at %s)", code, at)
	}
	if code, at := form(ts.URL, url.Values{"act": {"ack"}, "next": {"/app?topic=home"}}); code != 200 || at != "/app?topic=home" || state().State != "acknowledged" || state().StateBy != "phone" {
		t.Fatalf("ack: %d at %s, state %s by %s", code, at, state().State, state().StateBy)
	}
	if code, _ := form(ts.URL, url.Values{"snooze": {"1h"}}); code != 200 || state().State != "snoozed" {
		t.Fatalf("snooze: %d, state %s", code, state().State)
	}
}

// act posts an action with only the session cookie, the way the inbox does.
func act(t *testing.T, c *http.Client, base, id, action string) server.Notification {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/v1/notifications/"+id+"/"+action, nil)
	req.Header.Set("Origin", base)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var n server.Notification
	json.NewDecoder(res.Body).Decode(&n)
	if res.StatusCode != 200 {
		t.Fatalf("%s %s: %d", action, id, res.StatusCode)
	}
	return n
}
