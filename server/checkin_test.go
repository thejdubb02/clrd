package server

import (
	"context"
	"testing"
	"time"
)

func TestCheckin(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()

	for _, bad := range []string{"daily", "10s", "-1h", "9000h"} {
		if code, out := call(t, ts, "PUT", "/api/v1/topics/backup", admin, map[string]any{"checkin": bad}); code != 400 {
			t.Errorf("checkin %q: %d %v", bad, code, out)
		}
	}
	if code, out := call(t, ts, "PUT", "/api/v1/topics/backup", admin, map[string]any{"checkin": "25h"}); code != 200 || out["checkin"] != "25h" {
		t.Fatalf("set checkin: %d %v", code, out)
	}
	// Removing the icon keeps the check-in.
	if code, out := call(t, ts, "PUT", "/api/v1/topics/backup", admin, map[string]any{"icon": ""}); code != 200 {
		t.Fatalf("clear icon: %d %v", code, out)
	}
	overdue := func() {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE topics SET checkin_at = ? WHERE topic = 'backup'`, ms(time.Now().Add(-26*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	alert := func() map[string]any {
		t.Helper()
		_, out := call(t, ts, "GET", "/api/v1/notifications?topic=backup&state=all", admin, nil)
		for _, n := range out["notifications"].([]any) {
			if n := n.(map[string]any); n["external_id"] == checkinID {
				return n
			}
		}
		return nil
	}

	// On time: nothing.
	if err := s.checkins(ctx); err != nil {
		t.Fatal(err)
	}
	if a := alert(); a != nil {
		t.Fatalf("alert before due: %v", a)
	}

	// Overdue: one urgent alert, not one per tick.
	overdue()
	for range 2 {
		if err := s.checkins(ctx); err != nil {
			t.Fatal(err)
		}
	}
	a := alert()
	if a == nil || a["state"] != "active" || a["priority"] != float64(5) {
		t.Fatalf("missed check-in alert: %v", a)
	}
	before := s.seq.Load()
	overdue()
	if err := s.checkins(ctx); err != nil || s.seq.Load() != before {
		t.Fatalf("alerted twice: %v, seq %d -> %d", err, before, s.seq.Load())
	}

	// The next check-in resolves it and restarts the timer.
	if code, out := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "backup", "message": "done"}); code != 200 {
		t.Fatalf("check in: %d %v", code, out)
	}
	if a = alert(); a["state"] != "resolved" {
		t.Fatalf("after check-in: %v", a)
	}
	if err := s.checkins(ctx); err != nil {
		t.Fatal(err)
	}
	if a = alert(); a["state"] != "resolved" {
		t.Fatalf("timer not restarted: %v", a)
	}

	// Off: never alerts again.
	if code, out := call(t, ts, "PUT", "/api/v1/topics/backup", admin, map[string]any{"checkin": ""}); code != 200 {
		t.Fatalf("checkin off: %d %v", code, out)
	}
	overdue()
	before = s.seq.Load()
	if err := s.checkins(ctx); err != nil || s.seq.Load() != before {
		t.Fatalf("alert after off: %v", err)
	}
}
