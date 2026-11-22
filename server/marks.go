package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"
	"unicode/utf8"
)

// Marks (card marks): anyone who may read a topic can mark one of its
// notifications seen, or actioned with a short note and link, so the others
// see who already looked at it or dealt with it. A mark never changes the
// state, so it clears nothing for anyone. One mark per author: setting it
// again replaces it, and only its author changes or removes it.

const (
	MarkSeen     = "seen"
	MarkActioned = "actioned"
	maxMarkNote  = 200
)

type Mark struct {
	By   string    `json:"by"`    // the author's name
	ByID string    `json:"by_id"` // the person's id, or the top token's for a token with no person
	Mark string    `json:"mark"`  // seen or actioned
	Note string    `json:"note,omitempty"`
	Link string    `json:"link,omitempty"`
	At   time.Time `json:"at"`
}

// Marks is never null in JSON, so a client can always range over it.
type Marks []Mark

func (m Marks) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]Mark(m))
}

// markAuthor is who a mark is from: the person a token belongs to, else the
// token typed in (a phone's session token marks as its parent).
func markAuthor(ctx context.Context, q querier, tok *Token) (id, name string, err error) {
	if tok.User != "" {
		err = q.QueryRowContext(ctx, `SELECT id, name FROM people WHERE id = ?`, tok.User).Scan(&id, &name)
		return
	}
	if id, err = topIn(ctx, q, tok.ID); err != nil {
		return
	}
	err = q.QueryRowContext(ctx, `SELECT name FROM tokens WHERE id = ?`, id).Scan(&name)
	return
}

// SetMark sets tok's mark on notification id. kind "" removes it.
func (s *Server) SetMark(ctx context.Context, tok *Token, id, kind, note, link string) (*Notification, error) {
	if tok == nil {
		return nil, errf(401, "unauthorized", noToken)
	}
	switch kind {
	case MarkSeen, MarkActioned, "":
	default:
		return nil, errf(400, "bad_request", "mark must be seen or actioned")
	}
	if utf8.RuneCountInString(note) > maxMarkNote {
		return nil, errf(400, "bad_request", "note must be at most %d characters", maxMarkNote)
	}
	if err := checkImageURL("link", link); err != nil {
		return nil, err
	}
	var out *Notification
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		n, err := getNotification(ctx, tx, `id = ?`, id)
		if err != nil {
			return nil, err
		}
		if err := tok.need(ScopeRead, n.Topic); err != nil {
			return nil, err
		}
		by, name, err := markAuthor(ctx, tx, tok)
		if err != nil {
			return nil, err
		}
		out = n
		i := slices.IndexFunc(n.Marks, func(m Mark) bool { return m.ByID == by })
		if kind == "" {
			if i < 0 {
				return nil, nil
			}
			n.Marks = slices.Delete(n.Marks, i, i+1)
		} else {
			m := Mark{By: name, ByID: by, Mark: kind, Note: note, Link: link, At: now()}
			if i >= 0 {
				if old := n.Marks[i]; old.By == m.By && old.Mark == m.Mark && old.Note == m.Note && old.Link == m.Link {
					return nil, nil // the same again: no event
				}
				n.Marks[i] = m
			} else {
				n.Marks = append(n.Marks, m)
			}
		}
		return []Event{{Type: EventMark, Notification: n}}, nil
	})
	return out, err
}
