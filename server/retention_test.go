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

// TestTopicKeepCleared: a topic with its own keep_cleared_days goes by it,
// every other topic by the global one, and the preview counts the same (#262).
func TestTopicKeepCleared(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	day := 24 * time.Hour
	mk := func(topic string, age time.Duration) string {
		t.Helper()
		_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": topic, "message": "m"})
		id := n["id"].(string)
		call(t, ts, "POST", "/api/v1/notifications/"+id+"/dismiss", admin, nil)
		if _, err := s.db.Exec(`UPDATE notifications SET updated_at = ? WHERE id = ?`, ms(time.Now().Add(-age)), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exists := func(id string) bool {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE id = ?`, id).Scan(&n)
		return n == 1
	}
	ci3, ci10 := mk("ci", 3*day), mk("ci", 10*day)
	keep20, keep40 := mk("keep", 20*day), mk("keep", 40*day)
	other10, other40 := mk("other", 10*day), mk("other", 40*day)

	// Only an admin token for the topic may set or drop its keep.
	limited := mkToken(t, s, "ci-admin", []string{ScopeRead, ScopeAdmin}, []string{"ci"})
	reader := mkToken(t, s, "reader", []string{ScopeRead}, nil)
	for _, c := range []struct {
		tok  string
		body map[string]any
		want int
	}{
		{reader, map[string]any{"ci": 2}, 403},
		{reader, map[string]any{"ci": nil}, 403},
		{limited, map[string]any{"other": 2}, 403},
		{admin, map[string]any{"ci": -1}, 400},
		{admin, map[string]any{"bad topic": 2}, 400},
		{limited, map[string]any{"ci": 2}, 200},
	} {
		if code, out := call(t, ts, "PUT", "/api/v1/settings", c.tok, map[string]any{"topic_keep_cleared_days": c.body}); code != c.want {
			t.Fatalf("%v: want %d, got %d %v", c.body, c.want, code, out)
		}
	}
	call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"keep_cleared_days": 7, "topic_keep_cleared_days": map[string]any{"keep": 0}})

	// The topic-limited admin sees only its own topic's keep.
	if _, st := call(t, ts, "GET", "/api/v1/settings", limited, nil); len(st["topic_keep_cleared_days"].(map[string]any)) != 1 {
		t.Fatalf("limited read: %v", st["topic_keep_cleared_days"])
	}
	for q, want := range map[string]float64{"keep_cleared_days=7": 2, "keep_cleared_days=2&topic=ci": 2, "keep_cleared_days=5&topic=ci": 1} {
		if _, c := call(t, ts, "GET", "/api/v1/counts?"+q, admin, nil); c["would_remove"] != want {
			t.Fatalf("preview %s: %v", q, c["would_remove"])
		}
	}
	// Events older than the shortest keep (ci's 2 days) go too, so a
	// client that far behind gets a reset, not replays of removed rows.
	if _, err := s.db.Exec(`UPDATE events SET created_at = ?`, ms(time.Now().Add(-3*day))); err != nil {
		t.Fatal(err)
	}
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/events?since=0", admin, nil); code != 410 {
		t.Fatalf("events older than a topic keep: want 410, got %d", code)
	}
	if exists(ci3) || exists(ci10) || !exists(keep20) || !exists(keep40) || exists(other10) || exists(other40) {
		t.Fatalf("ci3=%v ci10=%v keep20=%v keep40=%v other10=%v other40=%v",
			exists(ci3), exists(ci10), exists(keep20), exists(keep40), exists(other10), exists(other40))
	}

	// Null drops the topic's keep: it follows the global one again.
	call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"topic_keep_cleared_days": map[string]any{"keep": nil}})
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(keep20) || exists(keep40) {
		t.Fatal("dropped override still kept forever")
	}
}

// TestKeepDefaultPreview: keep_cleared_days=default previews dropping a
// topic's own value against the exact global keep, not whole days (#336).
func TestKeepDefaultPreview(t *testing.T) {
	s, ts, admin := newTest(t)
	s.opt.Retention = 36 * time.Hour
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ci", "message": "m"})
	id := n["id"].(string)
	call(t, ts, "POST", "/api/v1/notifications/"+id+"/dismiss", admin, nil)
	if _, err := s.db.Exec(`UPDATE notifications SET updated_at = ? WHERE id = ?`, ms(time.Now().Add(-40*time.Hour)), id); err != nil {
		t.Fatal(err)
	}
	call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"topic_keep_cleared_days": map[string]any{"ci": 0}})
	for q, want := range map[string]any{"keep_cleared_days=default&topic=ci": 1.0, "keep_cleared_days=2&topic=ci": 0.0} {
		if _, c := call(t, ts, "GET", "/api/v1/counts?"+q, admin, nil); c["would_remove"] != want {
			t.Fatalf("preview %s: %v", q, c["would_remove"])
		}
	}
	if code, _ := call(t, ts, "GET", "/api/v1/counts?keep_cleared_days=-1", admin, nil); code != 400 {
		t.Fatalf("keep_cleared_days=-1: want 400, got %d", code)
	}
}
