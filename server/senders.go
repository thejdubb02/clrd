package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Senders (#119): every token tree that publishes, listed from its first
// message on, so an admin sees every service still talking. Nothing to set up.

// senderHint is what a request says about its sender: hints only, never trusted.
type senderHint struct{ ua, addr string }

type senderHintKey struct{}

const (
	senderRecent    = 10  // notification ids kept per sender
	senderAnonLimit = 500 // anonymous senders kept; the longest quiet goes first
)

// Sender is one publisher as GET /api/v1/senders shows it.
type Sender struct {
	ID        string          `json:"id"`
	TokenID   string          `json:"token_id"` // "" for an anonymous sender
	Name      string          `json:"name"`
	Kind      string          `json:"kind"` // token, person, ntfy_user, gotify_app, pushover_app, anonymous
	Template  string          `json:"template"`
	UserAgent string          `json:"user_agent"`
	Address   string          `json:"address"`
	FirstSeen time.Time       `json:"first_seen"`
	LastSeen  time.Time       `json:"last_seen"`
	Count     int             `json:"count"`
	Topics    map[string]int  `json:"topics"`
	Recent    []*Notification `json:"recent,omitempty"`
	recent    []string
}

// seeSender counts one stored publish for the sender whose top token is top,
// in the publish's own write. clrd's own alerts (top "") are no sender.
func seeSender(ctx context.Context, tx *sql.Tx, top, topic, template, id string) error {
	if top == "" {
		return nil
	}
	h, _ := ctx.Value(senderHintKey{}).(senderHint)
	key, tokID := top, any(top)
	if top == anonymousID || strings.HasPrefix(top, anonymousID+"-") { // one shared token: one sender per address
		sum := sha256.Sum256([]byte(h.addr))
		key, tokID = anonymousID+"-"+hex.EncodeToString(sum[:6]), nil
	}
	var topicsJSON, recentJSON string
	err := tx.QueryRowContext(ctx, `SELECT topics, recent FROM senders WHERE id = ?`, key).Scan(&topicsJSON, &recentJSON)
	if errors.Is(err, sql.ErrNoRows) {
		topicsJSON, recentJSON = "{}", "[]"
		if tokID == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM senders WHERE id IN (SELECT id FROM senders WHERE token_id IS NULL
				ORDER BY last_seen DESC LIMIT -1 OFFSET ?)`, senderAnonLimit-1); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	topics, recent := map[string]int{}, []string{}
	json.Unmarshal([]byte(topicsJSON), &topics)
	json.Unmarshal([]byte(recentJSON), &recent)
	topics[topic]++
	recent = append([]string{id}, slices.DeleteFunc(recent, func(r string) bool { return r == id })...)
	recent = recent[:min(len(recent), senderRecent)]
	tb, _ := json.Marshal(topics)
	rb, _ := json.Marshal(recent)
	t := ms(now())
	// A token revoked mid-publish is no sender, never a failed publish.
	_, err = tx.ExecContext(ctx, `INSERT INTO senders (id, token_id, user_agent, address, template, first_seen, last_seen, count, topics, recent)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?6, 1, ?7, ?8 WHERE ?2 IS NULL OR EXISTS (SELECT 1 FROM tokens WHERE id = ?2)
		ON CONFLICT(id) DO UPDATE SET user_agent = excluded.user_agent, address = excluded.address,
		  template = CASE WHEN ?5 = '' THEN template ELSE ?5 END, last_seen = ?6, count = count + 1, topics = ?7, recent = ?8`,
		key, tokID, h.ua, h.addr, template, t, string(tb), string(rb))
	return err
}

const senderCols = `s.id, COALESCE(s.token_id, ''), COALESCE(t.name, 'anonymous'),
  CASE WHEN s.token_id IS NULL THEN 'anonymous'
    WHEN EXISTS (SELECT 1 FROM users u WHERE u.token_id = s.token_id) THEN 'ntfy_user'
    WHEN EXISTS (SELECT 1 FROM gotify_apps g WHERE g.token_id = s.token_id) THEN 'gotify_app'
    WHEN s.token_id LIKE 'pushover-%' THEN 'pushover_app'
    WHEN t.user_id IS NOT NULL THEN 'person' ELSE 'token' END,
  s.template, s.user_agent, s.address, s.first_seen, s.last_seen, s.count, s.topics, s.recent
  FROM senders s LEFT JOIN tokens t ON t.id = s.token_id`

func scanSender(r scanner) (*Sender, error) {
	var sd Sender
	var first, last int64
	var topics, recent string
	if err := r.Scan(&sd.ID, &sd.TokenID, &sd.Name, &sd.Kind, &sd.Template, &sd.UserAgent, &sd.Address,
		&first, &last, &sd.Count, &topics, &recent); err != nil {
		return nil, err
	}
	sd.FirstSeen, sd.LastSeen = fromMS(first), fromMS(last)
	json.Unmarshal([]byte(topics), &sd.Topics)
	json.Unmarshal([]byte(recent), &sd.recent)
	return &sd, nil
}

// Senders lists every sender, most recently heard first. Full admin only:
// the list names every token and the addresses they send from.
func (s *Server) Senders(ctx context.Context, tok *Token) ([]*Sender, error) {
	if err := tok.needFullAdmin(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+senderCols+` ORDER BY s.last_seen DESC, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Sender{}
	for rows.Next() {
		sd, err := scanSender(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sd)
	}
	return out, rows.Err()
}

// Sender is one sender with its recent notifications that are still stored, newest first.
func (s *Server) Sender(ctx context.Context, tok *Token, id string) (*Sender, error) {
	if err := tok.needFullAdmin(); err != nil {
		return nil, err
	}
	sd, err := scanSender(s.db.QueryRowContext(ctx, `SELECT `+senderCols+` WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(404, "not_found", "no sender %q", id)
	} else if err != nil {
		return nil, err
	}
	sd.Recent = []*Notification{}
	for _, nid := range sd.recent {
		n, err := getNotification(ctx, s.db, `id = ?`, nid)
		if isNotFound(err) || (err == nil && sd.TokenID != "" && n.sender != sd.TokenID) {
			continue // purged, cleared away, or replaced by another sender's external_id
		} else if err != nil {
			return nil, err
		}
		sd.Recent = append(sd.Recent, n)
	}
	return sd, nil
}

func (s *Server) senderRoutes() {
	s.api("GET /api/v1/senders", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		out, err := s.Senders(r.Context(), tok)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"senders": out})
		return nil
	})
	s.api("GET /api/v1/senders/{id}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		sd, err := s.Sender(r.Context(), tok, r.PathValue("id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, sd)
		return nil
	})
}
