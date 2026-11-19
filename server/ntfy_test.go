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
	if code, _ := raw(t, "DELETE", ts.URL+"/other/"+id, admin, "", nil); code != 404 {
		t.Fatalf("delete through the wrong topic: %d", code)
	}
	if _, out := raw(t, "DELETE", ts.URL+"/alerts/"+id, admin, "", nil); ntfyDecode(t, out).Event != "message_delete" {
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
	ssc.Scan()
	if ssc.Text() != "event: open" {
		t.Fatalf("sse first line %q", ssc.Text())
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
