package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// raw does one request with a plain body and headers, returning status and body.
func raw(t *testing.T, method, url, tok, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if tok != "" {
		req.SetBasicAuth("", tok) // what the ntfy CLI and app send
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func ntfyDecode(t *testing.T, s string) ntfyMsg {
	t.Helper()
	var m ntfyMsg
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not a ntfy message: %q", s)
	}
	return m
}

// basicAuthParam is what the ntfy clients put in ?auth= for a token.
func basicAuthParam(tok string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("Bearer " + tok))
}

func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

func TestNtfyPublish(t *testing.T) {
	s, ts, admin := newTest(t)
	s.Mount("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "landing") }))

	if _, err := parseDuration("-106752d"); err == nil {
		t.Fatal("huge day count must not wrap around")
	}
	// A HEAD probe and a no-cache publish store nothing; the auth probe needs a token.
	before := s.seq.Load()
	if code, _ := raw(t, "HEAD", ts.URL+"/alerts/trigger?message=x", admin, "", nil); code != 405 {
		t.Fatalf("HEAD publish: %d", code)
	}
	if code, _ := raw(t, "PUT", ts.URL+"/alerts", admin, "secret", map[string]string{"Cache": "no"}); code != 400 {
		t.Fatalf("Cache: no: %d", code)
	}
	if code, _ := raw(t, "POST", ts.URL+"/", admin, `{"topic":"alerts","message":"secret","cache":"no"}`, nil); code != 400 {
		t.Fatalf("JSON cache no: %d", code)
	}
	if s.seq.Load() != before {
		t.Fatal("HEAD or Cache: no published")
	}
	if code, _ := raw(t, "GET", ts.URL+"/,/auth", "", "", nil); code != 401 {
		t.Fatalf("auth probe with no topics and no token: %d", code)
	}

	code, out := raw(t, "PUT", ts.URL+"/alerts", admin, "Disk 91%", map[string]string{"Title": "web-1", "Priority": "high",
		"Tags": "warning,disk", "X-Click": "https://example.com/d", "Actions": "view, Open, https://example.com/o; http, Hook, https://example.com/h"})
	m := ntfyDecode(t, out)
	if code != 200 || m.Event != "message" || m.Title != "web-1" || m.Message != "Disk 91%" || m.Priority != 4 ||
		len(m.Tags) != 2 || len(m.Actions) != 1 || m.Actions[0].URL != "https://example.com/o" || !strings.HasPrefix(m.ID, "s") {
		t.Fatalf("PUT publish: %d %s", code, out)
	}

	if _, out = raw(t, "GET", ts.URL+"/alerts/publish?message=hi&t=T&p=5", admin, "", nil); ntfyDecode(t, out).Priority != 5 {
		t.Fatalf("GET publish: %s", out)
	}
	code, out = raw(t, "POST", ts.URL+"/", admin, `{"topic":"alerts","message":"j","priority":2,"actions":[{"action":"view","label":"L","url":"https://example.com"}]}`, nil)
	if m := ntfyDecode(t, out); code != 200 || m.Message != "j" || m.Priority != 2 || len(m.Actions) != 1 {
		t.Fatalf("JSON publish past a root mount: %d %s", code, out)
	}
	if _, out = raw(t, "GET", ts.URL+"/", "", "", nil); out != "landing" {
		t.Fatalf("GET / should still reach the mount: %q", out)
	}

	for _, c := range []struct {
		name, tok, body string
		hdr             map[string]string
		want            int
	}{
		{"no token", "", "x", nil, 401},
		{"too big", admin, strings.Repeat("x", ntfyMaxBody+1), nil, 413},
		{"bad priority", admin, "x", map[string]string{"Priority": "loud"}, 400},
		{"bad delay", admin, "x", map[string]string{"Delay": "soonish"}, 400},
	} {
		if code, out := raw(t, "POST", ts.URL+"/alerts", c.tok, c.body, c.hdr); code != c.want {
			t.Errorf("%s: want %d, got %d %s", c.name, c.want, code, out)
		}
	}

	// X-Id: a second publish updates the same notification.
	_, first := raw(t, "POST", ts.URL+"/alerts", admin, "cpu 90", map[string]string{"X-Id": "cpu"})
	_, second := raw(t, "POST", ts.URL+"/alerts", admin, "cpu 95", map[string]string{"X-Id": "cpu"})
	if a, b := ntfyDecode(t, first), ntfyDecode(t, second); a.SequenceID != "cpu" || b.SequenceID != "cpu" || a.ID == b.ID {
		t.Fatalf("X-Id update: %s / %s", first, second)
	}
	// status resolved on the native API resolves it instead of creating one, then 404s.
	if code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "external_id": "cpu", "status": "resolved"}); code != 200 || n["state"] != Resolved {
		t.Fatalf("status resolved: %d %v", code, n)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "external_id": "cpu", "status": "resolved"}); code != 404 {
		t.Fatalf("resolve with nothing open: want 404, got %d", code)
	}

	// ntfy's clear and delete by sequence id: by external id, or by notification id.
	raw(t, "PUT", ts.URL+"/alerts/job-7", admin, "running", nil)
	if _, out := raw(t, "PUT", ts.URL+"/alerts/job-7/clear", admin, "", nil); ntfyDecode(t, out).Event != "message_clear" {
		t.Fatalf("clear: %s", out)
	}
	_, out = raw(t, "POST", ts.URL+"/alerts", admin, "no key", nil)
	id := ntfyDecode(t, out).SequenceID
	seq := s.seq.Load()
	if code, _ := raw(t, "DELETE", ts.URL+"/other/"+id, admin, "", nil); code != 404 {
		t.Fatalf("delete through the wrong topic: %d", code)
	}
	if _, out := raw(t, "GET", ts.URL+"/api/v1/notifications/"+id, admin, "", nil); !strings.Contains(out, `"state":"active"`) || s.seq.Load() != seq {
		t.Fatalf("wrong-topic delete changed the row: %s", out)
	}
	pub := mkToken(t, s, "cron", []string{"publish"}, nil) // resolve needs only publish, as in the native API
	if _, out := raw(t, "DELETE", ts.URL+"/alerts/"+id, pub, "", nil); ntfyDecode(t, out).Event != "message_delete" {
		t.Fatalf("delete: %s", out)
	}

	// Alertmanager: one notification per alert, keyed by fingerprint; resolved resolves.
	am := func(status string) string {
		return `{"status":"` + status + `","alerts":[{"status":"` + status + `","fingerprint":"fp1","labels":{"alertname":"HighCPU"},"annotations":{"summary":"CPU high"}}]}`
	}
	for _, c := range []struct{ status, want string }{
		{"firing", `{"published":1,"resolved":0}`},
		{"firing", `{"published":1,"resolved":0}`}, // repeat updates, does not stack
		{"resolved", `{"published":0,"resolved":1}`},
		{"resolved", `{"published":0,"resolved":0}`}, // already gone: fine
	} {
		if code, out := raw(t, "POST", ts.URL+"/alerts?template=alertmanager", admin, am(c.status), nil); code != 200 || strings.TrimSpace(out) != c.want {
			t.Fatalf("alertmanager %s: %d %s", c.status, code, out)
		}
	}
	if _, l := call(t, ts, "GET", "/api/v1/notifications?topic=alerts&q=CPU+high&state=all", admin, nil); len(l["notifications"].([]any)) != 1 {
		t.Fatalf("alertmanager stacked: %v", l)
	}
}

