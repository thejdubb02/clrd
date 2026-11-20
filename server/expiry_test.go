package server

import (
	"context"
	"testing"
	"time"
)

func TestExpiry(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()

	for _, bad := range []string{"soon", "-5m", "1ns", "2001-01-01T00:00:00Z"} {
		if code, out := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "door", "message": "m", "expires": bad}); code != 400 {
			t.Errorf("expires %q: %d %v", bad, code, out)
		}
	}
	at := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "door", "message": "m", "expires": at})
	if code != 200 || n["expires_at"] == nil {
		t.Fatalf("RFC3339 expires: %d %v", code, n)
	}
	code, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "door", "external_id": "front", "message": "open", "expires": "30m"})
	exp, _ := time.Parse(time.RFC3339, n["expires_at"].(string))
	if code != 200 || exp.Before(time.Now().Add(29*time.Minute)) || exp.After(time.Now().Add(31*time.Minute)) {
		t.Fatalf("duration expires: %d %v", code, n)
	}
	id := n["id"].(string)

	// Nothing is due yet.
	if err := s.expireDue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, n = call(t, ts, "GET", "/api/v1/notifications/"+id, admin, nil); n["state"] != "active" {
		t.Fatalf("expired early: %v", n)
	}

	// Its time comes: the server resolves it with a normal event.
	if _, err := s.db.Exec(`UPDATE notifications SET expires_at = ? WHERE id = ?`, ms(time.Now().Add(-time.Second)), id); err != nil {
		t.Fatal(err)
	}
	before := s.seq.Load()
	if err := s.expireDue(ctx); err != nil {
		t.Fatal(err)
	}
	_, n = call(t, ts, "GET", "/api/v1/notifications/"+id, admin, nil)
	if n["state"] != "resolved" || n["state_by"] != "server" || n["expires_at"] != nil || s.seq.Load() != before+1 {
		t.Fatalf("after expiry: %v, seq %d -> %d", n, before, s.seq.Load())
	}

	// A reopen does not expire again, and a new publish without expires has none.
	if code, n = call(t, ts, "POST", "/api/v1/notifications/"+id+"/reopen", admin, nil); code != 200 || n["state"] != "active" {
		t.Fatalf("reopen: %d %v", code, n)
	}
	s.expireDue(ctx)
	if _, n = call(t, ts, "GET", "/api/v1/notifications/"+id, admin, nil); n["state"] != "active" {
		t.Fatalf("reopened row expired again: %v", n)
	}
	if _, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "door", "external_id": "front", "message": "open", "expires": "1h"}); n["id"] != id || n["expires_at"] == nil {
		t.Fatalf("upsert sets expires: %v", n)
	}
	if _, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "door", "external_id": "front", "message": "still open"}); n["id"] != id || n["expires_at"] != nil {
		t.Fatalf("upsert without expires keeps none: %v", n)
	}

	// Snoozed past its expiry: it resolves, it never wakes and re-alerts.
	code, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "door", "message": "m", "expires": "1h",
		"snooze_until": time.Now().Add(time.Hour).Format(time.RFC3339)})
	if code != 200 {
		t.Fatalf("snoozed publish: %d %v", code, n)
	}
	id = n["id"].(string)
	past := ms(time.Now().Add(-time.Second))
	s.db.Exec(`UPDATE notifications SET expires_at = ?, snooze_until = ? WHERE id = ?`, past, past, id)
	s.wakeSnoozed(ctx)
	if _, n = call(t, ts, "GET", "/api/v1/notifications/"+id, admin, nil); n["state"] != "snoozed" {
		t.Fatalf("an expired snooze woke: %v", n)
	}
}
