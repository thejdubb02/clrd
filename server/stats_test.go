package server

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStats(t *testing.T) {
	s, ts, admin := newTest(t)
	s.opt.Retention = 100 * 365 * 24 * time.Hour // keeps the 2023 history below, so trends show
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
	for i, topic := range []string{"disk", "disk", "web", "disk"} {
		at := base - 86_400_000
		if i == 3 {
			at--
		}
		if _, err := s.db.Exec(`UPDATE notifications SET created_at = ? WHERE id = ?`, at, pub(topic, "")); err != nil {
			t.Fatal(err)
		}
	}
	tok := s.lookupToken(context.Background(), admin)
	if _, err := s.db.Exec(`UPDATE senders SET alarm_topic = 'alarm' WHERE id = ?`, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET state_by_id = 'another-token' WHERE created_at = ?`, base+2*3_600_000); err != nil {
		t.Fatal(err)
	}
	// A browser session of the sender clears one: still cleared by the sender.
	browser := s.lookupToken(context.Background(), mkToken(t, s, "browser", []string{ScopeAdmin}, nil))
	if _, err := s.db.Exec(`INSERT INTO sessions (token_id, parent_id) VALUES (?, ?)`, browser.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET state_by_id = ? WHERE created_at = ?`, browser.ID, base+3_600_000); err != nil {
		t.Fatal(err)
	}
	_, cancelSub := s.Subscribe()
	defer cancelSub()
	ws := dialWS(t, ts.URL, admin, 0)
	ws.until(func(m msg) bool { return m.Type == "ready" })
	q := fmt.Sprintf("/api/v1/stats?from=%d&to=%d", base, base+86_400_000)
	code, st := call(t, ts, "GET", q, admin, nil)
	if code != 200 || st["total"] != 4.0 || st["priorities"].(map[string]any)["4"] != 4.0 {
		t.Fatalf("stats: %d %v", code, st)
	}
	if st["cleared"] != 3.0 || st["self_cleared"] != 2.0 || st["connected"] != 1.0 {
		t.Fatalf("cleared=%v self_cleared=%v connected=%v", st["cleared"], st["self_cleared"], st["connected"])
	}
	quiet := st["quiet"].([]any)
	if len(quiet) != 1 {
		t.Fatalf("quiet: %v", quiet)
	}
	alarm := quiet[0].(map[string]any)
	if alarm["sender_id"] != tok.ID || alarm["name"] != tok.Name || alarm["topic"] != "alarm" {
		t.Fatalf("quiet alarm: %v", alarm)
	}
	var wantWeek [7][24]float64
	for _, offset := range []int64{0, 30 * 60_000, 3_600_000, 2 * 3_600_000} {
		at := time.UnixMilli(base + offset).In(time.Local)
		wantWeek[at.Weekday()][at.Hour()]++
	}
	week := st["week_hours"].([]any)
	if len(week) != len(wantWeek) {
		t.Fatalf("week has %d days", len(week))
	}
	for day, raw := range week {
		row := raw.([]any)
		if len(row) != len(wantWeek[day]) {
			t.Fatalf("day %d has %d hours", day, len(row))
		}
		for hour, count := range row {
			if count != wantWeek[day][hour] {
				t.Fatalf("week[%d][%d] = %v, want %v", day, hour, count, wantWeek[day][hour])
			}
		}
	}
	hours := st["hours"].([]any)
	if _, ok := hours[0].(map[string]any)["by"]; ok {
		t.Fatal("ungrouped hour has a breakdown")
	}
	if len(hours) != 3 || hours[0].(map[string]any)["at"] != float64(base) || hours[0].(map[string]any)["count"] != 2.0 {
		t.Fatalf("hours: %v", hours)
	}
	disk := st["topics"].([]any)[0].(map[string]any)
	if disk["key"] != "disk" || disk["previous"] != 2.0 || disk["cleared"] != 3.0 || disk["open"] != 0.0 ||
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
	if s := st["senders"].([]any); len(s) != 1 || s[0].(map[string]any)["count"] != 4.0 || s[0].(map[string]any)["previous"] != 3.0 {
		t.Fatalf("senders: %v", s)
	}
	s.opt.Retention = 30 * 24 * time.Hour // the period before is long pruned now
	if _, pruned := call(t, ts, "GET", q, admin, nil); pruned["topics"].([]any)[0].(map[string]any)["previous"] != nil {
		t.Fatalf("trend past retention: %v", pruned["topics"])
	}
	s.opt.Retention = 100 * 365 * 24 * time.Hour
	emptyRange := fmt.Sprintf("/api/v1/stats?from=%d&to=%d", base-2, base-1)
	if code, empty := call(t, ts, "GET", emptyRange, admin, nil); code != 200 ||
		empty["total"] != 0.0 || len(empty["quiet"].([]any)) != 1 {
		t.Fatalf("quiet alarm outside notification range: %d %v", code, empty)
	}
	for _, topic := range []string{"a", "b", "c", "d", "e"} {
		if _, err := s.db.Exec(`UPDATE notifications SET created_at = ? WHERE id = ?`, base, pub(topic, "")); err != nil {
			t.Fatal(err)
		}
	}
	code, grouped := call(t, ts, "GET", q+"&group=topic", admin, nil)
	if code != 200 {
		t.Fatalf("grouped stats: %d %v", code, grouped)
	}
	breakdown := grouped["hours"].([]any)[0].(map[string]any)["by"].(map[string]any)
	if len(breakdown) != 6 || breakdown["other"] != 2.0 {
		t.Fatalf("breakdown: %v", breakdown)
	}
	for _, topic := range []string{"disk", "a", "b", "c", "d"} {
		if breakdown[topic] != 1.0 {
			t.Errorf("breakdown %q: %v", topic, breakdown)
		}
	}
	if c, _ := call(t, ts, "GET", q+"&group=bogus", admin, nil); c != 400 {
		t.Fatalf("bad group: %d, want 400", c)
	}
	for group, key := range map[string]string{"sender": tok.ID, "priority": "4"} {
		code, grouped := call(t, ts, "GET", q+"&group="+group, admin, nil)
		if code != 200 {
			t.Fatalf("group=%s: %d %v", group, code, grouped)
		}
		breakdown := grouped["hours"].([]any)[0].(map[string]any)["by"].(map[string]any)
		if len(breakdown) != 1 || breakdown[key] != 7.0 {
			t.Errorf("group=%s: %v", group, breakdown)
		}
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
	for _, bad := range []string{"?days=0", "?days=400", "?days=281474976710663", fmt.Sprintf("?from=%d&to=%d", base, base), "?to=soon"} {
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

// reopenAfter runs each statement on s, closes it and opens the database
// again, as an upgrade from an older schema would.
func reopenAfter(t *testing.T, s *Server, open func() *Server, stmts ...string) *Server {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	return open()
}

// An upgrade copies webhook failures already in the delivery log, once (#794).
func TestHookFailuresUpgrade(t *testing.T) {
	open := reopener(t)
	s := open()
	if _, err := s.db.Exec(`INSERT INTO hook_deliveries (topic, name, seq, event, body, status, next_at, updated_at)
		VALUES ('t', 'h', 1, 'dismissed', '{}', 'failed', 0, 42), ('t', 'h', 2, 'dismissed', '{}', 'ok', 0, 43)`); err != nil {
		t.Fatal(err)
	}
	s = reopenAfter(t, s, open, `DROP TRIGGER hook_failed`, `DROP TABLE hook_failures`)
	s = reopenAfter(t, s, open)
	defer s.Close()
	var n int
	var at int64
	if err := s.db.QueryRow(`SELECT COUNT(*), MAX(at) FROM hook_failures WHERE topic = 't'`).Scan(&n, &at); err != nil || n != 1 || at != 42 {
		t.Fatalf("hook_failures after upgrade: %v %d %d, want 1 at 42", err, n, at)
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
	s = reopenAfter(t, s, open, `ALTER TABLE notifications DROP COLUMN cleared_at`)
	defer s.Close()
	var v int64
	if err := s.db.QueryRow(`SELECT cleared_at FROM notifications WHERE id = ?`, n.ID).Scan(&v); err != nil || v != 42 {
		t.Fatalf("after upgrade: %v %d", err, v)
	}
}

// Upgrade backfill refines cleared_at from the event log when a mark landed
// after the clear, and uses the latest clear after a reopen (#661).
func TestClearedAtUpgradeFromEvents(t *testing.T) {
	open := reopener(t)
	s := open()
	raw := mkToken(t, s, "admin", []string{ScopeAdmin}, nil)
	ctx := context.Background()
	tok := s.lookupToken(ctx, raw)
	// First: dismiss at t=100, then mark at t=500 -> backfill must pick 100.
	n1, err := s.Publish(ctx, tok, PublishInput{Topic: "t", Message: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Act(ctx, tok, n1.ID, "dismiss", "api", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE events SET created_at = 100 WHERE type = ? AND json_extract(data, '$.notification.id') = ?`, EventState, n1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMark(ctx, tok, n1.ID, MarkSeen, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET updated_at = 500 WHERE id = ?`, n1.ID); err != nil {
		t.Fatal(err)
	}
	// Second: dismiss at 100, reopen at 200, dismiss at 300, mark at 600 -> must pick 300.
	n2, err := s.Publish(ctx, tok, PublishInput{Topic: "t", Message: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Act(ctx, tok, n2.ID, "dismiss", "api", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Act(ctx, tok, n2.ID, "reopen", "api", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Act(ctx, tok, n2.ID, "dismiss", "api", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT seq FROM events WHERE type = ? AND json_extract(data, '$.notification.id') = ? ORDER BY seq`, EventState, n2.ID)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for rows.Next() {
		var seq int64
		rows.Scan(&seq)
		seqs = append(seqs, seq)
	}
	rows.Close()
	if len(seqs) != 3 {
		t.Fatalf("state events: %v", seqs)
	}
	for i, ts := range []int64{100, 200, 300} {
		if _, err := s.db.Exec(`UPDATE events SET created_at = ? WHERE seq = ?`, ts, seqs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetMark(ctx, tok, n2.ID, MarkSeen, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET updated_at = 600 WHERE id = ?`, n2.ID); err != nil {
		t.Fatal(err)
	}
	s = reopenAfter(t, s, open, `ALTER TABLE notifications DROP COLUMN cleared_at`)
	defer s.Close()
	var c1, c2 int64
	if err := s.db.QueryRow(`SELECT cleared_at FROM notifications WHERE id = ?`, n1.ID).Scan(&c1); err != nil || c1 != 100 {
		t.Fatalf("n1 cleared_at: %v %d, want 100", err, c1)
	}
	if err := s.db.QueryRow(`SELECT cleared_at FROM notifications WHERE id = ?`, n2.ID).Scan(&c2); err != nil || c2 != 300 {
		t.Fatalf("n2 cleared_at: %v %d, want 300", err, c2)
	}
}

// Pre-1970 timestamps floor to the hour below instead of rounding toward zero (#661).
func TestStatsFloorPre1970(t *testing.T) {
	s, ts, admin := newTest(t)
	code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "old", "message": "m"})
	if code != 200 {
		t.Fatalf("publish: %d", code)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET created_at = -1 WHERE id = ?`, n["id"].(string)); err != nil {
		t.Fatal(err)
	}
	code, st := call(t, ts, "GET", "/api/v1/stats?from=-7200000&to=3600000", admin, nil)
	if code != 200 {
		t.Fatalf("stats: %d %v", code, st)
	}
	hours := st["hours"].([]any)
	if len(hours) != 1 || hours[0].(map[string]any)["at"] != float64(-3_600_000) {
		t.Fatalf("pre-1970 bucket: %v, want at=-3600000", hours)
	}
}

// Delivery health counts failed outgoing webhooks and Web Push sends per topic,
// and CSV export carries the same numbers (#661).
func TestStatsDeliveryAndCSV(t *testing.T) {
	s, ts, admin := newTest(t)
	s.opt.Retention = 100 * 365 * 24 * time.Hour // keeps the 2023 history, so trends show
	const base = int64(1_700_000_000_000)
	code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "m", "external_id": "=SUM(A1)"})
	if code != 200 {
		t.Fatalf("publish: %d", code)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET created_at = ? WHERE id = ?`, base+1000, n["id"].(string)); err != nil {
		t.Fatal(err)
	}
	// Dismiss the first so a second publish with the same external_id creates a second row (flapper).
	call(t, ts, "POST", "/api/v1/notifications/"+n["id"].(string)+"/dismiss", admin, nil)
	code, n2 := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "alerts", "message": "m2", "external_id": "=SUM(A1)"})
	if code != 200 {
		t.Fatalf("publish 2: %d", code)
	}
	if _, err := s.db.Exec(`UPDATE notifications SET created_at = ? WHERE id = ?`, base+2000, n2["id"].(string)); err != nil {
		t.Fatal(err)
	}
	// Seed failed and ok webhook deliveries, plus Web Push failures inside and outside the range.
	// Deliveries fail the way the sender does it: pending, then updated to failed.
	for _, q := range []string{
		`INSERT INTO hooks_out (topic, name, url, events) VALUES ('alerts', 'h1', 'https://hook.test/', '[]')`,
		fmt.Sprintf(`INSERT INTO hook_deliveries (topic, name, seq, event, body, next_at, updated_at) VALUES ('alerts', 'h1', 1, 'dismissed', '{}', 0, %d)`, base+3000),
		fmt.Sprintf(`INSERT INTO hook_deliveries (topic, name, seq, event, body, next_at, updated_at) VALUES ('alerts', 'h1', 2, 'dismissed', '{}', 0, %d)`, base+4000),
		fmt.Sprintf(`INSERT INTO hook_deliveries (topic, name, seq, event, body, status, next_at, updated_at) VALUES ('alerts', 'h1', 3, 'dismissed', '{}', 'ok', 0, %d)`, base+5000),
		`UPDATE hook_deliveries SET status = 'failed' WHERE seq IN (1, 2)`,
		fmt.Sprintf(`INSERT INTO push_failures (topic, at) VALUES ('alerts', %d)`, base+3500),
		fmt.Sprintf(`INSERT INTO push_failures (topic, at) VALUES ('ops', %d)`, base+3600),
		fmt.Sprintf(`INSERT INTO push_failures (topic, at) VALUES ('ops', %d)`, base-1000),
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	q := fmt.Sprintf("/api/v1/stats?from=%d&to=%d", base, base+86_400_000)
	code, st := call(t, ts, "GET", q, admin, nil)
	if code != 200 {
		t.Fatalf("stats: %d %v", code, st)
	}
	deliv := st["delivery"].([]any)
	if len(deliv) != 2 {
		t.Fatalf("delivery: %v", deliv)
	}
	d0 := deliv[0].(map[string]any)
	d1 := deliv[1].(map[string]any)
	if d0["topic"] != "alerts" || d0["webhooks"] != 2.0 || d0["web_push"] != 1.0 ||
		d1["topic"] != "ops" || d1["webhooks"] != 0.0 || d1["web_push"] != 1.0 {
		t.Fatalf("delivery order/counts: %v", deliv)
	}
	// Deleting the hook and its delivery log keeps its past failures (#794).
	if c, _ := call(t, ts, "DELETE", "/api/v1/topics/alerts/hooks/h1", admin, nil); c != 200 && c != 204 {
		t.Fatalf("delete hook: %d", c)
	}
	if _, st = call(t, ts, "GET", q, admin, nil); st["delivery"].([]any)[0].(map[string]any)["webhooks"] != 2.0 {
		t.Fatalf("delivery after hook delete: %v", st["delivery"])
	}
	// A value starting with a quote stays apart from one the guard quoted (#794).
	if csvCell("-disk") == csvCell("'-disk") || csvCell("'-disk") != "''-disk" {
		t.Fatalf("csvCell collision: %q %q", csvCell("-disk"), csvCell("'-disk"))
	}
	// Line breaks and backslashes survive a CSV reader that folds CR LF (#804).
	vals := []string{"a\r\nb", "a\nb", "a\rb", `a\rb`, `a\\rb`, "\r=x", `C:\x`}
	var cb bytes.Buffer
	for _, v := range vals {
		writeCSVRow(&cb, csvCell(v))
	}
	back, err := csv.NewReader(&cb).ReadAll()
	if err != nil || len(back) != len(vals) {
		t.Fatalf("csv readback: %v %v", err, back)
	}
	seen := map[string]bool{}
	unesc := strings.NewReplacer(`\\`, `\`, `\r`, "\r")
	for i, r := range back {
		if seen[r[0]] {
			t.Errorf("csv collision on %q: %q", vals[i], r[0])
		}
		seen[r[0]] = true
		if got := unesc.Replace(strings.TrimPrefix(r[0], "'")); got != vals[i] {
			t.Errorf("csv decode %q: got %q", vals[i], got)
		}
	}
	// Bad format is refused.
	if c, _ := call(t, ts, "GET", q+"&format=xml", admin, nil); c != 400 {
		t.Fatalf("bad format: %d, want 400", c)
	}
	// CSV export.
	req, _ := http.NewRequest("GET", ts.URL+q+"&format=csv&group=topic", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("csv status/type: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	recs, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v\n%s", err, body)
	}
	if len(recs) < 5 || recs[0][0] != "section" {
		t.Fatalf("csv header: %v", recs)
	}
	joined := string(body)
	local := time.UnixMilli(base + 1000).In(time.Local)
	for _, want := range []string{
		"summary,total,,2,",
		"summary,cleared,,1,",
		"summary,self_cleared,,1,",
		fmt.Sprintf("week_hours,%d,%d,2,", local.Weekday(), local.Hour()),
		"hour_by," + time.UnixMilli(base).Truncate(time.Hour).UTC().Format(time.RFC3339) + ",alerts,2,",
		"topic_previous,alerts,,0,",
		"topic,alerts,,2,1,1,",
		"flapper,alerts,'=SUM(A1),2,",
		"delivery_webhook,alerts,,2,",
		"delivery_web_push,alerts,,1,",
		"delivery_web_push,ops,,1,",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("csv missing %q in:\n%s", want, joined)
		}
	}
}

// Prometheus /metrics is off by default, requires full admin when on, and
// reports stored counts and 24h delivery failures (#661).
func TestMetrics(t *testing.T) {
	// Off by default.
	_, tsOff, adminOff := newTest(t)
	if c, _ := call(t, tsOff, "GET", "/metrics", adminOff, nil); c != 404 {
		t.Fatalf("/metrics when off: %d, want 404", c)
	}
	// ConfigFromEnv parses CLRD_METRICS.
	t.Setenv("CLRD_METRICS", "on")
	if o, err := ConfigFromEnv(); err != nil || !o.Metrics {
		t.Fatalf("ConfigFromEnv on: %v %+v", err, o)
	}
	t.Setenv("CLRD_METRICS", "maybe")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv accepted invalid CLRD_METRICS")
	}

	s, err := New(Options{DB: filepath.Join(t.TempDir(), "m.db"), Metrics: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	admin := mkToken(t, s, "admin", []string{ScopeAdmin}, nil)
	reader := mkToken(t, s, "reader", []string{ScopeRead}, nil)
	scoped := mkToken(t, s, "scoped", []string{ScopeAdmin}, []string{"ops"})

	if c, _ := call(t, ts, "GET", "/metrics", "", nil); c != 401 {
		t.Fatalf("unauth /metrics: %d, want 401", c)
	}
	for _, tok := range []string{reader, scoped} {
		if c, _ := call(t, ts, "GET", "/metrics", tok, nil); c != 403 {
			t.Fatalf("non-full-admin /metrics: %d, want 403", c)
		}
	}
	if c, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "ops", "message": "hi", "priority": 5}); c != 200 {
		t.Fatalf("publish: %d", c)
	}
	if _, err := s.db.Exec(`INSERT INTO push_failures (topic, at) VALUES ('ops', ?)`, ms(time.Now())); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics status/type: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{
		`clrd_notifications_stored{state="active"} 1`,
		`clrd_notifications_topic_open{topic="ops"} 1`,
		`clrd_notifications_priority_stored{priority="5"} 1`,
		`clrd_delivery_failures_24h{kind="web_push",topic="ops"} 1`,
		`clrd_events_seq 1`,
		`clrd_jobs_failing 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q in:\n%s", want, body)
		}
	}

	// Topic name "metrics" stays valid for existing configs and publishers even
	// when CLRD_METRICS=on: GET /metrics does not collide with POST /metrics or
	// GET /metrics/json (#661).
	cfgPath := filepath.Join(t.TempDir(), "cfg.json")
	cfg := `{"tokens":[{"name":"m-pub","secret":"clrd_metrics_topic_token_1234","scopes":["publish","read"],"topics":["metrics"]}]}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	sm, err := New(Options{DB: filepath.Join(t.TempDir(), "mc.db"), Config: cfgPath, Metrics: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sm.Close() })
	if err := sm.applyConfig(context.Background()); err != nil {
		t.Fatalf("config token for topic 'metrics' failed: %v", err)
	}
	tsm := httptest.NewServer(sm)
	t.Cleanup(tsm.Close)
	mAdmin := mkToken(t, sm, "admin", []string{ScopeAdmin}, nil)
	if c, _ := call(t, tsm, "POST", "/api/v1/notifications", "clrd_metrics_topic_token_1234", map[string]any{"topic": "metrics", "message": "api pub"}); c != 200 {
		t.Fatalf("native publish to topic 'metrics': %d", c)
	}
	ntfyReq, _ := http.NewRequest("POST", tsm.URL+"/metrics", strings.NewReader("ntfy pub"))
	ntfyReq.Header.Set("Authorization", "Bearer clrd_metrics_topic_token_1234")
	ntfyResp, err := http.DefaultClient.Do(ntfyReq)
	if err != nil || ntfyResp.StatusCode != 200 {
		t.Fatalf("ntfy POST /metrics: %v %v", err, ntfyResp)
	}
	ntfyResp.Body.Close()
	mReq, _ := http.NewRequest("GET", tsm.URL+"/metrics", nil)
	mReq.Header.Set("Authorization", "Bearer "+mAdmin)
	mResp, err := http.DefaultClient.Do(mReq)
	if err != nil {
		t.Fatal(err)
	}
	mRaw, _ := io.ReadAll(mResp.Body)
	mResp.Body.Close()
	if mResp.StatusCode != 200 || !strings.Contains(string(mRaw), `clrd_notifications_topic_open{topic="metrics"} 2`) {
		t.Fatalf("GET /metrics after publishing to topic 'metrics': %d %s", mResp.StatusCode, mRaw)
	}
}
