package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLimiterRefill(t *testing.T) {
	l, t0 := newLimiter(2, time.Minute), time.Unix(1000, 0)
	if l.take("a", t0) != 0 || l.take("a", t0) != 0 {
		t.Fatal("burst of 2 refused")
	}
	if w := l.take("a", t0.Add(-time.Second)); w != time.Minute { // an older now changes nothing
		t.Fatalf("stale now: wait %v", w)
	}
	if w := l.take("a", t0); w != time.Minute {
		t.Fatalf("third take: wait %v, want 1m", w)
	}
	if l.take("b", t0) != 0 {
		t.Fatal("other key limited")
	}
	if w := l.take("a", t0.Add(30*time.Second)); w != 30*time.Second {
		t.Fatalf("half refilled: wait %v, want 30s", w)
	}
	if l.take("a", t0.Add(time.Minute)) != 0 {
		t.Fatal("not refilled after a minute")
	}
	for i := 0; i < 10000; i++ { // a full map of empty buckets: new keys share one
		l.m[strconv.Itoa(i)] = &bucket{at: t0}
	}
	if l.take("new1", t0) != 0 || l.take("new2", t0) != 0 || l.take("new3", t0) == 0 || len(l.m) != 10003 { // a, b, 10000 full, one overflow
		t.Fatalf("overflow: %d buckets", len(l.m))
	}
}

