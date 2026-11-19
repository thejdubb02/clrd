package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thejdubb02/clrd/server"
)

// TestWebOnRealServer drives the mounted pages the way a browser does:
// sign in with a token, see a notification, dismiss it, see it move.
func TestWebOnRealServer(t *testing.T) {
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mountWeb(s, "")
	ts := httptest.NewServer(s)
	defer ts.Close()
	_, raw, err := s.CreateToken(context.Background(), "phone", []string{"admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	get := func(path string) (int, string) {
		t.Helper()
		res, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	for _, p := range []string{"/", "/docs/", "/favicon.svg", "/robots.txt", "/sitemap.xml"} {
		if code, _ := get(p); code != 200 {
			t.Fatalf("GET %s: %d", p, code)
		}
	}
	if code, body := get("/app"); code != 401 || strings.Contains(body, "disk full") {
		t.Fatalf("/app signed out: %d", code)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/notifications", strings.NewReader(`{"topic":"home","message":"disk full"}`))
	req.Header.Set("Authorization", "Bearer "+raw)
	if res, err := c.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("publish: %v %v", err, res)
	}
	if res, err := c.PostForm(ts.URL+"/api/v1/session", url.Values{"token": {raw}}); err != nil || res.StatusCode != 200 {
		t.Fatalf("sign in: %v %v", err, res)
	}
	if _, body := get("/app"); !strings.Contains(body, "disk full") {
		t.Fatal("inbox misses the notification")
	}
	if _, body := get("/app/settings"); !strings.Contains(body, "<span>phone</span>") {
		t.Fatal("settings misses the device")
	}
	if _, body := get("/app/cleared"); strings.Contains(body, "disk full") {
		t.Fatal("cleared shows an active notification")
	}
	list, _ := s.List(context.Background(), mustTok(t, s, raw), server.ListQuery{State: "active"})
	req, _ = http.NewRequest("POST", ts.URL+"/api/v1/notifications/"+list[0].ID+"/dismiss", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	if res, err := c.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("dismiss: %v %v", err, res)
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
