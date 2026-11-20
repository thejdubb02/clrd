package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestKeepCleared: cleared messages go by the keep_cleared_days setting,
// counted from when they were cleared; open and snoozed ones never go.
func TestKeepCleared(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	day := 24 * time.Hour
	mk := func(action string, body any, age time.Duration) string {
		t.Helper()
		_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "m"})
		id := n["id"].(string)
		if action != "" {
			if code, out := call(t, ts, "POST", "/api/v1/notifications/"+id+"/"+action, admin, body); code != 200 {
				t.Fatalf("%s: %d %v", action, code, out)
			}
		}
		if _, err := s.db.Exec(`UPDATE notifications SET updated_at = ? WHERE id = ?`, ms(time.Now().Add(-age)), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	six := mk("dismiss", nil, 6*day)
	four := mk("dismiss", nil, 4*day)
	open := mk("", nil, 60*day)
	snoozed := mk("snooze", map[string]string{"for": "1h"}, 60*day)
	ten := mk("resolve", nil, 10*day)
	exists := func(id string) bool {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE id = ?`, id).Scan(&n)
		return n == 1
	}

	// No setting yet: CLRD_RETENTION decides, and reads show it in days.
	if _, st := call(t, ts, "GET", "/api/v1/settings", admin, nil); st["keep_cleared_days"] != 30.0 {
		t.Fatalf("default keep_cleared_days: %v", st["keep_cleared_days"])
	}
	s.opt.Retention = 7 * day
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(ten) || !exists(six) {
		t.Fatalf("CLRD_RETENTION 7d: ten=%v six=%v", exists(ten), exists(six))
	}
	s.opt.Retention = 30 * day

	// It deletes data, so only an admin token may change it. An unrelated
	// save must not pin the CLRD_RETENTION value into the database.
	reader := mkToken(t, s, "reader", []string{ScopeRead}, nil)
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", reader, map[string]any{"keep_cleared_days": 5}); code != 403 {
		t.Fatalf("read token set keep_cleared_days: %d", code)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", reader, map[string]any{"autolink": false}); code != 200 {
		t.Fatalf("read token display switch: %d", code)
	}
	var raw string
	s.db.QueryRow(`SELECT value FROM settings WHERE key = 'display'`).Scan(&raw)
	if !strings.Contains(raw, `"keep_cleared_days":null`) {
		t.Fatalf("unrelated save stored a keep: %s", raw)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"keep_cleared_days": -1}); code != 400 {
		t.Fatalf("negative days: %d", code)
	}

	// The preview counts exactly what the next prune removes.
	_, c := call(t, ts, "GET", "/api/v1/counts?keep_cleared_days=5", admin, nil)
	if c["would_remove"] != 1.0 {
		t.Fatalf("preview: %v", c)
	}
	if code, st := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"keep_cleared_days": 5}); code != 200 || st["keep_cleared_days"] != 5.0 {
		t.Fatalf("set 5 days: %d %v", code, st)
	}
	// Events as old as the removed message: a client that far behind must
	// get a reset, not replay changes to rows that are gone.
	if _, err := s.db.Exec(`UPDATE events SET created_at = ?`, ms(time.Now().Add(-6*day))); err != nil {
		t.Fatal(err)
	}
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(six) || !exists(four) || !exists(open) || !exists(snoozed) {
		t.Fatalf("5 days: six=%v four=%v open=%v snoozed=%v", exists(six), exists(four), exists(open), exists(snoozed))
	}
	if code, _ := call(t, ts, "GET", "/api/v1/events?since=0", admin, nil); code != 410 {
		t.Fatalf("events older than the keep: want 410, got %d", code)
	}

	// Forever keeps every cleared message.
	call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"keep_cleared_days": 0})
	s.db.Exec(`UPDATE notifications SET updated_at = ? WHERE id = ?`, ms(time.Now().Add(-400*day)), four)
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(four) {
		t.Fatal("forever removed a cleared message")
	}
}
