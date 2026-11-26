package server

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Stats (issue #120): traffic numbers for an admin, read from the stored
// notifications. Cleared rows past the keep-cleared setting are gone, so a
// range longer than it counts only what is still kept.

const (
	statsMaxRange = 366 * 24 * time.Hour
	statsTop      = 50 // longest list returned per dimension
)

// StatsGroup is one topic, sender or clearer's numbers.
type StatsGroup struct {
	Key           string `json:"key"`
	Count         int    `json:"count"`
	Previous      *int   `json:"previous,omitempty"`
	Open          int    `json:"open"` // not cleared yet: the ones nobody dealt with
	Cleared       int    `json:"cleared"`
	ClearMedianMS int64  `json:"clear_median_ms"`
	ClearP90MS    int64  `json:"clear_p90_ms"`
	clear         []int64
}

// StatsBucket is the count published in one UTC hour.
type StatsBucket struct {
	At    int64          `json:"at"`
	Count int            `json:"count"`
	By    map[string]int `json:"by,omitempty"`
}

// StatsFlapper is an external id that fired more than once in the range.
type StatsFlapper struct {
	Topic      string `json:"topic"`
	ExternalID string `json:"external_id"`
	Count      int    `json:"count"`
}

// StatsDelivery is one topic's failed delivery counts in the range (#661).
type StatsDelivery struct {
	Topic    string `json:"topic"`
	Webhooks int    `json:"webhooks"` // outgoing webhook deliveries that failed
	WebPush  int    `json:"web_push"` // Web Push sends that failed
}

type StatsQuiet struct {
	SenderID string `json:"sender_id"`
	Name     string `json:"name"`
	Topic    string `json:"topic"`
}

type Stats struct {
	WeekHours   [7][24]int      `json:"week_hours"`
	Cleared     int             `json:"cleared"`
	SelfCleared int             `json:"self_cleared"`
	Quiet       []StatsQuiet    `json:"quiet"`
	Connected   int             `json:"connected"`
	From        int64           `json:"from"`
	To          int64           `json:"to"`
	Total       int             `json:"total"`
	Answered    int             `json:"answered"` // a reply action was tapped
	Priorities  map[string]int  `json:"priorities"`
	Hours       []StatsBucket   `json:"hours"`
	Topics      []*StatsGroup   `json:"topics"`
	Senders     []*StatsGroup   `json:"senders"`
	ClearedBy   []*StatsGroup   `json:"cleared_by"`
	Flappers    []StatsFlapper  `json:"flappers"`
	Delivery    []StatsDelivery `json:"delivery"` // topics with webhook or Web Push failures, worst first
}

// floorBucket rounds t down to a multiple of step, even for timestamps before 1970 (#661).
func floorBucket(t, step int64) int64 {
	r := t % step
	if r < 0 {
		r += step
	}
	return t - r
}

// Stats counts notifications created in [from, to). Admin with no topic list.
func (s *Server) Stats(ctx context.Context, tok *Token, from, to time.Time) (*Stats, error) {
	return s.GroupedStats(ctx, tok, from, to, "")
}

// GroupedStats optionally splits each hour by topic, sender or priority.
func (s *Server) GroupedStats(ctx context.Context, tok *Token, from, to time.Time, group string) (*Stats, error) {
	if err := tok.needFullAdmin(); err != nil {
		return nil, err
	}
	if !from.Before(to) || to.Sub(from) > statsMaxRange {
		return nil, errf(400, "bad_request", "from must be before to, at most 366 days apart")
	}
	switch group {
	case "", "topic", "sender", "priority":
	default:
		return nil, errf(400, "bad_request", "group must be topic, sender or priority")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT n.topic, n.sender, n.external_id, n.priority, n.state_by, n.created_at,
  n.cleared_at, n.reply != '', COALESCE(n.state_by_id != '' AND n.state_by_id = s.token_id, 0)
  FROM notifications n LEFT JOIN senders s ON s.id = n.sender
  WHERE n.created_at >= ? AND n.created_at < ?`, ms(from), ms(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	st := &Stats{From: ms(from), To: ms(to), Priorities: map[string]int{}}
	topics, senders, by := map[string]*StatsGroup{}, map[string]*StatsGroup{}, map[string]*StatsGroup{}
	hours, flaps := map[int64]*StatsBucket{}, map[[2]string]int{}
	add := func(m map[string]*StatsGroup, k string, cleared sql.NullInt64, created int64) {
		g := m[k]
		if g == nil {
			g = &StatsGroup{Key: k}
			m[k] = g
		}
		g.Count++
		if cleared.Valid {
			g.Cleared++
			g.clear = append(g.clear, cleared.Int64-created)
		} else {
			g.Open++
		}
	}
	for rows.Next() {
		var topic, sender, ext, stateBy string
		var prio int
		var created int64
		var cleared sql.NullInt64
		var answered, selfCleared bool
		if err := rows.Scan(&topic, &sender, &ext, &prio, &stateBy, &created, &cleared, &answered, &selfCleared); err != nil {
			return nil, err
		}
		st.Total++
		if answered {
			st.Answered++
		}
		priority := strconv.Itoa(prio)
		st.Priorities[priority]++
		local := time.UnixMilli(created).In(time.Local)
		st.WeekHours[local.Weekday()][local.Hour()]++
		at := floorBucket(created, time.Hour.Milliseconds())
		h := hours[at]
		if h == nil {
			h = &StatsBucket{At: at}
			if group != "" {
				h.By = map[string]int{}
			}
			hours[at] = h
		}
		h.Count++
		switch group {
		case "topic":
			h.By[topic]++
		case "sender":
			h.By[sender]++
		case "priority":
			h.By[priority]++
		}
		add(topics, topic, cleared, created)
		add(senders, sender, cleared, created)
		if cleared.Valid {
			st.Cleared++
			if selfCleared {
				st.SelfCleared++
			}
			add(by, stateBy, cleared, created)
		}
		if ext != "" {
			flaps[[2]string{topic, ext}]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	st.Hours = []StatsBucket{}
	for _, h := range hours {
		st.Hours = append(st.Hours, *h)
	}
	sort.Slice(st.Hours, func(i, j int) bool { return st.Hours[i].At < st.Hours[j].At })
	st.Topics, st.Senders, st.ClearedBy = top(topics), top(senders), top(by)
	switch group {
	case "topic":
		foldStatsHours(st.Hours, st.Topics)
	case "sender":
		foldStatsHours(st.Hours, st.Senders)
	}
	prev := ms(from.Add(-to.Sub(from)))
	if err := s.statsPrevious(ctx, "topic", prev, st.From, st.Topics); err != nil {
		return nil, err
	}
	if err := s.statsPrevious(ctx, "sender", prev, st.From, st.Senders); err != nil {
		return nil, err
	}
	st.Quiet, err = s.statsQuiet(ctx)
	if err != nil {
		return nil, err
	}
	s.hub.mu.Lock()
	for _, sub := range s.hub.subs { // every client stream: WebSocket, SSE and ntfy, not in-process ones
		if sub.tok != "" {
			st.Connected++
		}
	}
	s.hub.mu.Unlock()
	st.Flappers = []StatsFlapper{}
	for k, n := range flaps {
		if n > 1 {
			st.Flappers = append(st.Flappers, StatsFlapper{k[0], k[1], n})
		}
	}
	sort.Slice(st.Flappers, func(i, j int) bool {
		a, b := st.Flappers[i], st.Flappers[j]
		return a.Count > b.Count || a.Count == b.Count && a.Topic+"\x00"+a.ExternalID < b.Topic+"\x00"+b.ExternalID
	})
	st.Flappers = st.Flappers[:min(len(st.Flappers), statsTop)]
	deliv, err := s.deliveryFailures(ctx, ms(from), ms(to), statsTop)
	if err != nil {
		return nil, err
	}
	st.Delivery = deliv
	return st, nil
}

func foldStatsHours(hours []StatsBucket, groups []*StatsGroup) {
	keep := map[string]bool{"other": true}
	for _, g := range groups[:min(5, len(groups))] {
		keep[g.Key] = true
	}
	for _, h := range hours {
		other := 0
		for key, count := range h.By {
			if !keep[key] {
				other += count
				delete(h.By, key)
			}
		}
		if other > 0 {
			h.By["other"] += other
		}
	}
}

func (s *Server) statsPrevious(ctx context.Context, dimension string, from, to int64, groups []*StatsGroup) error {
	if len(groups) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+dimension+`, COUNT(*) FROM notifications
  WHERE created_at >= ? AND created_at < ? GROUP BY `+dimension, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	counts := make(map[string]*int, len(groups))
	for _, g := range groups {
		g.Previous = new(int)
		counts[g.Key] = g.Previous
	}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return err
		}
		if prev := counts[key]; prev != nil {
			*prev = count
		}
	}
	return rows.Err()
}

func (s *Server) statsQuiet(ctx context.Context) ([]StatsQuiet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, COALESCE(t.name, 'anonymous'), s.alarm_topic
  FROM senders s LEFT JOIN tokens t ON t.id = s.token_id WHERE s.alarm_topic != '' ORDER BY 2, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	quiet := []StatsQuiet{}
	for rows.Next() {
		var q StatsQuiet
		if err := rows.Scan(&q.SenderID, &q.Name, &q.Topic); err != nil {
			return nil, err
		}
		quiet = append(quiet, q)
	}
	return quiet, rows.Err()
}

// deliveryFailures counts failed outgoing webhook deliveries and Web Push
// sends per topic in [fromMS, toMS), worst first (#661). limit <= 0 keeps all.
func (s *Server) deliveryFailures(ctx context.Context, fromMS, toMS int64, limit int) ([]StatsDelivery, error) {
	byTopic := map[string]*StatsDelivery{}
	get := func(topic string) *StatsDelivery {
		d := byTopic[topic]
		if d == nil {
			d = &StatsDelivery{Topic: topic}
			byTopic[topic] = d
		}
		return d
	}
	rows, err := s.db.QueryContext(ctx, `SELECT topic, COUNT(*) FROM hook_failures WHERE at >= ? AND at < ? GROUP BY topic`, fromMS, toMS)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var topic string
		var n int
		if err := rows.Scan(&topic, &n); err != nil {
			rows.Close()
			return nil, err
		}
		get(topic).Webhooks = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT topic, COUNT(*) FROM push_failures WHERE at >= ? AND at < ? GROUP BY topic`, fromMS, toMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var topic string
		var n int
		if err := rows.Scan(&topic, &n); err != nil {
			return nil, err
		}
		get(topic).WebPush = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]StatsDelivery, 0, len(byTopic))
	for _, d := range byTopic {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Webhooks+out[i].WebPush, out[j].Webhooks+out[j].WebPush
		return a > b || a == b && out[i].Topic < out[j].Topic
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// top sorts groups by count, keeps the first statsTop and fills their clear times.
func top(m map[string]*StatsGroup) []*StatsGroup {
	out := []*StatsGroup{}
	for _, g := range m {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Count > out[j].Count || out[i].Count == out[j].Count && out[i].Key < out[j].Key
	})
	out = out[:min(len(out), statsTop)]
	for _, g := range out {
		if n := len(g.clear); n > 0 {
			slices.Sort(g.clear)
			g.ClearMedianMS, g.ClearP90MS = g.clear[(n-1)/2], g.clear[(n*9+9)/10-1] // nearest rank
		}
	}
	return out
}

func (s *Server) statsRoutes() {
	s.api("GET /api/v1/stats", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		q, to := r.URL.Query(), now()
		if v := q.Get("to"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return errf(400, "bad_request", "to must be unix milliseconds")
			}
			to = fromMS(n)
		}
		back := 7
		if v := q.Get("days"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 366 {
				return errf(400, "bad_request", "days must be a whole number from 1 to 366")
			}
			back = n
		}
		from := to.Add(-days(back))
		if v := q.Get("from"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return errf(400, "bad_request", "from must be unix milliseconds")
			}
			from = fromMS(n)
		}
		format := q.Get("format")
		if format != "" && format != "json" && format != "csv" {
			return errf(400, "bad_request", "format must be json or csv")
		}
		st, err := s.GroupedStats(r.Context(), tok, from, to, q.Get("group"))
		if err != nil {
			return err
		}
		if format == "csv" {
			writeStatsCSV(w, st)
			return nil
		}
		writeJSON(w, 200, st)
		return nil
	})
	s.route("GET /metrics", s.handleMetrics)
}

