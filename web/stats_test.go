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
		snap.Stats = &Stats{From: from, To: from + 24*hour, Total: 3, Answered: 1, Days: q.Stats, Group: q.Group, Cleared: 2, SelfCleared: 1, Connected: 4,
			Quiet:      []StatsQuiet{{SenderID: "tok-1", Name: "nas", Topic: "missing-backup"}},
			Priorities: map[string]int{"5": 2, "3": 1},
			Hours:      []StatsBucket{{At: from - from%hour, Count: 2}, {At: from - from%hour + 3*hour, Count: 1}},
			Topics:     []StatsGroup{{Key: "home", Count: 3, Previous: 1, Open: 1, Cleared: 2, ClearMedianMS: 45000, ClearP90MS: 3*3600000 + 5*60000}},
			Senders:    []StatsGroup{{Key: "", Count: 2, Previous: 3, Open: 2}, {Key: "tok-1", Name: "nas", Count: 1, Cleared: 1}},
			ClearedBy:  []StatsGroup{{Key: "web", Count: 2, Cleared: 2, ClearMedianMS: 60000}},
			Flappers:   []StatsFlapper{{Topic: "home", ExternalID: "disk-full", Count: 2}},
			Delivery:   []StatsDelivery{{Topic: "home", Webhooks: 3, WebPush: 1}}}
		snap.Stats.WeekHours[time.Friday][0] = 2
		snap.Stats.WeekHours[time.Friday][3] = 1
		if q.Group == "topic" {
			snap.Stats.Hours[0].By = map[string]int{"home": 2}
			snap.Stats.Hours[1].By = map[string]int{"home": 1}
		}
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
		`href="/api/v1/stats?days=1&amp;format=csv">CSV</a>`,
		"Notifications by hour of day and day of week, server time",
		`scope="row" class="text-left">Friday</th>`, `<td title="2">`, `opacity: 0.5"`,
		`>up 2</span>`, `>down 1</span>`, `>new</span>`,
		"4 connected now. 1 cleared by their sender, 1 by a person.",
		"Gone quiet", `href="/app/senders/tok-1">nas</a> (missing-backup)`,
		"Delivery health", `<th scope="col">Webhooks</th>`, `<th scope="col">Web Push</th>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stats page has no %q", want)
		}
	}
	if n := strings.Count(body, `<td title="`); n != 7*24 {
		t.Errorf("heatmap has %d cells", n)
	}
	if code, grouped := get(t, h, "/app/stats?days=1&group=topic"); code != 200 ||
		!strings.Contains(grouped, `group=topic" aria-current="page">By topic`) ||
		!strings.Contains(grouped, `<th scope="row">Fri, Jan 2</th><td class="tabular-nums">3</td>`) {
		t.Errorf("grouped stats returned %d or is missing the daily total", code)
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

func TestFloorBucket(t *testing.T) {
	const hour = int64(time.Hour / time.Millisecond)
	if got := floorBucket(-1, hour); got != -hour {
		t.Fatalf("floorBucket(-1, hour) = %d, want %d", got, -hour)
	}
	if got := floorBucket(0, hour); got != 0 {
		t.Fatalf("floorBucket(0, hour) = %d, want 0", got)
	}
}
