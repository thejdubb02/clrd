package web

import (
	"fmt"
	"slices"
	"strconv"
	"time"
)

// Stats is GET /api/v1/stats (docs/SPEC.md, #120) for the Stats page.
type Stats struct {
	WeekHours   [7][24]int      `json:"week_hours"`
	Cleared     int             `json:"cleared"`
	SelfCleared int             `json:"self_cleared"`
	Connected   int             `json:"connected"`
	Quiet       []StatsQuiet    `json:"quiet"`
	Group       string          `json:"-"`
	From        int64           `json:"from"`
	To          int64           `json:"to"`
	Total       int             `json:"total"`
	Answered    int             `json:"answered"`
	Priorities  map[string]int  `json:"priorities"`
	Hours       []StatsBucket   `json:"hours"`
	Topics      []StatsGroup    `json:"topics"`
	Senders     []StatsGroup    `json:"senders"` // Key is the top token id; Name is filled from the senders list
	ClearedBy   []StatsGroup    `json:"cleared_by"`
	Flappers    []StatsFlapper  `json:"flappers"`
	Delivery    []StatsDelivery `json:"delivery"`
	Days        int             `json:"-"` // the range picked on the page
}

type StatsQuiet struct {
	SenderID string `json:"sender_id"`
	Name     string `json:"name"`
	Topic    string `json:"topic"`
}

type StatsDelivery struct {
	Topic    string `json:"topic"`
	Webhooks int    `json:"webhooks"`
	WebPush  int    `json:"web_push"`
}

type StatsBucket struct {
	At    int64          `json:"at"` // unix ms, start of the UTC hour
	Count int            `json:"count"`
	By    map[string]int `json:"by"`
}

type StatsGroup struct {
	Key           string `json:"key"`
	Name          string `json:"-"`
	Count         int    `json:"count"`
	Previous      int    `json:"previous"`
	Open          int    `json:"open"`
	Cleared       int    `json:"cleared"`
	ClearMedianMS int64  `json:"clear_median_ms"`
	ClearP90MS    int64  `json:"clear_p90_ms"`
}

type StatsFlapper struct {
	Topic      string `json:"topic"`
	ExternalID string `json:"external_id"`
	Count      int    `json:"count"`
}

// StatsRanges are the ranges the page offers, in days; 7 is the default.
var StatsRanges = []int{1, 7, 30, 90}

// statsDays reads ?days=, keeping it to one of StatsRanges.
func statsDays(v string) int {
	if n, err := strconv.Atoi(v); err == nil && slices.Contains(StatsRanges, n) {
		return n
	}
	return 7
}

func (s Stats) Ranges() []int { return StatsRanges }

func statsGroup(v string) string {
	switch v {
	case "topic", "sender", "priority":
		return v
	}
	return ""
}

type StatsGroupOption struct {
	Key, Label string
}

func (s Stats) GroupOptions() []StatsGroupOption {
	return []StatsGroupOption{{"", "None"}, {"topic", "By topic"}, {"sender", "By sender"}, {"priority", "By priority"}}
}

func (s Stats) ClearedByPerson() int { return s.Cleared - s.SelfCleared }

type HeatCell struct {
	Count   int
	Opacity float64
}

type HeatRow struct {
	Day   string
	Cells [24]HeatCell
}

func (s Stats) Heatmap() [7]HeatRow {
	peak := 1
	for _, hours := range s.WeekHours {
		peak = max(peak, slices.Max(hours[:]))
	}
	var rows [7]HeatRow
	for day, hours := range s.WeekHours {
		rows[day].Day = time.Weekday(day).String()
		for hour, count := range hours {
			opacity := float64(count) / float64(peak)
			if count > 0 {
				opacity = max(opacity, 0.15)
			}
			rows[day].Cells[hour] = HeatCell{count, opacity}
		}
	}
	return rows
}

type StatsDay struct {
	Label  string
	Counts map[string]int
}