func TestNtfySubscribe(t *testing.T) {
	_, ts, admin := newTest(t)
	var ids []string
	for _, msg := range []string{"one", "two\nlines", "three"} {
		_, out := raw(t, "POST", ts.URL+"/alerts", admin, msg, nil)
		ids = append(ids, ntfyDecode(t, out).ID)
	}
	raw(t, "POST", ts.URL+"/other", admin, "elsewhere", nil)

	poll := func(path string) []string {
		t.Helper()
		code, out := raw(t, "GET", ts.URL+path, admin, "", nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, out)
		}
		return lines(out)
	}
	if l := poll("/alerts/json?poll=1"); len(l) != 3 || ntfyDecode(t, l[0]).Event != "message" || ntfyDecode(t, l[0]).ID != ids[0] {
		t.Fatalf("poll: %v", l)
	}
	if l := poll("/alerts,other/json?poll=1"); len(l) != 4 {
		t.Fatalf("poll two topics: %v", l)
	}
	if l := poll("/alerts/json?poll=1&since=" + ids[1]); len(l) != 1 || ntfyDecode(t, l[0]).Message != "three" {
		t.Fatalf("since id: %v", l)
	}
	if l := poll("/alerts/json?poll=1&since=latest"); len(l) != 1 || ntfyDecode(t, l[0]).Message != "three" {
		t.Fatalf("since latest: %v", l)
	}
	if l := poll("/alerts/json?poll=1&since=10m"); len(l) != 3 {
		t.Fatalf("since duration: %v", l)
	}
	if l := poll("/alerts/raw?poll=1"); strings.Join(l, "|") != "one|two lines|three" {
		t.Fatalf("raw: %v", l)
	}
	if code, _ := raw(t, "GET", ts.URL+"/alerts/json?poll=1&since=whenever", admin, "", nil); code != 400 {
		t.Fatalf("bad since: %d", code)
	}
	if code, _ := raw(t, "GET", ts.URL+"/alerts/json?poll=1", "", "", nil); code != 401 {
		t.Fatalf("no token: %d", code)
	}

	// Live: open first, then a publish, then a dismiss from the clrd app
	// arrives as message_delete for the same sequence id (clears the ntfy app too).
	req, _ := http.NewRequest("GET", ts.URL+"/alerts/json", nil)
	req.SetBasicAuth("", admin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(res.Body)
	next := func() ntfyMsg { t.Helper(); sc.Scan(); return ntfyDecode(t, sc.Text()) }
	if m := next(); m.Event != "open" || m.Topic != "alerts" {
		t.Fatalf("want open, got %+v", m)
	}
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "live"})
	if m := next(); m.Event != "message" || m.Message != "live" || m.SequenceID != n["id"] {
		t.Fatalf("live message: %+v", m)
	}
	call(t, ts, "POST", "/api/v1/notifications/"+n["id"].(string)+"/dismiss", admin, nil)
	if m := next(); m.Event != "message_delete" || m.SequenceID != n["id"] {
		t.Fatalf("dismiss: %+v", m)
	}

	// SSE: open has an event line, messages are data only.
	req, _ = http.NewRequest("GET", ts.URL+"/alerts/sse", nil)
	req.SetBasicAuth("", admin)
	sres, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sres.Body.Close()
	ssc := bufio.NewScanner(sres.Body)
	// sse reads one SSE frame: its event line (if any) and its data.
	sse := func() (string, ntfyMsg) {
		t.Helper()
		event := ""
		for ssc.Scan() {
			if l := ssc.Text(); strings.HasPrefix(l, "event: ") {
				event = l
			} else if d, ok := strings.CutPrefix(l, "data: "); ok {
				return event, ntfyDecode(t, d)
			}
		}
		t.Fatalf("sse stream ended: %v", ssc.Err())
		return "", ntfyMsg{}
	}
	if ev, m := sse(); ev != "event: open" || m.Event != "open" {
		t.Fatalf("sse open: %q %+v", ev, m)
	}
	_, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "over sse"})
	if ev, m := sse(); ev != "" || m.Event != "message" || m.Message != "over sse" {
		t.Fatalf("sse message: %q %+v", ev, m)
	}
	call(t, ts, "POST", "/api/v1/notifications/"+n["id"].(string)+"/ack", admin, nil)
	if ev, m := sse(); ev != "" || m.Event != "message_clear" {
		t.Fatalf("sse clear: %q %+v", ev, m)
	}
	call(t, ts, "POST", "/api/v1/notifications/"+n["id"].(string)+"/dismiss", admin, nil)
	if ev, m := sse(); ev != "" || m.Event != "message_delete" {
		t.Fatalf("sse delete: %q %+v", ev, m)
	}

	// WebSocket: open, then a message.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/alerts/ws?auth="+basicAuthParam(admin), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	read := func() ntfyMsg {
		t.Helper()
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ntfyDecode(t, string(b))
	}
	if m := read(); m.Event != "open" {
		t.Fatalf("ws open: %+v", m)
	}
	raw(t, "POST", ts.URL+"/alerts", admin, "over ws", nil)
	if m := read(); m.Message != "over ws" {
		t.Fatalf("ws message: %+v", m)
	}
}

