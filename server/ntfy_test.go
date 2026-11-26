package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	return send(t, req)
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
	if code, _ := raw(t, "PUT", ts.URL+"/alerts", admin, "secret", map[string]string{"Cache": "no"}); code != 200 {
		t.Fatalf("Cache: no: %d", code)
	}
	if code, _ := raw(t, "POST", ts.URL+"/", admin, `{"topic":"alerts","message":"secret","cache":"no"}`, nil); code != 200 {
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

	// RFC 2047 encoded words, as ntfy's clients send non-ASCII headers (#842).
	_, out = raw(t, "PUT", ts.URL+"/alerts", admin, "", map[string]string{"Title": "=?UTF-8?B?R3LDvMOfZSDwn5Sl?=",
		"X-Tags": "plain,=?UTF-8?Q?caf=C3=A9?=", "Message": "=?utf-8?q?h=C3=A9llo?="})
	if m := ntfyDecode(t, out); m.Title != "Grüße 🔥" || len(m.Tags) != 2 || m.Tags[0] != "plain" || m.Tags[1] != "café" ||
		m.Message != "héllo" {
		t.Fatalf("encoded headers: %s", out)
	}
	if _, out = raw(t, "GET", ts.URL+"/alerts/publish?message=hi&t=T&p=5", admin, "", nil); ntfyDecode(t, out).Priority != 5 {
		t.Fatalf("GET publish: %s", out)
	}
	if _, out = raw(t, "POST", ts.URL+"/alerts?priority=low", admin, "hi", map[string]string{"Prio": "high"}); ntfyDecode(t, out).Priority != 4 {
		t.Fatalf("a header alias beats the query, as in ntfy (#172): %s", out)
	}
	code, out = raw(t, "POST", ts.URL+"/", admin, `{"topic":"alerts","message":"j","priority":2,"actions":[{"action":"view","label":"L","url":"https://example.com"}]}`, nil)
	if m := ntfyDecode(t, out); code != 200 || m.Message != "j" || m.Priority != 2 || len(m.Actions) != 1 {
		t.Fatalf("JSON publish past a root mount: %d %s", code, out)
	}
	code, out = raw(t, "POST", ts.URL+"/", admin, `{"topic":"alerts","message":"a","title":"json wins"}`,
		map[string]string{"X-Priority": "high", "X-Tags": "warn", "X-Click": "https://example.com/c", "X-Title": "header"})
	if m := ntfyDecode(t, out); code != 200 || m.Priority != 4 || len(m.Tags) != 1 || m.Tags[0] != "warn" ||
		m.Click != "https://example.com/c" || m.Title != "json wins" {
		t.Fatalf("JSON publish with header options (Apprise, #130): %d %s", code, out)
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
	// Grafana sends the same alerts[] shape (issue #7).
	for _, tpl := range []string{"alertmanager", "grafana"} {
		for _, c := range []struct{ status, want string }{
			{"firing", `{"published":1,"resolved":0}`},
			{"firing", `{"published":1,"resolved":0}`}, // repeat updates, does not stack
			{"resolved", `{"published":0,"resolved":1}`},
			{"resolved", `{"published":0,"resolved":0}`}, // already gone: fine
		} {
			if code, out := raw(t, "POST", ts.URL+"/alerts-"+tpl+"?template="+tpl, admin, am(c.status), nil); code != 200 || strings.TrimSpace(out) != c.want {
				t.Fatalf("%s %s: %d %s", tpl, c.status, code, out)
			}
		}
		if _, l := call(t, ts, "GET", "/api/v1/notifications?topic=alerts-"+tpl+"&q=CPU+high&state=all", admin, nil); len(l["notifications"].([]any)) != 1 {
			t.Fatalf("%s stacked: %v", tpl, l)
		}
	}
}

// A delayed message: the publish reply and ?scheduled=1 show it as a
// message at its delivery time; a plain poll does not.
func TestNtfyScheduled(t *testing.T) {
	s, ts, admin := newTest(t)
	at := time.Now().Add(time.Hour).Unix()
	pub := mkToken(t, s, "pub", []string{ScopePublish}, nil)
	for topic, tok := range map[string]string{"later": admin, "other": pub} { // a publish-only token never sees state, but its own delay
		_, out := raw(t, "POST", ts.URL+"/"+topic, tok, "soon", map[string]string{"X-Delay": "1h"})
		if m := ntfyDecode(t, out); m.Event != "message" || m.Message != "soon" || m.Time < at-5 {
			t.Fatalf("delayed reply: %s", out)
		}
	}

	// Live with scheduled=1: a new delayed message does not show until it fires.
	req, _ := http.NewRequest("GET", ts.URL+"/live/json?scheduled=1", nil)
	req.SetBasicAuth("", admin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	if sc.Scan(); ntfyDecode(t, sc.Text()).Event != "open" {
		t.Fatalf("want open: %s", sc.Text())
	}
	raw(t, "POST", ts.URL+"/live", admin, "later still", map[string]string{"X-Delay": "2h"})
	raw(t, "POST", ts.URL+"/live", admin, "now", nil)
	for sc.Scan() {
		if m := ntfyDecode(t, sc.Text()); m.Event == "message" {
			if m.Message != "now" {
				t.Fatalf("live got %+v", m)
			}
			break
		}
	}
	raw(t, "POST", ts.URL+"/later", admin, "now", nil)
	msgs := func(q string) (got []string) {
		_, out := raw(t, "GET", ts.URL+"/later/json?poll=1"+q, admin, "", nil)
		for _, l := range lines(out) {
			if m := ntfyDecode(t, l); m.Event == "message" {
				got = append(got, m.Message)
			}
		}
		return got
	}
	if got := msgs(""); strings.Join(got, ",") != "now" {
		t.Fatalf("plain poll: %v", got)
	}
	for _, q := range []string{"&scheduled=1", "&sched=yes"} {
		if got := msgs(q); strings.Join(got, ",") != "soon,now" {
			t.Fatalf("%s: %v", q, got)
		}
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
	// A browser's RFC 9218 Priority header is not a filter, and does not hide ?priority=.
	if code, out := raw(t, "GET", ts.URL+"/f/json?poll=1", admin, "", map[string]string{"Priority": "u=3, i"}); code != 200 || len(lines(out)) != 2 {
		t.Errorf("browser Priority header: %d %v", code, lines(out))
	}
	if code, out := raw(t, "GET", ts.URL+"/f/json?poll=1&priority=high", admin, "", map[string]string{"Priority": "u=1"}); code != 200 || len(lines(out)) != 1 || ntfyDecode(t, lines(out)[0]).Message != "keep" {
		t.Errorf("browser Priority header with ?priority=high: %d %v", code, lines(out))
	}
	if code, _ := raw(t, "POST", ts.URL+"/f", admin, "from a browser", map[string]string{"Priority": "u=1, i"}); code != 200 {
		t.Errorf("publish with browser Priority header: %d", code)
	}
	if code, _ := raw(t, "GET", ts.URL+"/f/json?poll=1&p=7", admin, "", nil); code != 400 {
		t.Errorf("bad priority filter: %d", code)
	}
}

// A page on another site is refused on the ntfy routes (requests, preflight
// and WebSocket) until an admin allows its origin; requests with no Origin
// (phones, scripts) and clrd's own pages pass. The native API never answers
// CORS (#374).
func TestNtfyCORS(t *testing.T) {
	s, ts, admin := newTest(t)
	s.Mount("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(405) }))
	do := func(method, path, origin, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("Access-Control-Request-Method", "POST")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	acao := func(r *http.Response) string { return r.Header.Get("Access-Control-Allow-Origin") }
	// Anonymous publish on, as a LAN server might have it.
	if code, _ := call(t, ts, "PUT", "/api/v1/ntfy/anonymous", admin, map[string]any{"grants": []Grant{{Topic: "*", Scopes: []string{ScopeRead, ScopePublish}}}}); code != 200 {
		t.Fatalf("anonymous on: %d", code)
	}
	evil := "https://elsewhere.example"
	before := s.seq.Load()
	if res := do("POST", "/alerts", evil, "from a page"); res.StatusCode != 403 || acao(res) != "" {
		t.Errorf("default, foreign page publish: %d %q", res.StatusCode, acao(res))
	}
	if res := do("POST", "/message", evil, `{"message":"m"}`); res.StatusCode != 403 {
		t.Errorf("default, foreign page on Gotify's publish: %d", res.StatusCode)
	}
	if s.seq.Load() != before {
		t.Fatal("a refused foreign page published")
	}
	if res := do("GET", "/alerts/json?poll=1", evil, ""); res.StatusCode != 403 {
		t.Errorf("default, foreign page read: %d", res.StatusCode)
	}
	if res := do("OPTIONS", "/alerts", evil, ""); res.StatusCode != 403 || acao(res) != "" {
		t.Errorf("default, foreign preflight: %d %q", res.StatusCode, acao(res))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/alerts/ws"
	if c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {evil}}}); err == nil {
		c.CloseNow()
		t.Error("default, foreign page opened a WebSocket")
	}
	// No Origin (a phone, curl) and clrd's own origin work.
	// Vary even with no Origin, so a cache never hands this reply to a page.
	if res := do("POST", "/alerts", "", "from a script"); res.StatusCode != 200 || res.Header.Get("Vary") != "Origin" {
		t.Errorf("no origin: %d %q", res.StatusCode, res.Header.Get("Vary"))
	}
	if res := do("POST", "/alerts", ts.URL, "from clrd's page"); res.StatusCode != 200 {
		t.Errorf("same origin: %d", res.StatusCode)
	}

	// Allowing one site lets that site in, echoed, and no other.
	if code, m := call(t, ts, "PUT", "/api/v1/ntfy/origins", admin, map[string]any{"origins": []string{"https://Ntfy.sh/", "https://ntfy.sh"}}); code != 200 || len(m["origins"].([]any)) != 1 || m["origins"].([]any)[0] != "https://ntfy.sh" {
		t.Fatalf("set origins: %d %v", code, m)
	}
	if res := do("GET", "/alerts/json?poll=1", "https://ntfy.sh", ""); res.StatusCode != 200 || acao(res) != "https://ntfy.sh" || res.Header.Get("Vary") != "Origin" {
		t.Errorf("allowed read: %d %q %q", res.StatusCode, acao(res), res.Header.Get("Vary"))
	}
	res := do("OPTIONS", "/alerts", "https://ntfy.sh", "")
	if res.StatusCode != 204 || acao(res) != "https://ntfy.sh" || !strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("allowed preflight: %d %v", res.StatusCode, res.Header)
	}
	if c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://ntfy.sh"}}}); err != nil {
		t.Errorf("allowed WebSocket: %v", err)
	} else {
		c.CloseNow()
	}
	if res := do("POST", "/alerts", evil, "x"); res.StatusCode != 403 {
		t.Errorf("other site with a list: %d", res.StatusCode)
	}
	// "*" is ntfy's own behaviour, on request.
	call(t, ts, "PUT", "/api/v1/ntfy/origins", admin, map[string]any{"origins": []string{"*"}})
	if res := do("GET", "/alerts/json?poll=1", evil, ""); res.StatusCode != 200 || acao(res) != "*" {
		t.Errorf("any origin: %d %q", res.StatusCode, acao(res))
	}

	// Only a full admin sets it, and only to origins.
	for _, bad := range []map[string]any{{}, {"origins": []string{"ntfy.sh"}}, {"origins": []string{"https://ntfy.sh/app"}}, {"origins": []string{"ftp://x.example"}}} {
		if code, _ := call(t, ts, "PUT", "/api/v1/ntfy/origins", admin, bad); code != 400 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	pub := mkToken(t, s, "p", []string{ScopePublish}, nil)
	if code, _ := call(t, ts, "PUT", "/api/v1/ntfy/origins", pub, map[string]any{"origins": []string{}}); code != 403 {
		t.Errorf("non-admin set: %d", code)
	}
	if res := do("OPTIONS", "/api/v1/notifications", evil, ""); res.StatusCode == 204 || acao(res) != "" {
		t.Errorf("native API preflight allowed: %d", res.StatusCode)
	}
	if res := do("OPTIONS", "/mcp", evil, ""); res.StatusCode != 405 {
		t.Errorf("preflight took a mounted path: %d", res.StatusCode)
	}
	// The list survives a restart: read back from the database.
	s.origins.Store(nil)
	if code, m := call(t, ts, "GET", "/api/v1/ntfy/origins", admin, nil); code != 200 || m["origins"].([]any)[0] != "*" {
		t.Errorf("stored list: %d %v", code, m)
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

func TestNtfyMixedPublishOptions(t *testing.T) {
	_, ts, admin := newTest(t)
	const topic = "mixed"
	at := time.Now().Add(time.Hour).Unix()
	t.Run("header and query fallback", func(t *testing.T) {
		code, out := raw(t, "POST", fmt.Sprintf("%s/?in=%d&sid=mixed-id&md=no&filename=report.txt", ts.URL, at),
			admin, `{"topic":"mixed"}`, map[string]string{
				"Message": "from header",
				"Actions": "view, Open, https://example.com/report",
				"Attach":  "https://example.com/file",
			})
		m := ntfyDecode(t, out)
		if code != 200 || m.Topic != topic || m.Message != "from header" || m.Time != at || m.SequenceID != "mixed-id" ||
			m.ContentType != "" || len(m.Actions) != 1 || m.Actions[0].URL != "https://example.com/report" ||
			m.Attachment == nil || m.Attachment.Name != "report.txt" || m.Attachment.URL != "https://example.com/file" {
			t.Fatalf("mixed publish: %d %s", code, out)
		}
	})
	t.Run("JSON wins", func(t *testing.T) {
		body := fmt.Sprintf(`{"topic":"mixed","message":"from JSON","delay":"%d","sequence_id":"json-id","markdown":false,"tags":[],"actions":[],"cache":"yes"}`, at)
		code, out := raw(t, "POST", ts.URL+"/?delay=invalid&sid=query-id&md=yes&tags=query",
			admin, body, map[string]string{"Message": "from header", "Actions": "[invalid", "Cache": "no"})
		m := ntfyDecode(t, out)
		if code != 200 || m.Message != "from JSON" || m.Time != at || m.SequenceID != "json-id" ||
			m.ContentType != "" || len(m.Actions) != 0 || len(m.Tags) != 0 {
			t.Fatalf("JSON precedence: %d %s", code, out)
		}
	})
	t.Run("Markdown media type", func(t *testing.T) {
		code, out := raw(t, "PUT", ts.URL+"/mixed", admin, "**bold**",
			map[string]string{"Content-Type": "text/markdown; charset=utf-8", "Markdown": "no"})
		if m := ntfyDecode(t, out); code != 200 || m.ContentType != "text/markdown" {
			t.Fatalf("Markdown media type: %d %s", code, out)
		}
	})
	t.Run("invalid fallback is rejected", func(t *testing.T) {
		for _, h := range []map[string]string{{"Actions": "[invalid"}, {"Delay": "later-ish"}} {
			if code, out := raw(t, "POST", ts.URL+"/", admin, `{"topic":"mixed","message":"hello"}`, h); code != 400 {
				t.Errorf("invalid option %v: %d %s", h, code, out)
			}
		}
	})
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

// ntfy's schedule forms (#841), on a fixed clock: Wednesday 2026-11-25 14:30.
func TestDelayTime(t *testing.T) {
	zone := time.FixedZone("X", -5*3600)
	now := time.Date(2026, 11, 25, 14, 30, 20, 0, zone)
	day := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, zone) }
	for v, want := range map[string]time.Time{
		"10am": day(26, 10, 0), "8:30pm": day(25, 20, 30), "17:05": day(25, 17, 5), "noon": day(26, 12, 0),
		"tomorrow, 3pm": day(26, 15, 0), "3 PM tomorrow": day(26, 15, 0), "Tuesday, 7am": time.Date(2026, 12, 1, 7, 0, 0, 0, zone),
		"wed 2pm": time.Date(2026, 12, 2, 14, 0, 0, 0, zone), "Wednesday 3pm": day(25, 15, 0),
		"2 days": now.Add(48 * time.Hour), "30min": now.Add(30 * time.Minute), "90 minutes": now.Add(90 * time.Minute), "30m": now.Add(30 * time.Minute),
		"2026-11-26T10:00:00Z": time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC), "2026-11-26T09:00:00+09:00": day(25, 19, 0),
	} {
		if got, err := delayTime(v, now); err != nil || !got.Equal(want) {
			t.Errorf("%q: got %v %v, want %v", v, got, err, want)
		}
	}
	for _, v := range []string{"soonish", "today 9am", "2026-11-25T10:00:00Z", "tomorrow tomorrow", "10am 11am", "next tuesday", "2 fortnights", "25pm"} {
		if got, err := delayTime(v, now); err == nil {
			t.Errorf("%q: want an error, got %v", v, got)
		}
	}
}

// A time of day is read in the sender's timezone, from a header or JSON; a
// bad timezone is refused only when there is a delay to read it for (#841).
func TestNtfyDelayTimezone(t *testing.T) {
	_, ts, admin := newTest(t)
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("no timezone database")
	}
	for _, c := range []struct {
		path, body string
		hdr        map[string]string
	}{
		{"/tz", "m", map[string]string{"At": "10am", "X-Timezone": "Asia/Tokyo"}},
		{"/?timezone=Mars/Base", `{"topic":"tz","message":"m","delay":"10am","timezone":"Asia/Tokyo"}`, map[string]string{"Timezone": "Mars/Base"}},
		{"/?at=10am&timezone=Mars/Base", `{"topic":"tz","message":"m"}`, map[string]string{"Timezone": "Asia/Tokyo"}},
		{"/?at=10am&timezone=Asia/Tokyo", `{"topic":"tz","message":"m"}`, nil},
	} {
		code, out := raw(t, "POST", ts.URL+c.path, admin, c.body, c.hdr)
		at := time.Unix(ntfyDecode(t, out).Time, 0).In(tokyo)
		if ahead := time.Until(at); code != 200 || at.Hour() != 10 || at.Minute() != 0 || ahead <= 0 || ahead > 24*time.Hour {
			t.Errorf("%s: 10am in Tokyo came back as %v: %d %s", c.path, at, code, out)
		}
	}
	if code, out := raw(t, "POST", ts.URL+"/tz", admin, "m", map[string]string{"At": "10am", "Timezone": "Mars/Base"}); code != 400 ||
		!strings.Contains(out, "timezone") {
		t.Errorf("bad timezone with a delay: %d %s", code, out)
	}
	if code, out := raw(t, "POST", ts.URL+"/tz", admin, "m", map[string]string{"Timezone": "Mars/Base"}); code != 200 {
		t.Errorf("bad timezone without a delay: %d %s", code, out)
	}
}

// ntfy's inline templates (#272): ?tpl=yes renders m, t and p over the JSON body.
func TestNtfyInlineTemplate(t *testing.T) {
	_, ts, admin := newTest(t)
	mm := "?tpl=yes&markdown=yes&m={{.text}}&p={{if%20.channel}}2{{else}}4{{end}}&auth=" + basicAuthParam(admin)
	code, out := raw(t, "POST", ts.URL+"/wsg"+mm, "", `{"text": "disk full"}`,
		map[string]string{"Content-Type": "application/json"})
	if m := ntfyDecode(t, out); code != 200 || m.Message != "disk full" || m.Priority != 4 {
		t.Fatalf("Mattermost-style templated publish: %d %s", code, out)
	}
	if _, out = raw(t, "POST", ts.URL+"/wsg"+mm, "", `{"text": "x", "channel": "c"}`, nil); ntfyDecode(t, out).Priority != 2 {
		t.Fatalf("templated priority: %s", out)
	}
	code, out = raw(t, "PUT", ts.URL+"/wsg", admin, `{"a": {"b": "deep"}, "n": 5}`,
		map[string]string{"X-Template": "1", "X-Title": `{{.a.b}}`, "X-Message": `n={{.n}} {{.missing}}`})
	if m := ntfyDecode(t, out); code != 200 || m.Title != "deep" || m.Message != "n=5" {
		t.Fatalf("X-Template header form: %d %s", code, out)
	}
	if code, out = raw(t, "POST", ts.URL+"/wsg?template=yes&m={{.text}}", admin, `{"text": "t"}`, nil); code != 200 || ntfyDecode(t, out).Message != "t" {
		t.Fatalf("?template=yes is ntfy's inline form, not a saved template named yes: %d %s", code, out)
	}
	if code, out = raw(t, "POST", ts.URL+"/wsg?tpl=yes&m={{.text}}", admin, `not json`, nil); code != 400 {
		t.Fatalf("non-JSON body with tpl=yes: %d %s", code, out)
	}
	if code, out = raw(t, "POST", ts.URL+"/wsg?tpl=yes&m={{range .}}x{{end}}", admin, `[1]`, nil); code != 400 {
		t.Fatalf("range must be refused as in saved templates: %d %s", code, out)
	}
}

// #443: since= older than the log makes a ntfy stream start over from the
// oldest event left, on the same subscription, so a live-only message sent
// while it does still arrives.
func TestNtfyRestartKeepsLive(t *testing.T) {
	s, ts, admin := newTest(t)
	for _, m := range []string{"gone", "kept"} {
		raw(t, "PUT", ts.URL+"/alerts", admin, m, nil)
	}
	if _, err := s.db.Exec(`DELETE FROM events WHERE seq <= 1`); err != nil {
		t.Fatal(err)
	}
	s.pruned.Store(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []string
	err := s.follow(ctx, s.lookupToken(ctx, admin), 0, []string{"alerts"}, nil, func(msg []byte, seq int64) error {
		switch {
		case seq < 0 && len(got) == 0:
			got = append(got, "reset")
			raw(t, "PUT", ts.URL+"/alerts", admin, "live", map[string]string{"Cache": "no"})
			return errPruned
		case strings.Contains(string(msg), `"type":"ready"`):
			got = append(got, "ready")
		default:
			var e Event
			json.Unmarshal(msg, &e)
			if got = append(got, e.Notification.Message); e.Notification.Live {
				cancel()
			}
		}
		return nil
	}, func() error { return nil })
	if err != nil || strings.Join(got, " ") != "reset kept ready live" {
		t.Fatalf("got %q, %v", got, err)
	}
}