func (s Stats) GroupDays() []StatsDay {
	const step = int64(24 * time.Hour / time.Millisecond)
	var days []StatsDay
	for _, h := range s.Hours {
		label := time.UnixMilli(floorBucket(h.At, step)).UTC().Format("Mon, Jan 2")
		if len(days) == 0 || days[len(days)-1].Label != label {
			days = append(days, StatsDay{label, map[string]int{}})
		}
		for key, count := range h.By {
			days[len(days)-1].Counts[key] += count
		}
	}
	return days
}

func (s Stats) GroupColumns() []StatsGroup {
	if s.Group == "priority" {
		return []StatsGroup{{Key: "1"}, {Key: "2"}, {Key: "3"}, {Key: "4"}, {Key: "5"}}
	}
	rows := s.Topics
	if s.Group == "sender" {
		rows = s.Senders
	}
	cols := slices.Clone(rows[:min(len(rows), 5)])
	if !slices.ContainsFunc(cols, func(g StatsGroup) bool { return g.Key == "other" }) {
		for _, h := range s.Hours {
			if h.By["other"] > 0 {
				return append(cols, StatsGroup{Key: "other"})
			}
		}
	}
	return cols
}

func (g StatsGroup) Trend() string {
	switch {
	case g.Previous == 0 && g.Count > 0:
		return "new"
	case g.Count > g.Previous:
		return fmt.Sprintf("up %d", g.Count-g.Previous)
	case g.Count < g.Previous:
		return fmt.Sprintf("down %d", g.Previous-g.Count)
	}
	return ""
}

// StatsTable is one of the page's top lists.
type StatsTable struct {
	Title, Col string
	Rows       []StatsGroup
	Clears     bool // the who clears table: Count is clears and nothing is open
}

func (s Stats) Tables() []StatsTable {
	return []StatsTable{{"Top topics", "Topic", s.Topics, false}, {"Top senders", "Sender", s.Senders, false}, {"Who clears", "Cleared by", s.ClearedBy, true}}
}

// Label is the group's name for people: the sender's name, else its key.
func (g StatsGroup) Label() string {
	if g.Name != "" {
		return g.Name
	}
	if g.Key == "" {
		return "Anonymous"
	}
	return g.Key
}

// Bar is one bar of the volume chart, in the chart's 600 by 100 box.
type Bar struct {
	X, Y, W, H float64
	Count      int
	Label      string
}

func floorBucket(t, step int64) int64 {
	r := t % step
	if r < 0 {
		r += step
	}
	return t - r
}

// Bars buckets Hours by hour for a one day range and by day otherwise,
// with empty buckets kept so the chart shows the quiet stretches.
func (s Stats) Bars() []Bar {
	step := int64(time.Hour / time.Millisecond)
	format := "Jan 2, 15:00 UTC"
	if s.Days > 1 {
		step *= 24
		format = "Mon Jan 2"
	}
	start := floorBucket(s.From, step)
	n := int((s.To - start + step - 1) / step)
	if n < 1 {
		return nil
	}
	counts := make([]int, n)
	for _, h := range s.Hours {
		if h.At >= start {
			if i := int((h.At - start) / step); i < n {
				counts[i] += h.Count
			}
		}
	}
	top := max(slices.Max(counts), 1)
	w := 600 / float64(n)
	bars := make([]Bar, n)
	for i, c := range counts {
		h := 100 * float64(c) / float64(top)
		if c > 0 {
			h = max(h, 2) // one notification still shows
		}
		bars[i] = Bar{X: float64(i)*w + w*0.1, Y: 100 - h, W: w * 0.8, H: h, Count: c,
			Label: time.UnixMilli(start + int64(i)*step).UTC().Format(format)}
	}
	return bars
}

// PriorityRow is one priority with its count.
type PriorityRow struct {
	Name  string
	Count int
}

// PriorityRows lists the five priorities, urgent first.
func (s Stats) PriorityRows() []PriorityRow {
	names := []string{"Min", "Low", "Default", "High", "Urgent"}
	rows := make([]PriorityRow, 0, 5)
	for p := 5; p >= 1; p-- {
		rows = append(rows, PriorityRow{names[p-1], s.Priorities[strconv.Itoa(p)]})
	}
	return rows
}

// took shows a time to clear in ms the short way: 45s, 12m, 3h 5m, 2d 4h.
func took(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}
