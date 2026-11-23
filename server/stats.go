package server

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"sort"
	"strconv"
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
	Open          int    `json:"open"` // not cleared yet: the ones nobody dealt with
	Cleared       int    `json:"cleared"`
	ClearMedianMS int64  `json:"clear_median_ms"`
	ClearP90MS    int64  `json:"clear_p90_ms"`
	clear         []int64
}

// StatsBucket is the count published in one UTC hour.
type StatsBucket struct {
	At    int64 `json:"at"`
	Count int   `json:"count"`
}

// StatsFlapper is an external id that fired more than once in the range.
type StatsFlapper struct {
	Topic      string `json:"topic"`
	ExternalID string `json:"external_id"`
	Count      int    `json:"count"`
}

type Stats struct {
	From       int64          `json:"from"`
	To         int64          `json:"to"`
	Total      int            `json:"total"`
	Answered   int            `json:"answered"` // a reply action was tapped
	Priorities map[string]int `json:"priorities"`
	Hours      []StatsBucket  `json:"hours"`
	Topics     []*StatsGroup  `json:"topics"`
	Senders    []*StatsGroup  `json:"senders"`
	ClearedBy  []*StatsGroup  `json:"cleared_by"`
	Flappers   []StatsFlapper `json:"flappers"`
}

// Stats counts notifications created in [from, to). Admin with no topic list.
func (s *Server) Stats(ctx context.Context, tok *Token, from, to time.Time) (*Stats, error) {
	if err := tok.needFullAdmin(); err != nil {
		return nil, err
	}
	if !from.Before(to) || to.Sub(from) > statsMaxRange {
		return nil, errf(400, "bad_request", "from must be before to, at most 366 days apart")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT topic, sender, external_id, priority, state, state_by, created_at, cleared_at, reply != ''
		FROM notifications WHERE created_at >= ? AND created_at < ?`, ms(from), ms(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	st := &Stats{From: ms(from), To: ms(to), Priorities: map[string]int{}}
	topics, senders, by := map[string]*StatsGroup{}, map[string]*StatsGroup{}, map[string]*StatsGroup{}
	hours, flaps := map[int64]int{}, map[[2]string]int{}
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
		var topic, sender, ext, state, stateBy string
		var prio int
		var created int64
		var cleared sql.NullInt64
		var answered bool
		if err := rows.Scan(&topic, &sender, &ext, &prio, &state, &stateBy, &created, &cleared, &answered); err != nil {
			return nil, err
		}
		st.Total++
		if answered {
			st.Answered++
		}
		st.Priorities[strconv.Itoa(prio)]++
		hours[created-created%int64(time.Hour/time.Millisecond)]++
		add(topics, topic, cleared, created)
		add(senders, sender, cleared, created)
		if cleared.Valid {
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
	for at, k := range hours {
		st.Hours = append(st.Hours, StatsBucket{at, k})
	}
	sort.Slice(st.Hours, func(i, j int) bool { return st.Hours[i].At < st.Hours[j].At })
	st.Topics, st.Senders, st.ClearedBy = top(topics), top(senders), top(by)
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
	return st, nil
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
		st, err := s.Stats(r.Context(), tok, from, to)
		if err != nil {
			return err
		}
		writeJSON(w, 200, st)
		return nil
	})
}
