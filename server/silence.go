package server

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"time"
	"unicode/utf8"
)

// Silence mutes a topic, a tag, or a tag within a topic until a set time
// (issue #81): notifications still arrive, sync and show, but every event
// written while one matches carries "silenced": true, so clients do not alert.
// It ends by itself; DELETE ends it early.
type Silence struct {
	ID        string    `json:"id"`
	Topic     string    `json:"topic"` // "" = every topic (needs a token without a topic list)
	Tag       string    `json:"tag"`   // "" = every notification in the topic
	Until     time.Time `json:"until"`
	Comment   string    `json:"comment"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

const maxSilence = 8784 * time.Hour // a year, like check-ins

// matches: a silence with a topic needs that topic; one with a tag needs that tag.
func (sl Silence) matches(n *Notification) bool {
	return (sl.Topic == "" || sl.Topic == n.Topic) && (sl.Tag == "" || slices.Contains(n.Tags, sl.Tag))
}

func silenced(sils []Silence, n *Notification) bool {
	return slices.ContainsFunc(sils, func(sl Silence) bool { return sl.matches(n) })
}

// needSilence: publish on the topic; a silence across every topic needs a
// token that is not limited to some topics.
func (tok *Token) needSilence(topic string) error {
	if err := tok.need(ScopePublish, topic); err != nil {
		return err
	}
	if topic == "" && tok.limited() {
		return errf(403, "forbidden", "a silence without a topic needs a token without a topic list")
	}
	return nil
}

func activeSilences(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, at time.Time) ([]Silence, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, topic, tag, until_ms, comment, created_by, created_at
		FROM silences WHERE until_ms > ? ORDER BY until_ms`, ms(at))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Silence
	for rows.Next() {
		var sl Silence
		var until, created int64
		if err := rows.Scan(&sl.ID, &sl.Topic, &sl.Tag, &until, &sl.Comment, &sl.CreatedBy, &created); err != nil {
			return nil, err
		}
		sl.Until, sl.CreatedAt = fromMS(until), fromMS(created)
		out = append(out, sl)
	}
	return out, rows.Err()
}

// Silences lists the running silences tok may read, soonest end first.
func (s *Server) Silences(ctx context.Context, tok *Token) ([]Silence, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return nil, err
	}
	all, err := activeSilences(ctx, s.db, time.Now())
	if err != nil {
		return nil, err
	}
	out := []Silence{}
	for _, sl := range all {
		if tok.may(ScopeRead, sl.Topic) {
			out = append(out, sl)
		}
	}
	return out, nil
}

func (s *Server) silenceRoutes() {
	s.api("GET /api/v1/silences", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		out, err := s.Silences(r.Context(), tok)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"silences": out})
		return nil
	})
	s.api("POST /api/v1/silences", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var in struct {
			Topic, Tag, For, Comment string
			Until                    *time.Time
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.Topic == "" && in.Tag == "" {
			return errf(400, "bad_request", `a silence needs a "topic", a "tag" or both`)
		}
		if in.Topic != "" {
			if err := validTopic(in.Topic); err != nil {
				return err
			}
		}
		if utf8.RuneCountInString(in.Tag) > 64 || utf8.RuneCountInString(in.Comment) > 256 {
			return errf(400, "bad_request", "tag is at most 64 characters, comment at most 256")
		}
		if err := tok.needSilence(in.Topic); err != nil {
			return err
		}
		t := now()
		var until time.Time
		switch {
		case in.For != "" && in.Until == nil:
			d, err := parseDuration(in.For)
			if err != nil {
				return errf(400, "bad_request", `for must be a duration like "2h" or "30m"`)
			}
			until = t.Add(d)
		case in.For == "" && in.Until != nil:
			until = *in.Until
		default:
			return errf(400, "bad_request", `give exactly one of "for" (like "2h") or "until" (RFC3339)`)
		}
		until = until.UTC().Truncate(time.Millisecond) // as stored, so "1ns" is not a silence that never matches
		if !until.After(t) || until.Sub(t) > maxSilence {
			return errf(400, "bad_request", "a silence must end in the future and within 8784h")
		}
		sl := Silence{ID: randomString(12), Topic: in.Topic, Tag: in.Tag, Until: until, Comment: in.Comment,
			CreatedBy: tok.Name, CreatedAt: t}
		if _, err := s.db.ExecContext(r.Context(), `INSERT INTO silences (id, topic, tag, until_ms, comment, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, sl.ID, sl.Topic, sl.Tag, ms(sl.Until), sl.Comment, sl.CreatedBy, ms(sl.CreatedAt)); err != nil {
			return err
		}
		writeJSON(w, 201, sl)
		return nil
	})
	s.api("DELETE /api/v1/silences/{id}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var topic string
		err := s.db.QueryRowContext(r.Context(), `SELECT topic FROM silences WHERE id = ? AND until_ms > ?`,
			r.PathValue("id"), ms(time.Now())).Scan(&topic)
		if err == sql.ErrNoRows || (err == nil && topic != "" && !tok.Allows(topic)) {
			return errf(404, "not_found", "silence not found")
		}
		if err != nil {
			return err
		}
		if err := tok.needSilence(topic); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), `DELETE FROM silences WHERE id = ?`, r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(204)
		return nil
	})
}