func TestPasswordLimit(t *testing.T) {
	_, u, toks, _ := importFixture(t, NtfyImport{})
	try := func(pass, xff string) *http.Response {
		req, _ := http.NewRequest("GET", u+"/alerts/json?poll=1", nil)
		req.SetBasicAuth("phone", pass)
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	for i := 0; i < 20; i++ { // right passwords never trip it
		if c := try("phonepass", "").StatusCode; c != 200 {
			t.Fatalf("right password %d: %d", i, c)
		}
	}
	for i := 0; i < 15; i++ { // a route that never checks the password costs nothing
		req, _ := http.NewRequest("GET", u+"/v1/health", nil)
		req.SetBasicAuth("phone", "wrong")
		if c, _ := send(t, req); c != 200 {
			t.Fatalf("health %d: %d", i, c)
		}
	}
	for i := 0; i < 10; i++ {
		if c := try("wrong", "").StatusCode; c != 401 {
			t.Fatalf("wrong password %d: %d, want 401", i, c)
		}
	}
	res := try("wrong", "")
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" || res.Header.Get("Retry-After") == "0" {
		t.Fatalf("11th wrong password: %d Retry-After %q, want 429", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if c := try("phonepass", "").StatusCode; c != 429 {
		t.Fatalf("right password while blocked: %d, want 429", c)
	}
	if c, _ := bearer(t, "GET", u+"/news/json?poll=1", toks["pixel"], ""); c != 200 {
		t.Fatalf("token while passwords are blocked: %d, want 200", c)
	}
	stream, _ := http.NewRequest("GET", u+"/alerts/json", nil) // a held stream must not hold a limiter token
	stream.SetBasicAuth("phone", "phonepass")
	stream.Header.Set("X-Forwarded-For", "203.0.113.20")
	var held []*http.Response
	for i := 0; i < 12; i++ {
		res, err := http.DefaultClient.Do(stream.Clone(stream.Context()))
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("stream %d: %v %v", i, err, res)
		}
		held = append(held, res)
	}
	for _, res := range held {
		res.Body.Close()
	}
	if c := try("phonepass", "203.0.113.9").StatusCode; c != 200 {
		t.Fatalf("other address behind the proxy: %d, want 200", c)
	}
	if got := clientIP(&http.Request{RemoteAddr: "198.51.100.7:1", Header: http.Header{"X-Forwarded-For": {"203.0.113.9"}}}, nil); got != "198.51.100.7" {
		t.Fatalf("public peer trusted X-Forwarded-For: %s", got)
	}
	if got := clientIP(&http.Request{RemoteAddr: "[2001:db8::1]:1"}, nil); got != "2001:db8::" {
		t.Fatalf("IPv6 not cut to /64: %s", got)
	}
	// Issue #171: with CLRD_TRUSTED_PROXIES set, only those peers' X-Forwarded-For counts.
	t.Setenv("CLRD_TRUSTED_PROXIES", "10.0.0.5, 172.16.0.0/12")
	o, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	for peer, want := range map[string]string{"10.0.0.5:1": "203.0.113.9", "172.20.1.1:1": "203.0.113.9", "10.0.0.6:1": "10.0.0.6", "127.0.0.1:1": "127.0.0.1"} {
		if got := clientIP(&http.Request{RemoteAddr: peer, Header: http.Header{"X-Forwarded-For": {"203.0.113.9"}}}, o.TrustedProxies); got != want {
			t.Errorf("peer %s with trusted proxies: %s, want %s", peer, got, want)
		}
	}
	for _, bad := range []string{"10.0.0.0/33", " , "} { // a list of nothing must not fall back to trusting every private peer
		t.Setenv("CLRD_TRUSTED_PROXIES", bad)
		if _, err := ConfigFromEnv(); err == nil {
			t.Errorf("bad CLRD_TRUSTED_PROXIES %q accepted", bad)
		}
	}
	t.Setenv("CLRD_TRUSTED_PROXIES", "::ffff:10.0.0.5")
	if o, _ := ConfigFromEnv(); clientIP(&http.Request{RemoteAddr: "10.0.0.5:1", Header: http.Header{"X-Forwarded-For": {"203.0.113.9"}}}, o.TrustedProxies) != "203.0.113.9" {
		t.Error("IPv4-mapped proxy address not matched")
	}
}

func TestPublishLimit(t *testing.T) {
	s, ts, _ := newTest(t)
	if s.opt.RateToken != 60 || s.opt.RateTopic != 60 || s.tokLimit == nil || s.topicLimit == nil {
		t.Fatalf("limits should default on at 60: %+v", s.opt)
	}
	for _, l := range []*limiter{s.tokLimit, s.topicLimit} {
		if l.burst != 60 || l.every != time.Second {
			t.Fatalf("default limiter: burst %v every %v", l.burst, l.every)
		}
	}
	s.tokLimit, s.topicLimit = newLimiter(3, time.Hour), newLimiter(5, time.Hour)
	a := mkToken(t, s, "a", []string{ScopePublish}, nil)
	b := mkToken(t, s, "b", []string{ScopePublish}, nil)
	pub := func(tok, topic string, want int, wantMsg string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"topic": topic, "title": "x"})
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/notifications", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s to %s: got %d %v, want %d", tok[:6], topic, res.StatusCode, out, want)
		}
		if want == 429 && (out["code"] != "rate_limited" || res.Header.Get("Retry-After") == "" || !strings.Contains(out["error"].(string), wantMsg)) {
			t.Fatalf("429 without code, message or Retry-After: %v %v", out, res.Header)
		}
	}
	for range 3 {
		pub(a, "news", 200, "")
	}
	pub(a, "other", 429, "token") // token cap, any topic
	pub(b, "news", 200, "")
	pub(b, "news", 200, "")
	pub(b, "news", 429, "topic") // topic cap across tokens; b's turn is given back
	pub(b, "x", 200, "")
	pub(b, "y", 429, "token") // b spent 3: the refund above is what let "x" through
}

func TestRateConfig(t *testing.T) {
	t.Setenv("CLRD_RATE_TOKEN", "0")
	t.Setenv("CLRD_RATE_TOPIC", "120")
	o, err := ConfigFromEnv()
	if err != nil || o.RateToken != -1 || o.RateTopic != 120 {
		t.Fatalf("got %d %d %v", o.RateToken, o.RateTopic, err)
	}
	if perMinute(-1) != nil || perMinute(120).every != time.Minute/120 {
		t.Fatal("perMinute")
	}
	t.Setenv("CLRD_RATE_TOPIC", "lots")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("bad value should fail")
	}
}
