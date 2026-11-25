package web

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// statsAPI answers like the server: stats only for a full admin, and only
// when the page asks for them.
type statsAPI struct {
	readOnly
	admin bool
	days  *int
}

func (s statsAPI) Snapshot(_ *http.Request, q Query) (Snapshot, error) {
	*s.days = q.Stats
	snap := Snapshot{CanSenders: s.admin}
	if q.Stats > 0 && s.admin {
		from := time.Date(2026, 1, 2, 0, 30, 0, 0, time.UTC).UnixMilli()
		hour := int64(time.Hour / time.Millisecond)
		snap.Stats = &Stats{From: from, To: from + 24*hour, Total: 3, Answered: 1, Days: q.Stats,
			Priorities: map[string]int{"5": 2, "3": 1},
			Hours:      []StatsBucket{{At: from - from%hour, Count: 2}, {At: from - from%hour + 3*hour, Count: 1}},
			Topics:     []StatsGroup{{Key: "home", Count: 3, Open: 1, Cleared: 2, ClearMedianMS: 45000, ClearP90MS: 3*3600000 + 5*60000}},
			Senders:    []StatsGroup{{Key: "", Count: 2, Open: 2}, {Key: "tok-1", Name: "nas", Count: 1, Cleared: 1}},
			ClearedBy:  []StatsGroup{{Key: "web", Count: 2, Cleared: 2, ClearMedianMS: 60000}},
			Flappers:   []StatsFlapper{{Topic: "home", ExternalID: "disk-full", Count: 2}}}
	}
	return snap, nil
}

func TestStatsPage(t *testing.T) {
	var days int
	h := Handler(statsAPI{admin: true, days: &days}, "")
	code, body := get(t, h, "/app/stats?days=1")
	if code != 200 || days != 1 {
		t.Fatalf("status %d, asked for %d days", code, days)
	}
	for _, want := range []string{
		`<strong class="tabular-nums" data-total>3</strong>`,
		`href="/app/stats?days=1" aria-current="page">24 hours`,
		"Volume per hour", "<title>Jan 2, 00:00 UTC: 2</title>", "<title>Jan 2, 01:00 UTC: 0</title>",
		"<td>Urgent</td><td class=\"tabular-nums\">2</td>",
		">Anonymous</td>", ">nas</td>", ">45s</td>", ">3h 5m</td>", "disk-full",
		`data-live="stats"`, `<th scope="col">Cleared</th>`, ">1m</td>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stats page has no %q", want)
		}
	}
	if n := strings.Count(body, " UTC: "); n != 25 {
		t.Errorf("%d bars for a day that spans 25 hours, want 25 (empty hours kept)", n)
	}
	if n := strings.Count(body, `scope="col">Open</th>`); n != 2 {
		t.Errorf("%d Open columns, want 2 (none on who clears)", n)
	}
	if get(t, h, "/app/stats?days=5"); days != 7 {
		t.Errorf("days=5 asked for %d days, want the 7 day default", days)
	}
	if code, body := get(t, Handler(statsAPI{days: &days}, ""), "/app/stats"); code != 403 || strings.Contains(body, "data-total") {
		t.Errorf("a non-admin got status %d, or saw stats", code)
	}
	if _, body := get(t, Handler(statsAPI{days: &days}, ""), "/app"); strings.Contains(body, "/app/stats") {
		t.Error("a non-admin sees the Stats link")
	}
	if _, body := get(t, h, "/app"); !strings.Contains(body, `href="/app/stats"`) || days != 0 {
		t.Errorf("an admin sees no Stats link, or the inbox asked for stats (%d days)", days)
	}
}

func TestTook(t *testing.T) {
	for ms, want := range map[int64]string{0: "0s", 59999: "59s", 60000: "1m", 3600000: "1h 0m", 2*86400000 + 4*3600000: "2d 4h"} {
		if got := took(ms); got != want {
			t.Errorf("took(%d) = %q, want %q", ms, got, want)
		}
	}
}
