package server

import (
	"context"
	"testing"
	"time"
)

func TestRepeat(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	for _, bad := range []map[string]any{
		{"repeat": "30s"},
		{"repeat": "25h"},
		{"repeat": "soon"},
		{"repeat": "5m", "repeat_times": 101},
		{"repeat": "5m", "repeat_times": -1},
		{"repeat_times": 3},
	} {
		bad["topic"], bad["message"] = "pager", "m"
		if code, out := call(t, ts, "POST", "/api/v1/notifications", admin, bad); code != 400 {
			t.Errorf("%v: %d %v", bad, code, out)
		}
	}
	pub := func(body map[string]any) string {
		t.Helper()
		body["topic"], body["message"] = "pager", "disk full"
		code, n := call(t, ts, "POST", "/api/v1/notifications", admin, body)
		if code != 200 {
			t.Fatalf("publish: %d %v", code, n)
		}
		return n["id"].(string)
	}
	due := func(id string) { // make a running repeat due now, then run the ticker
		t.Helper()
		s.db.Exec(`UPDATE notifications SET repeat = json_set(repeat, '$.next', ?) WHERE id = ? AND json_extract(repeat, '$.next') IS NOT NULL`,
			time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), id)
		if err := s.repeatDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) *Notification {
		t.Helper()
		n, err := getNotification(ctx, s.db, "id = ?", id)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	repeats := func(id string) (k int) {
		s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'repeat' AND json_extract(data, '$.notification.id') = ?`, id).Scan(&k)
		return k
	}

	// No repeat: the ticker never touches it.
	plain := pub(map[string]any{})
	if get(plain).Repeat != nil {
		t.Fatal("repeat set without asking")
	}

	// Two re-alerts, then it stops by itself.
	id := pub(map[string]any{"priority": 5, "repeat": "5m", "repeat_times": 2})
	r := get(id).Repeat
	if r == nil || r.Every != "5m" || r.Left != 2 || r.Next == nil || time.Until(*r.Next) < 4*time.Minute {
		t.Fatalf("repeat after publish: %+v", r)
	}
	if err := s.repeatDue(ctx); err != nil || repeats(id) != 0 {
		t.Fatalf("re-alerted before its time: %v %d", err, repeats(id))
	}
	due(id)
	if n := get(id); repeats(id) != 1 || n.Repeat.Count != 1 || n.Repeat.Left != 1 || n.Repeat.Next == nil || n.State != Active {
		t.Fatalf("first re-alert: %d %+v", repeats(id), n.Repeat)
	}
	due(id)
	if n := get(id); repeats(id) != 2 || n.Repeat.Left != 0 || n.Repeat.Next != nil {
		t.Fatalf("last re-alert: %d %+v", repeats(id), n.Repeat)
	}
	due(id)
	if repeats(id) != 2 {
		t.Fatalf("re-alerted past repeat_times: %d", repeats(id))
	}

	// Acknowledge stops it for good, and a reopen does not start it again.
	id = pub(map[string]any{"repeat": "5m"})
	if get(id).Repeat.Left != 10 {
		t.Fatalf("default repeat_times: %+v", get(id).Repeat)
	}
	if code, out := call(t, ts, "POST", "/api/v1/notifications/"+id+"/ack", admin, nil); code != 200 {
		t.Fatalf("ack: %d %v", code, out)
	}
	if get(id).Repeat.Next != nil {
		t.Fatal("ack left the repeat running")
	}
	call(t, ts, "POST", "/api/v1/notifications/"+id+"/reopen", admin, nil)
	due(id)
	if get(id).State != Active || repeats(id) != 0 {
		t.Fatalf("reopen restarted the repeat: %s %d", get(id).State, repeats(id))
	}

	// Snooze stops it too.
	id = pub(map[string]any{"repeat": "5m"})
	call(t, ts, "POST", "/api/v1/notifications/"+id+"/snooze", admin, map[string]any{"until": time.Now().Add(time.Hour).Format(time.RFC3339)})
	if get(id).Repeat.Next != nil {
		t.Fatal("snooze left the repeat running")
	}

	// A scheduled send counts from its delivery.
	at := time.Now().Add(time.Hour)
	id = pub(map[string]any{"repeat": "5m", "snooze_until": at.Format(time.RFC3339)})
	if r := get(id).Repeat; r.Next == nil || r.Next.Before(at) {
		t.Fatalf("scheduled repeat starts before delivery: %+v", r)
	}

	// A tap on the card a device showed before a re-alert still counts.
	id = pub(map[string]any{"repeat": "5m", "actions": []map[string]any{{"label": "Yes", "reply": "yes"}}})
	shown := get(id).Seq
	due(id)
	if code, out := call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", admin, map[string]any{"reply": "yes", "seq": shown}); code != 200 {
		t.Fatalf("reply after a re-alert: %d %v", code, out)
	}

	// No re-alert once it has expired.
	id = pub(map[string]any{"repeat": "5m", "expires": "1h"})
	s.db.Exec(`UPDATE notifications SET expires_at = ? WHERE id = ?`, ms(time.Now().Add(-time.Second)), id)
	due(id)
	if repeats(id) != 0 {
		t.Fatal("re-alerted after expiry")
	}

	// A re-alert during a silence is silenced, like any other event.
	id = pub(map[string]any{"repeat": "5m"})
	call(t, ts, "POST", "/api/v1/silences", admin, map[string]any{"topic": "pager", "for": "1h"})
	due(id)
	if !get(id).Silenced || repeats(id) != 1 {
		t.Fatalf("re-alert in a silence: %v %d", get(id).Silenced, repeats(id))
	}
}
