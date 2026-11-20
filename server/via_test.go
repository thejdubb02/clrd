package server

import (
	"encoding/json"
	"testing"
)

// Every state change records how it was made (#267), and a missing or bad via never blocks the action.
func TestStateEventsRecordVia(t *testing.T) {
	s, ts, admin := newTest(t)
	lastVia := func() string {
		t.Helper()
		var raw string
		if err := s.db.QueryRow(`SELECT data FROM events ORDER BY seq DESC LIMIT 1`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var e Event
		if err := json.Unmarshal([]byte(raw), &e); err != nil || e.Type != EventState {
			t.Fatalf("last event: %v %s", err, raw)
		}
		return e.Via
	}
	pub := func(title string) string {
		_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "title": title})
		return n["id"].(string)
	}
	a, b, c := pub("a"), pub("b"), pub("c")
	act := func(id, action string, body any, wantVia string) {
		t.Helper()
		if code, n := call(t, ts, "POST", "/api/v1/notifications/"+id+"/"+action, admin, body); code != 200 {
			t.Fatalf("%s %v: %d %v", action, body, code, n)
		}
		if v := lastVia(); v != wantVia {
			t.Fatalf("%s %v: via %q, want %q", action, body, v, wantVia)
		}
	}
	act(a, "dismiss", map[string]string{"via": "tray_cleared"}, "tray_cleared")
	act(b, "ack", nil, "api") // an older client sends no body
	act(b, "snooze", map[string]string{"for": "1h", "via": "app"}, "app")
	act(b, "reopen", "junk", "api")                                  // not an object: still done
	act(b, "dismiss", map[string]string{"via": "Not A Via!"}, "api") // malformed label is not stored

	if code, _ := call(t, ts, "POST", "/api/v1/notifications/"+c+"/snooze", admin, nil); code != 400 {
		t.Fatalf("snooze with no body: want 400, got %d", code)
	}
	if code, n := call(t, ts, "POST", "/api/v1/notifications/clear", admin, map[string]any{"ids": []string{c}, "via": "web_bulk"}); code != 200 || n["cleared"] != 1.0 {
		t.Fatalf("clear: %d %v", code, n)
	}
	if v := lastVia(); v != "web_bulk" {
		t.Fatalf("clear via %q", v)
	}
}