// csvCell guards free-text cells against spreadsheet formula prefixes. A
// leading quote gets one too, so a value already starting with a quote stays
// distinct from one the guard quoted (#794). A CR is written as \r and a
// backslash as \\, since many CSV readers fold CR LF into LF (#804).
func csvCell(v string) string {
	v = csvEsc.Replace(v)
	if len(v) > 0 && strings.ContainsRune("=+-@\t'", rune(v[0])) {
		return "'" + v
	}
	return v
}

var csvEsc = strings.NewReplacer(`\`, `\\`, "\r", `\r`)

// writeCSVRow appends one RFC 4180 CSV line without pulling encoding/csv into the binary.
func writeCSVRow(b *bytes.Buffer, cols ...string) {
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(',')
		}
		if strings.ContainsAny(c, ",\"\r\n") {
			b.WriteByte('"')
			b.WriteString(strings.ReplaceAll(c, `"`, `""`))
			b.WriteByte('"')
		} else {
			b.WriteString(c)
		}
	}
	b.WriteByte('\n')
}

// writeStatsCSV writes st in one flat CSV table with a section column (#661).
func writeStatsCSV(w http.ResponseWriter, st *Stats) {
	var buf bytes.Buffer
	writeCSVRow(&buf, "section", "key", "detail", "count", "open", "cleared", "clear_median_ms", "clear_p90_ms")
	writeCSVRow(&buf, "summary", "total", "", strconv.Itoa(st.Total), "", "", "", "")
	writeCSVRow(&buf, "summary", "answered", "", strconv.Itoa(st.Answered), "", "", "", "")
	writeCSVRow(&buf, "summary", "cleared", "", strconv.Itoa(st.Cleared), "", "", "", "")
	writeCSVRow(&buf, "summary", "self_cleared", "", strconv.Itoa(st.SelfCleared), "", "", "", "")
	writeCSVRow(&buf, "summary", "connected", "", strconv.Itoa(st.Connected), "", "", "", "")
	for _, q := range st.Quiet {
		writeCSVRow(&buf, "quiet", csvCell(q.SenderID), csvCell(q.Topic), "", "", "", "", "")
		writeCSVRow(&buf, "quiet_name", csvCell(q.SenderID), csvCell(q.Name), "", "", "", "", "")
	}
	for day, hours := range st.WeekHours {
		for hour, count := range hours {
			writeCSVRow(&buf, "week_hours", strconv.Itoa(day), strconv.Itoa(hour), strconv.Itoa(count), "", "", "", "")
		}
	}
	pkeys := make([]string, 0, len(st.Priorities))
	for k := range st.Priorities {
		pkeys = append(pkeys, k)
	}
	sort.Strings(pkeys)
	for _, k := range pkeys {
		writeCSVRow(&buf, "priority", k, "", strconv.Itoa(st.Priorities[k]), "", "", "", "")
	}
	for _, h := range st.Hours {
		at := fromMS(h.At).UTC().Format(time.RFC3339)
		writeCSVRow(&buf, "hour", at, "", strconv.Itoa(h.Count), "", "", "", "")
		for _, k := range slices.Sorted(maps.Keys(h.By)) {
			writeCSVRow(&buf, "hour_by", at, csvCell(k), strconv.Itoa(h.By[k]), "", "", "", "")
		}
	}
	writeGroups := func(sec string, gs []*StatsGroup) {
		for _, g := range gs {
			writeCSVRow(&buf, sec, csvCell(g.Key), "", strconv.Itoa(g.Count), strconv.Itoa(g.Open), strconv.Itoa(g.Cleared),
				strconv.FormatInt(g.ClearMedianMS, 10), strconv.FormatInt(g.ClearP90MS, 10))
			if g.Previous != nil {
				writeCSVRow(&buf, sec+"_previous", csvCell(g.Key), "", strconv.Itoa(*g.Previous), "", "", "", "")
			}
		}
	}
	writeGroups("topic", st.Topics)
	writeGroups("sender", st.Senders)
	writeGroups("cleared_by", st.ClearedBy)
	for _, f := range st.Flappers {
		writeCSVRow(&buf, "flapper", csvCell(f.Topic), csvCell(f.ExternalID), strconv.Itoa(f.Count), "", "", "", "")
	}
	for _, d := range st.Delivery {
		if d.Webhooks > 0 {
			writeCSVRow(&buf, "delivery_webhook", csvCell(d.Topic), "", strconv.Itoa(d.Webhooks), "", "", "", "")
		}
		if d.WebPush > 0 {
			writeCSVRow(&buf, "delivery_web_push", csvCell(d.Topic), "", strconv.Itoa(d.WebPush), "", "", "", "")
		}
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="clrd-stats.csv"`)
	w.WriteHeader(200)
	w.Write(buf.Bytes())
}

// promLabel escapes a Prometheus label value (text exposition format 0.0.4).
func promLabel(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// handleMetrics serves Prometheus text metrics when CLRD_METRICS=on (#661).
// Full admin only, like GET /api/v1/stats, so topic names never leak.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) error {
	if !s.opt.Metrics {
		return errf(404, "not_found", "metrics are off; set CLRD_METRICS=on")
	}
	tok := s.auth(r, true)
	if tok == nil {
		return errf(401, "unauthorized", noToken)
	}
	if err := tok.needFullAdmin(); err != nil {
		return err
	}
	ctx := r.Context()
	byState := map[string]int{}
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM notifications GROUP BY state`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return err
		}
		byState[st] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	byTopic := map[string]int{}
	rows, err = s.db.QueryContext(ctx, `SELECT topic, COUNT(*) FROM notifications WHERE cleared_at IS NULL GROUP BY topic ORDER BY topic`)
	if err != nil {
		return err
	}
	var topics []string
	for rows.Next() {
		var topic string
		var n int
		if err := rows.Scan(&topic, &n); err != nil {
			rows.Close()
			return err
		}
		byTopic[topic] = n
		topics = append(topics, topic)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	byPrio := map[int]int{}
	rows, err = s.db.QueryContext(ctx, `SELECT priority, COUNT(*) FROM notifications GROUP BY priority`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p, n int
		if err := rows.Scan(&p, &n); err != nil {
			rows.Close()
			return err
		}
		byPrio[p] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	to := now()
	deliv, err := s.deliveryFailures(ctx, ms(to.Add(-24*time.Hour)), ms(to)+1, 0)
	if err != nil {
		return err
	}
	failingJobs := 0
	s.failing.Range(func(_, _ any) bool { failingJobs++; return true })

	var b bytes.Buffer
	b.WriteString("# HELP clrd_notifications_stored Stored notifications by state.\n# TYPE clrd_notifications_stored gauge\n")
	for _, st := range []string{Active, Acknowledged, Snoozed, Dismissed, Resolved} {
		fmt.Fprintf(&b, "clrd_notifications_stored{state=%q} %d\n", st, byState[st])
	}
	b.WriteString("# HELP clrd_notifications_topic_open Open (not cleared) notifications by topic.\n# TYPE clrd_notifications_topic_open gauge\n")
	for _, t := range topics {
		fmt.Fprintf(&b, "clrd_notifications_topic_open{topic=\"%s\"} %d\n", promLabel(t), byTopic[t])
	}
	b.WriteString("# HELP clrd_notifications_priority_stored Stored notifications by priority.\n# TYPE clrd_notifications_priority_stored gauge\n")
	for p := 1; p <= 5; p++ {
		fmt.Fprintf(&b, "clrd_notifications_priority_stored{priority=\"%d\"} %d\n", p, byPrio[p])
	}
	b.WriteString("# HELP clrd_delivery_failures_24h Failed webhook and Web Push deliveries in the last 24 hours by topic.\n# TYPE clrd_delivery_failures_24h gauge\n")
	sort.Slice(deliv, func(i, j int) bool { return deliv[i].Topic < deliv[j].Topic })
	for _, d := range deliv {
		if d.Webhooks > 0 {
			fmt.Fprintf(&b, "clrd_delivery_failures_24h{kind=\"webhook\",topic=\"%s\"} %d\n", promLabel(d.Topic), d.Webhooks)
		}
		if d.WebPush > 0 {
			fmt.Fprintf(&b, "clrd_delivery_failures_24h{kind=\"web_push\",topic=\"%s\"} %d\n", promLabel(d.Topic), d.WebPush)
		}
	}
	fmt.Fprintf(&b, "# HELP clrd_events_seq Latest event sequence number.\n# TYPE clrd_events_seq counter\nclrd_events_seq %d\n", s.CurrentSeq())
	fmt.Fprintf(&b, "# HELP clrd_jobs_failing Number of background jobs currently failing.\n# TYPE clrd_jobs_failing gauge\nclrd_jobs_failing %d\n", failingJobs)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(200)
	w.Write(b.Bytes())
	return nil
}