// ntfy's subscribe filters, each one alone excluding the same message.
func TestNtfyFilters(t *testing.T) {
	_, ts, admin := newTest(t)
	_, out := raw(t, "POST", ts.URL+"/f", admin, "keep", map[string]string{"Title": "disk", "Priority": "high", "Tags": "a,b"})
	keep := ntfyDecode(t, out).ID
	raw(t, "POST", ts.URL+"/f", admin, "drop", map[string]string{"Title": "cpu", "Priority": "low", "Tags": "a"})
	for _, q := range []string{"id=" + keep, "message=keep", "m=keep", "title=disk", "t=disk", "priority=high", "p=4,5", "prio=max,high",
		"tags=a,b", "ta=b", "tag=b"} {
		code, out := raw(t, "GET", ts.URL+"/f/json?poll=1&"+q, admin, "", nil)
		if l := lines(out); code != 200 || len(l) != 1 || ntfyDecode(t, l[0]).Message != "keep" {
			t.Errorf("%s: %d %v", q, code, l)
		}
	}
	if _, out := raw(t, "GET", ts.URL+"/f/json?poll=1", admin, "", nil); len(lines(out)) != 2 {
		t.Fatalf("no filter: %v", lines(out))
	}
	if _, out := raw(t, "GET", ts.URL+"/f/json?poll=1", admin, "", map[string]string{"X-Title": "cpu"}); len(lines(out)) != 1 || ntfyDecode(t, lines(out)[0]).Message != "drop" {
		t.Errorf("filter by header: %v", lines(out))
	}
	if code, _ := raw(t, "GET", ts.URL+"/f/json?poll=1&p=7", admin, "", nil); code != 400 {
		t.Errorf("bad priority filter: %d", code)
	}
}

