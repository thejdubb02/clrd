package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A client that sends headers and then stalls the body gets 408, not a
// handler stuck forever.
func TestSlowBodyTimesOut(t *testing.T) {
	_, ts, admin := newTest(t)
	old := bodyTimeout
	bodyTimeout = 200 * time.Millisecond
	defer func() { bodyTimeout = old }()
	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "POST /api/v1/notifications HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer "+admin+
		"\r\nContent-Type: application/json\r\nContent-Length: 50\r\n\r\n{")
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || res.StatusCode != 408 {
		t.Fatalf("stalled body: %v %v", res, err)
	}
	// A normal body still works, and a large one is refused.
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "ok"}); code != 201 && code != 200 {
		t.Fatalf("normal publish: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": strings.Repeat("x", maxBody)}); code != 413 {
		t.Fatalf("huge body: %d", code)
	}
}

// Publishing again with a delay hides the open row until then, the same as a
// new delayed publish, instead of updating it in place at once.
func TestDelayedRepublishSnoozes(t *testing.T) {
	_, ts, admin := newTest(t)
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "external_id": "k", "message": "now"})
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	_, n2 := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "external_id": "k", "message": "later",
		"snooze_until": until.Format(time.RFC3339Nano)})
	if n2["id"] != n["id"] || n2["state"] != Snoozed || n2["message"] != "later" || n2["snooze_until"] != until.Format("2006-01-02T15:04:05.999Z07:00") {
		t.Fatalf("delayed republish: %v", n2)
	}
}

// With an https base URL, a page served over plain http on the same host
// cannot sign a browser in.
func TestOriginChecksScheme(t *testing.T) {
	s, ts, _ := newTest(t)
	s.opt.BaseURL = "https://clrd.example"
	raw := mkToken(t, s, "browser", []string{ScopeRead}, nil)
	host := strings.TrimPrefix(ts.URL, "http://")
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for origin, want := range map[string]int{"http://" + host: 403, "https://" + host: 303} {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/session", strings.NewReader(url.Values{"token": {raw}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		res, err := noRedirect.Do(req)
		if err != nil || res.StatusCode != want {
			t.Fatalf("origin %s: want %d, got %v %v", origin, want, res.StatusCode, err)
		}
	}
}

// After a reset to an empty log, ready still carries id: 0, so EventSource
// stops sending the old Last-Event-ID and does not reset on every reconnect.
func TestSSEReadyIDAfterReset(t *testing.T) {
	_, ts, admin := newTest(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/events/sse", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Last-Event-ID", "5") // from a log this server never had
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	var prev string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("no ready: %v", err)
		}
		if strings.Contains(line, `"ready"`) {
			break
		}
		if line != "\n" {
			prev = line
		}
	}
	if prev != "id: 0\n" {
		t.Fatalf("ready after reset: id line %q", prev)
	}
}

// A prune that lands after ntfy picks its since point still returns what is
// left, not an empty 200.
func TestNtfyPollAcrossPrune(t *testing.T) {
	s, ts, admin := newTest(t)
	for _, m := range []string{"one", "two", "three"} {
		raw(t, "POST", ts.URL+"/alerts", admin, m, nil)
	}
	if _, err := s.db.Exec(`DELETE FROM events WHERE seq <= 2`); err != nil { // s.pruned not moved yet
		t.Fatal(err)
	}
	_, out := raw(t, "GET", ts.URL+"/alerts/json?poll=1", admin, "", nil)
	if l := lines(out); len(l) != 1 || ntfyDecode(t, l[0]).Message != "three" {
		t.Fatalf("poll across a prune: %v", l)
	}
}
