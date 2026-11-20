package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCheckin(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()

	for _, bad := range []map[string]any{{"checkin": "daily"}, {"checkin": "10s"}, {"checkin": "-1h"}, {"checkin": "9000h"},
		{"checkin": "0s"}, {"checkin": "1h", "icon": "javascript:x"}} {
		if code, out := call(t, ts, "PUT", "/api/v1/topics/backup", admin, bad); code != 400 {
			t.Errorf("%v: %d %v", bad, code, out)
		}
	}
	var on int
	if s.db.QueryRow(`SELECT COUNT(*) FROM topics WHERE checkin_ms IS NOT NULL`).Scan(&on); on != 0 {
		t.Fatal("a rejected PUT set the check-in")
	}
	if code, out := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "backup", "external_id": checkinID}); code != 400 {
		t.Fatalf("reserved external_id: %d %v", code, out)
	}
	if code, out := call(t, ts, "PUT", "/api/v1/topics/backup", admin, map[string]any{"checkin": "25h"}); code != 200 || out["checkin"] != "25h" {
		t.Fatalf("set checkin: %d %v", code, out)
	}
	// Removing the icon keeps the check-in.
	if code, out := call(t, ts, "PUT", "/api/v1/topics/backup", admin, map[string]any{"icon": "https://example.com/i.png"}); code != 200 {
		t.Fatalf("set icon: %d %v", code, out)
	}
	if code, out := call(t, ts, "PUT", "/api/v1/topics/backup", admin, map[string]any{"icon": ""}); code != 200 {
		t.Fatalf("clear icon: %d %v", code, out)
	}
	var icon string
	if err := s.db.QueryRow(`SELECT icon FROM topics WHERE topic = 'backup' AND checkin_ms = ?`, (25 * time.Hour).Milliseconds()).Scan(&icon); err != nil || icon != "" {
		t.Fatalf("after clearing the icon: %q %v", icon, err)
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

	// A check-in that lands between the sweep's query and its alert wins.
	overdue()
	if _, err := s.Publish(ctx, serverToken, PublishInput{Topic: "backup", ExternalID: checkinID, Message: "late", checkin: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, &Token{Name: "cron", Scopes: []string{ScopePublish}}, PublishInput{Topic: "backup", Message: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, serverToken, PublishInput{Topic: "backup", ExternalID: checkinID, Message: "stale", checkin: true}); !errors.Is(err, errOnTime) {
		t.Fatalf("stale alert: %v", err)
	}
	if a = alert(); a["state"] != "resolved" {
		t.Fatalf("stale alert reopened: %v", a)
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
