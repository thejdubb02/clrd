package server

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
)

// A live-only message (ntfy's Cache: no, native live) reaches a stream that
// is open now and is stored nowhere (#400).
func TestLiveOnly(t *testing.T) {
	s, ts, admin := newTest(t)
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "kept"})

	req, _ := http.NewRequest("GET", ts.URL+"/alerts/json", nil)
	req.SetBasicAuth("", admin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	next := func() ntfyMsg { t.Helper(); sc.Scan(); return ntfyDecode(t, sc.Text()) }
	if m := next(); m.Event != "open" {
		t.Fatalf("want open, got %+v", m)
	}
	mcp, cancel := s.Subscribe() // MCP's wait_for_reply never sees one
	defer cancel()

	before := s.CurrentSeq()
	_, out := raw(t, "POST", ts.URL+"/alerts", admin, "ding", map[string]string{"Cache": "no", "Title": "door"})
	reply := ntfyDecode(t, out)
	m := next()
	if m.Event != "message" || m.Message != "ding" || m.Title != "door" || m.ID != reply.ID || !strings.HasPrefix(m.ID, "l") || len(m.ID) != 12 {
		t.Fatalf("live message %+v, reply %+v", m, reply)
	}
	code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "dong", "live": true})
	if m2 := next(); code != 200 || m2.Message != "dong" || m2.ID == m.ID || m2.ID != n["id"] || n["live"] != true {
		t.Fatalf("native live %d %v, got %+v", code, n, m2)
	}
	if s.CurrentSeq() != before {
		t.Fatal("a live message moved the log")
	}
	select {
	case e := <-mcp:
		t.Fatalf("MCP subscriber got %+v", e.Notification)
	default:
	}

	// Nothing kept: not in a poll, not in the native list.
	if _, out := raw(t, "GET", ts.URL+"/alerts/json?poll=1&since=all", admin, "", nil); strings.Contains(out, "ding") || !strings.Contains(out, "kept") {
		t.Fatalf("poll: %s", out)
	}
	if _, l := call(t, ts, "GET", "/api/v1/notifications?state=all", admin, nil); len(l["notifications"].([]any)) != 1 {
		t.Fatalf("list: %v", l)
	}
	// since=<live id> replays only what was stored after it.
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "after"})
	if _, out := raw(t, "GET", ts.URL+"/alerts/json?poll=1&since="+m.ID, admin, "", nil); len(lines(out)) != 1 || ntfyDecode(t, out).Message != "after" {
		t.Fatalf("since live id: %s", out)
	}

	// Not kept, so nothing that needs keeping.
	for _, h := range []map[string]string{{"Cache": "no", "Delay": "1h"}, {"Cache": "no", "Id": "k"}, {"Cache": "no", "Filename": "a.txt"}} {
		if code, out := raw(t, "PUT", ts.URL+"/alerts", admin, "x", h); code != 400 {
			t.Fatalf("%v: %d %s", h, code, out)
		}
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "x", "live": true, "repeat": "5m"}); code != 400 {
		t.Fatalf("live repeat: %d", code)
	}
	if s.CurrentSeq() != before+1 {
		t.Fatal("a refused live message was stored")
	}
}

func TestLiveID(t *testing.T) {
	for _, seq := range []int64{0, 1, 35, 36*36*36*36*36*36*36 - 1} {
		id := liveID(seq, 1<<31)
		if got, ok := liveSeq(id); !ok || got != seq || len(id) != 12 {
			t.Fatalf("%d: %q -> %d %v", seq, id, got, ok)
		}
	}
	if liveID(5, 1) == liveID(5, 2) {
		t.Fatal("same id twice")
	}
	s, ts, admin := newTest(t) // an imported ntfy id that looks live is still the imported one
	for range 3 {
		call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "a", "message": "m"})
	}
	if _, err := s.db.Exec(`INSERT INTO ntfy_mids (mid, seq) VALUES ('l0000000wxyz', 2)`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ntfySince(t.Context(), "l0000000wxyz", nil); err != nil || got != 2 {
		t.Fatalf("imported l id: %d %v", got, err)
	}
	if _, ok := liveSeq("s00000000001"); ok {
		t.Fatal("a stored id read as live")
	}
}