// The ntfy routes answer any origin, errors included, and a preflight needs
// no token. The native API does not.
func TestNtfyCORS(t *testing.T) {
	s, ts, _ := newTest(t)
	s.Mount("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(405) }))
	do := func(method, path string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		req.Header.Set("Origin", "https://elsewhere.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := do("GET", "/alerts/json?poll=1"); res.StatusCode != 401 || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("subscribe without token: %d, allow-origin %q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
	}
	res := do("OPTIONS", "/alerts")
	if res.StatusCode != 204 || res.Header.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("preflight: %d %v", res.StatusCode, res.Header)
	}
	if res := do("OPTIONS", "/api/v1/notifications"); res.StatusCode == 204 || res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("native API preflight allowed: %d", res.StatusCode)
	}
	if res := do("OPTIONS", "/mcp"); res.StatusCode != 405 {
		t.Errorf("preflight took a mounted path: %d", res.StatusCode)
	}
}

// An external_id with a slash round-trips when the SDKs escape it as %2F.
func TestExternalIDWithSlash(t *testing.T) {
	_, ts, admin := newTest(t)
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ci", "external_id": "repo/main", "message": "red"})
	if code, n := call(t, ts, "POST", "/ci/repo%2Fmain/resolve", admin, nil); code != 200 || n["external_id"] != "repo/main" || n["state"] != Resolved {
		t.Fatalf("resolve by escaped key: %d %v", code, n)
	}
	raw(t, "PUT", ts.URL+"/ci/repo%2Fdev", admin, "red", nil)
	if _, out := raw(t, "DELETE", ts.URL+"/ci/repo%2Fdev", admin, "", nil); ntfyDecode(t, out).SequenceID != "repo/dev" {
		t.Fatalf("ntfy delete by escaped key: %s", out)
	}
}

// Review fixes on PR #11: an empty topic list, a lowercase auth scheme and a
// delay past ntfy's 3-day limit.
func TestNtfyEdgeInputs(t *testing.T) {
	_, ts, admin := newTest(t)
	if code, body := raw(t, "GET", ts.URL+"/,/json?poll=1", "", "", nil); code != 400 {
		t.Fatalf("comma-only topics: %d %s", code, body)
	}
	if code, body := raw(t, "POST", ts.URL+"/edge", "", "hi", map[string]string{"Authorization": "bearer " + admin}); code != 200 {
		t.Fatalf("lowercase bearer: %d %s", code, body)
	}
	if code, body := raw(t, "POST", ts.URL+"/edge", admin, "hi", map[string]string{"X-Delay": "253402300800"}); code != 400 {
		t.Fatalf("delay in year 10000: %d %s", code, body)
	}
	if code, body := raw(t, "POST", ts.URL+"/edge", admin, "hi", map[string]string{"X-Delay": "4d"}); code != 400 {
		t.Fatalf("delay of 4 days: %d %s", code, body)
	}
}
