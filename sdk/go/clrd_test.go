package clrd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type hit struct{ method, uri, body, auth string }

func fake(t *testing.T) (*Client, *[]hit) {
	var hits []hit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hits = append(hits, hit{r.Method, r.URL.RequestURI(), string(b), r.Header.Get("Authorization")})
		switch {
		case strings.HasPrefix(r.URL.Path, "/missing"):
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"no open notification","code":"not_found"}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/notifications"):
			io.WriteString(w, `{"notifications":[{"id":"01J","state":"active"}]}`)
		default:
			io.WriteString(w, `{"id":"01J","state":"active"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", "clrd_test"), &hits
}

func TestSend(t *testing.T) {
	c, hits := fake(t)
	n, err := c.Send(context.Background(), Message{Topic: "infra", Message: "Disk 91%", Priority: 4, ID: "disk-build"})
	if err != nil || n.ID != "01J" {
		t.Fatal(n, err)
	}
	h := (*hits)[0]
	var body map[string]any
	json.Unmarshal([]byte(h.body), &body)
	if h.uri != "/api/v1/notifications" || body["external_id"] != "disk-build" || body["priority"] != 4.0 || h.auth != "Bearer clrd_test" {
		t.Fatalf("%+v", h)
	}
	if _, ok := body["title"]; ok {
		t.Fatal("empty title sent")
	}
}

func TestPaths(t *testing.T) {
	c, hits := fake(t)
	ctx := context.Background()
	c.Resolve(ctx, "infra", "disk/build")
	c.ResolveID(ctx, "01J")
	c.Ack(ctx, "infra", "k")
	c.AckID(ctx, "01J")
	c.Dismiss(ctx, "01J")
	c.Snooze(ctx, "01J", "30m")
	c.Get(ctx, "01J")
	list, err := c.List(ctx, "infra", "")
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	want := []string{
		"POST /infra/disk%2Fbuild/resolve",
		"POST /api/v1/notifications/01J/resolve",
		"POST /infra/k/ack",
		"POST /api/v1/notifications/01J/ack",
		"POST /api/v1/notifications/01J/dismiss",
		"POST /api/v1/notifications/01J/snooze",
		"GET /api/v1/notifications/01J",
		"GET /api/v1/notifications?state=open&topic=infra",
	}
	for i, w := range want {
		if got := (*hits)[i].method + " " + (*hits)[i].uri; got != w {
			t.Errorf("%d: %s, want %s", i, got, w)
		}
	}
	if (*hits)[5].body != `{"for":"30m"}` {
		t.Errorf("snooze body %s", (*hits)[5].body)
	}
}

func TestError(t *testing.T) {
	c, _ := fake(t)
	c.URL += "/missing"
	_, err := c.Resolve(context.Background(), "infra", "nope")
	var e *Error
	if !errors.As(err, &e) || e.Status != 404 || e.Code != "not_found" {
		t.Fatal(err)
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	var got []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.RedirectHandler(other.URL+"/api/v1/notifications", http.StatusFound))
	defer srv.Close()
	for _, c := range []*Client{New(srv.URL, "clrd_test"), {URL: srv.URL, Token: "clrd_test"}} {
		_, err := c.Send(context.Background(), Message{Topic: "infra", Message: "x"})
		var e *Error
		if !errors.As(err, &e) || e.Status != 302 || !strings.Contains(e.Message, other.URL) {
			t.Fatal(err)
		}
	}
	if len(got) != 0 {
		t.Fatalf("redirect followed, token sent on: %q", got)
	}
}
