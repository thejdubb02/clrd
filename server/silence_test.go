package server

import (
	"context"
	"testing"
	"time"
)

func TestSilences(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	pub := func(topic string, tags ...string) map[string]any {
		t.Helper()
		code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": topic, "message": "m", "tags": tags})
		if code != 200 {
			t.Fatalf("publish: %d %v", code, n)
		}
		return n
	}

	if n := pub("door"); n["silenced"] != false {
		t.Fatalf("no silence yet: %v", n)
	}
	for _, bad := range []map[string]any{
		{"for": "1h"},
		{"topic": "door"},
		{"topic": "door", "for": "-1h"},
		{"topic": "door", "for": "9000h"},
		{"topic": "door", "for": "1ns"},
		{"topic": "door", "for": "1h", "until": time.Now().Add(time.Hour).Format(time.RFC3339)},
	} {
		if code, out := call(t, ts, "POST", "/api/v1/silences", admin, bad); code != 400 {
			t.Errorf("%v: %d %v", bad, code, out)
		}
	}

	code, sl := call(t, ts, "POST", "/api/v1/silences", admin, map[string]any{"topic": "door", "for": "2h", "comment": "night"})
	if code != 201 || sl["id"] == nil {
		t.Fatalf("create: %d %v", code, sl)
	}
	doorID := sl["id"].(string)
	if n := pub("door"); n["silenced"] != true {
		t.Fatalf("publish during a silence: %v", n)
	}
	if n := pub("garage"); n["silenced"] != false {
		t.Fatalf("other topic silenced: %v", n)
	}
	// The flag is stored, so a read agrees with the event, and ntfy gets min priority.
	n := pub("door")
	id := n["id"].(string)
	if _, got := call(t, ts, "GET", "/api/v1/notifications/"+id, admin, nil); got["silenced"] != true {
		t.Fatalf("read after silenced publish: %v", got)
	}
	row, err := getNotification(ctx, s.db, "id = ?", id)
	if err != nil || s.ntfyMessage(row, 1).Priority != 1 {
		t.Fatalf("ntfy priority while silenced: %v", err)
	}

	// Tag silence across topics.
	call(t, ts, "POST", "/api/v1/silences", admin, map[string]any{"tag": "backup", "for": "1h"})
	if n := pub("garage", "backup"); n["silenced"] != true {
		t.Fatalf("tag silence: %v", n)
	}
	if n := pub("garage", "other"); n["silenced"] != false {
		t.Fatalf("tag silence hit an untagged one: %v", n)
	}

	// A snooze that wakes during the silence does not re-alert.
	code, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "door", "message": "m",
		"snooze_until": time.Now().Add(time.Hour).Format(time.RFC3339)})
	if code != 200 {
		t.Fatalf("snoozed publish: %d %v", code, n)
	}
	sid := n["id"].(string)
	s.db.Exec(`UPDATE notifications SET snooze_until = ? WHERE id = ?`, ms(time.Now().Add(-time.Second)), sid)
	if err := s.wakeSnoozed(ctx); err != nil {
		t.Fatal(err)
	}
	if _, got := call(t, ts, "GET", "/api/v1/notifications/"+sid, admin, nil); got["state"] != "active" || got["silenced"] != true {
		t.Fatalf("snooze wake during a silence: %v", got)
	}

	// Scope: a token for another topic cannot see, make or end it.
	other := mkToken(t, s, "other", []string{"read", "publish"}, []string{"garage"})
	if code, out := call(t, ts, "POST", "/api/v1/silences", other, map[string]any{"topic": "door", "for": "1h"}); code != 403 {
		t.Fatalf("other topic create: %d %v", code, out)
	}
	if code, out := call(t, ts, "POST", "/api/v1/silences", other, map[string]any{"tag": "x", "for": "1h"}); code != 403 {
		t.Fatalf("limited token, every topic: %d %v", code, out)
	}
	if code, _ := call(t, ts, "DELETE", "/api/v1/silences/"+doorID, other, nil); code != 404 {
		t.Fatalf("other topic delete: %d", code)
	}
	if _, out := call(t, ts, "GET", "/api/v1/silences", other, nil); len(out["silences"].([]any)) != 1 {
		t.Fatalf("other token sees: %v", out)
	}
	pubOnly := mkToken(t, s, "sender", []string{"publish"}, []string{"door"})
	if code, out := call(t, ts, "GET", "/api/v1/silences", pubOnly, nil); code != 403 {
		t.Fatalf("publish-only list: %d %v", code, out)
	}
	if _, out := call(t, ts, "GET", "/api/v1/silences", admin, nil); len(out["silences"].([]any)) != 2 {
		t.Fatalf("admin sees: %v", out)
	}

	// DELETE ends it early.
	if code, _ := call(t, ts, "DELETE", "/api/v1/silences/"+doorID, admin, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if n := pub("door"); n["silenced"] != false {
		t.Fatalf("after delete: %v", n)
	}

	// It ends by itself, and prune drops it.
	call(t, ts, "POST", "/api/v1/silences", admin, map[string]any{"topic": "door", "for": "1h"})
	s.db.Exec(`UPDATE silences SET until_ms = ?`, ms(time.Now().Add(-time.Second)))
	if n := pub("door", "backup"); n["silenced"] != false {
		t.Fatalf("after it ended: %v", n)
	}
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	var left int
	s.db.QueryRow(`SELECT COUNT(*) FROM silences`).Scan(&left)
	if left != 0 {
		t.Fatalf("prune left %d silences", left)
	}
}
