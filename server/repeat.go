package server

import (
	"context"
	"database/sql"
	"time"
)

// Repeat re-alerts an active notification until someone acts on it (issue #80):
// every Every the server logs a "repeat" event, up to the number of times
// asked. Acknowledge, snooze, dismiss or resolve stops it for good.
type Repeat struct {
	Every string     `json:"every"` // as given: "5m", "1h"
	Left  int        `json:"left"`  // re-alerts still to come
	Count int        `json:"count"` // re-alerts sent so far
	Next  *time.Time `json:"next"`  // null once it stopped
}

const (
	repeatMin          = time.Minute
	repeatMax          = 24 * time.Hour
	repeatTimesDefault = 10
	repeatTimesMax     = 100
)

// parseRepeat reads a publish's repeat and repeat_times. The first re-alert
// is one interval after from (the publish, or a scheduled delivery).
func parseRepeat(every string, times int, from time.Time) (*Repeat, error) {
	if every == "" {
		if times != 0 {
			return nil, errf(400, "bad_request", `repeat_times needs "repeat", like "5m"`)
		}
		return nil, nil
	}
	d, err := parseDuration(every)
	if err != nil || d < repeatMin || d > repeatMax {
		return nil, errf(400, "bad_request", `repeat must be a duration from 1m to 24h, like "5m"`)
	}
	if times == 0 {
		times = repeatTimesDefault
	}
	if times < 1 || times > repeatTimesMax {
		return nil, errf(400, "bad_request", "repeat_times must be 1 to %d", repeatTimesMax)
	}
	next := from.Add(d)
	return &Repeat{Every: every, Left: times, Next: &next}, nil
}

// stopRepeat ends the re-alerts; the count stays as history.
func (n *Notification) stopRepeat() {
	if n.Repeat != nil {
		n.Repeat.Next = nil
	}
}

// repeatDue logs a "repeat" event for every active notification whose next
// re-alert has come.
// ponytail: scans every active repeating row each tick; add a next-time column and index if that grows large.
func (s *Server) repeatDue(ctx context.Context) error {
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		ns, err := queryNotifications(tx, `SELECT `+notifCols+` FROM notifications
			WHERE state = 'active' AND repeat != '' AND json_extract(repeat, '$.next') IS NOT NULL
			AND (expires_at IS NULL OR expires_at > ?)`, ms(time.Now()))
		if err != nil {
			return nil, err
		}
		t := now()
		var evs []Event
		for _, n := range ns {
			r := n.Repeat
			if r == nil || r.Next == nil || r.Next.After(t) {
				continue
			}
			d, _ := parseDuration(r.Every) // checked on publish
			r.Count++
			r.Left--
			r.Next = nil
			if r.Left > 0 {
				next := t.Add(d)
				r.Next = &next
			}
			evs = append(evs, Event{Type: EventRepeat, Notification: n})
		}
		return evs, nil
	})
	return err
}
