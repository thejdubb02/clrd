package server

import (
	"context"
	"fmt"
	"testing"
)

func TestStats(t *testing.T) {
	s, ts, admin := newTest(t)
	const base = int64(1_699_999_200_000) // a whole UTC hour
	pub := func(topic, ext string) string {
		t.Helper()
		code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": topic, "message": "m", "external_id": ext, "priority": 4})
		if code != 200 {
			t.Fatalf("publish: %d %v", code, n)
		}
		return n["id"].(string)
	}
	// disk/full fires three times and is cleared after 1, 2 and 10 minutes; web stays open.
	for i, mins := range []int64{1, 2, 10} {
		id := pub("disk", "full")
		call(t, ts, "POST", "/api/v1/notifications/"+id+"/dismiss", admin, nil)
		at := base + int64(i)*3_600_000
		if _, err := s.db.Exec(`UPDATE notifications SET created_at = ?, cleared_at = ? WHERE id = ?`, at, at+mins*60_000, id); err != nil {
			t.Fatal(err)
		}
	}
	id := pub("web", "")
	if _, err := s.db.Exec(`UPDATE notifications SET created_at = ? WHERE id = ?`, base+30*60_000, id); err != nil {
		t.Fatal(err)
	}
	q := fmt.Sprintf("/api/v1/stats?from=%d&to=%d", base, base+86_400_000)
	code, st := call(t, ts, "GET", q, admin, nil)
	if code != 200 || st["total"] != 4.0 || st["priorities"].(map[string]any)["4"] != 4.0 {
		t.Fatalf("stats: %d %v", code, st)
	}
	hours := st["hours"].([]any)
	if len(hours) != 3 || hours[0].(map[string]any)["at"] != float64(base) || hours[0].(map[string]any)["count"] != 2.0 {
		t.Fatalf("hours: %v", hours)
	}
	disk := st["topics"].([]any)[0].(map[string]any)
	if disk["key"] != "disk" || disk["cleared"] != 3.0 || disk["open"] != 0.0 ||
		disk["clear_median_ms"] != 120_000.0 || disk["clear_p90_ms"] != 600_000.0 {
		t.Fatalf("disk: %v", disk)
	}
	if web := st["topics"].([]any)[1].(map[string]any); web["open"] != 1.0 || web["clear_median_ms"] != 0.0 {
		t.Fatalf("web: %v", web)
	}
	if f := st["flappers"].([]any); len(f) != 1 || f[0].(map[string]any)["external_id"] != "full" || f[0].(map[string]any)["count"] != 3.0 {
		t.Fatalf("flappers: %v", f)
	}
	if by := st["cleared_by"].([]any); len(by) != 1 || by[0].(map[string]any)["count"] != 3.0 {
		t.Fatalf("cleared_by: %v", by)
	}
	if s := st["senders"].([]any); len(s) != 1 || s[0].(map[string]any)["count"] != 4.0 {
		t.Fatalf("senders: %v", s)
	}
	// Admins only, and only without a topic list; a bad range is refused.
	for raw, want := range map[string]int{
		mkToken(t, s, "reader", []string{ScopeRead}, nil):              403,
		mkToken(t, s, "infra", []string{ScopeAdmin}, []string{"disk"}): 403,
	} {
		if c, _ := call(t, ts, "GET", q, raw, nil); c != want {
			t.Fatalf("token: %d, want %d", c, want)
		}
	}
	for _, bad := range []string{"?days=0", "?days=400", fmt.Sprintf("?from=%d&to=%d", base, base), "?to=soon"} {
		if c, _ := call(t, ts, "GET", "/api/v1/stats"+bad, admin, nil); c != 400 {
			t.Fatalf("%s: %d", bad, c)
		}
	}
}

// cleared_at is the first clear: a later mark keeps it, a reopen drops it.
func TestClearedAt(t *testing.T) {
	s, ts, admin := newTest(t)
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "m"})
	id := n["id"].(string)
	at := func() (v *int64) {
		t.Helper()
		if err := s.db.QueryRow(`SELECT cleared_at FROM notifications WHERE id = ?`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if at() != nil {
		t.Fatal("open row has cleared_at")
	}
	call(t, ts, "POST", "/api/v1/notifications/"+id+"/dismiss", admin, nil)
	first := at()
	if _, err := s.db.Exec(`UPDATE notifications SET cleared_at = 5 WHERE id = ?`, id); err != nil { // tell a later write apart
		t.Fatal(err)
	}
	call(t, ts, "POST", "/api/v1/notifications/"+id+"/mark", admin, map[string]any{"mark": "seen"})
	if first == nil || *at() != 5 {
		t.Fatalf("mark moved cleared_at: %v", at())
	}
	call(t, ts, "POST", "/api/v1/notifications/"+id+"/reopen", admin, nil)
	if at() != nil {
		t.Fatal("reopen kept cleared_at")
	}
}

// An upgrade fills cleared_at for rows already cleared.
func TestClearedAtUpgrade(t *testing.T) {
	open := reopener(t)
	s := open()
	raw := mkToken(t, s, "admin", []string{ScopeAdmin}, nil)
	ctx := context.Background()
	n, err := s.Publish(ctx, s.lookupToken(ctx, raw), PublishInput{Topic: "t", Message: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET state = 'dismissed', updated_at = 42 WHERE id = ?`, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE notifications DROP COLUMN cleared_at`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open()
	defer s.Close()
	var v int64
	if err := s.db.QueryRow(`SELECT cleared_at FROM notifications WHERE id = ?`, n.ID).Scan(&v); err != nil || v != 42 {
		t.Fatalf("after upgrade: %v %d", err, v)
	}
}
