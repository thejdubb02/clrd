package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Subscriptions (#617, docs/SPEC.md section 2.3): which topics reach one
// owner's devices. Only a view filter: nothing here changes a notification,
// and the ntfy routes, webhooks and other readers never see it.

// EventSubscriptions is the event every change of an owner's set appends.
// Its topic column is subMark and the owner, never a valid topic name:
// visible refuses it for every reader before any access check, and only the
// owner's filtered streams pick it up, by that name (view.changedBy).
const EventSubscriptions = "subscriptions"

const subMark = "!"

// view is one reader's subscriptions filter.
type view struct {
	owner     string // "u:<user id>" or "t:<top token id>"
	filtered  bool   // a device that follows the set; a typed-in token with no user is not
	all       bool   // subscribed=all: shows every readable topic
	newTopics bool
	rows      map[string]bool
	seq       int64 // the owner's last subscriptions event
}

func (v *view) subscribed(topic string) bool {
	if on, ok := v.rows[topic]; ok {
		return on
	}
	return v.newTopics
}

// hides reports whether v leaves topic out. A nil view hides nothing.
func (v *view) hides(topic string) bool {
	return v != nil && v.filtered && !v.all && !v.subscribed(topic)
}

// hiding reports whether v may hide any topic at all.
func (v *view) hiding() bool {
	if v == nil || !v.filtered || v.all {
		return false
	}
	for _, on := range v.rows {
		if !on {
			return true
		}
	}
	return !v.newTopics
}

// changedBy reports e as a change of v's own set.
func (v *view) changedBy(e Event) bool {
	return v != nil && v.filtered && e.Notification.Topic == subMark+v.owner
}

// ownerOf is whose set tok follows, and whether it follows it at all.
func (s *Server) ownerOf(ctx context.Context, tok *Token) (string, bool, error) {
	if tok.User != "" {
		return "u:" + tok.User, true, nil
	}
	top, err := s.topOf(ctx, tok.ID)
	return "t:" + top, top != tok.ID, err
}

// withView is tok with its view loaded fresh; all is subscribed=all.
func (s *Server) withView(ctx context.Context, tok *Token, all bool) (*Token, error) {
	owner, filtered, err := s.ownerOf(ctx, tok)
	if err != nil {
		return nil, err
	}
	v := &view{owner: owner, filtered: filtered, all: all, newTopics: true, rows: map[string]bool{}}
	c := *tok
	c.view = v
	// One snapshot, so new_topics, seq and the rows are one version of the set.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Not filtered, only the last move counts: a token just detached resets
	// on a stale since, but its browsers changing the set never resets it (#635).
	var nt bool
	var moved int64
	err = tx.QueryRowContext(ctx, `SELECT new_topics, seq, moved FROM sub_owners WHERE owner = ?`, owner).Scan(&nt, &v.seq, &moved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if !filtered {
		v.seq = moved
		return &c, nil
	}
	if err == nil {
		v.newTopics = nt
	}
	rows, err := tx.QueryContext(ctx, `SELECT topic, subscribed FROM subscriptions WHERE owner = ?`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var on bool
		if err := rows.Scan(&t, &on); err != nil {
			return nil, err
		}
		v.rows[t] = on
	}
	return &c, rows.Err()
}

// viewed is tok with a view: its own, unless all asks for more than it has.
func (s *Server) viewed(ctx context.Context, tok *Token, all bool) (*Token, error) {
	if tok.view != nil && (tok.view.all || !all) {
		return tok, nil
	}
	return s.withView(ctx, tok, all)
}

// SubSettings is "subscriptions" in the settings view (section 2.1).
type SubSettings struct {
	Filtered  bool            `json:"filtered"`
	NewTopics *bool           `json:"new_topics,omitempty"`
	Topics    map[string]bool `json:"topics"`
}

// A stream's own view is used as it is, so its settings match its filter.
func (s *Server) subSettings(ctx context.Context, tok *Token) (*SubSettings, error) {
	tok, err := s.viewed(ctx, tok, false)
	if err != nil {
		return nil, err
	}
	v := tok.view
	out := &SubSettings{Filtered: v.filtered, Topics: map[string]bool{}}
	if v.filtered {
		out.NewTopics = &v.newTopics
		for t, on := range v.rows {
			if tok.may(ScopeRead, t) {
				out.Topics[t] = on
			}
		}
	}
	return out, nil
}

// SubTopic is one topic in GET /api/v1/subscriptions.
type SubTopic struct {
	Topic      string     `json:"topic"`
	Subscribed bool       `json:"subscribed"`
	Open       int        `json:"open"`
	LastAt     *time.Time `json:"last_at"`
	Approvals  bool       `json:"approvals"`
}

// SubscriptionList is GET /api/v1/subscriptions. Filtered false is a
// typed-in token with no user: it sees every topic, so Topics is empty.
type SubscriptionList struct {
	Filtered  bool       `json:"filtered"`
	NewTopics *bool      `json:"new_topics,omitempty"`
	Topics    []SubTopic `json:"topics"`
}

// Subscriptions is tok's owner's set over every topic tok can read.
func (s *Server) Subscriptions(ctx context.Context, tok *Token) (SubscriptionList, error) {
	out := SubscriptionList{Topics: []SubTopic{}}
	if err := tok.need(ScopeRead, ""); err != nil {
		return out, err
	}
	tok, err := s.withView(ctx, tok, true)
	if err != nil || !tok.view.filtered {
		return out, err
	}
	out.Filtered, out.NewTopics = true, &tok.view.newTopics
	ts, err := s.Topics(ctx, tok)
	for _, t := range ts {
		out.Topics = append(out.Topics, SubTopic{t.Topic, t.Subscribed, t.Open, t.LastAt, t.Approvals})
	}
	return out, err
}

// SetSubscriptions changes tok's owner's set: newTopics if not nil, and
// each topic in topics. Errors are apiErrors (PublicError gives the
// status): 400 for no change asked, too many or a bad name, 403 for a topic
// tok cannot read, 409 for a token no set belongs to. Nothing changes
// when any of them fails.
func (s *Server) SetSubscriptions(ctx context.Context, tok *Token, newTopics *bool, topics map[string]bool) (SubscriptionList, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return SubscriptionList{}, err
	}
	owner, filtered, err := s.ownerOf(ctx, tok)
	if err != nil {
		return SubscriptionList{}, err
	}
	if !filtered {
		return SubscriptionList{}, errf(409, "conflict", "this token has no user to store topic choices for: attach it to a user, or sign in or link a device with it")
	}
	if newTopics == nil && topics == nil {
		return SubscriptionList{}, errf(400, "bad_request", `body needs "new_topics" or "topics"`)
	}
	if len(topics) > 500 {
		return SubscriptionList{}, errf(400, "bad_request", "topics: at most 500 in one call")
	}
	names := slices.Sorted(maps.Keys(topics))
	for _, t := range names {
		if err := validTopic(t); err != nil {
			return SubscriptionList{}, err
		}
		if !tok.may(ScopeRead, t) {
			return SubscriptionList{}, errf(403, "forbidden", "token may not read topic %q", t)
		}
	}
	var readers []*Token // for the backfill: any of the owner's tokens that can read a topic
	if newTopics != nil && !*newTopics {
		if readers, err = s.ownerReaders(ctx, owner); err != nil {
			return SubscriptionList{}, err
		}
	}
	err = s.changeSubs(ctx, owner, newTopics, false, func(tx *sql.Tx, cur bool) error {
		if readers != nil && cur { // turning new_topics off keeps every topic seen so far as it is
			rows, err := tx.QueryContext(ctx, `SELECT topic FROM notifications UNION SELECT topic FROM topics
			  EXCEPT SELECT topic FROM subscriptions WHERE owner = ?`, owner)
			if err != nil {
				return err
			}
			var keep []string
			for rows.Next() {
				var t string
				if rows.Scan(&t) == nil && slices.ContainsFunc(readers, func(r *Token) bool { return r.may(ScopeRead, t) }) {
					keep = append(keep, t)
				}
			}
			rows.Close()
			for _, t := range keep {
				if _, err := tx.ExecContext(ctx, `INSERT INTO subscriptions (owner, topic, subscribed) VALUES (?, ?, 1)`, owner, t); err != nil {
					return err
				}
			}
		}
		for _, t := range names {
			if _, err := tx.ExecContext(ctx, `INSERT INTO subscriptions (owner, topic, subscribed) VALUES (?, ?, ?)
			  ON CONFLICT (owner, topic) DO UPDATE SET subscribed = excluded.subscribed`, owner, t, topics[t]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SubscriptionList{}, err
	}
	return s.Subscriptions(ctx, tok)
}

// changeSubs runs fn (if set) with the owner's current new_topics, stores
// newTopics (if set) and appends the owner's subscriptions event, in one
// transaction under s.mu as write does, so seq order stays delivery order.
// moved marks an attach or detach. The event goes to the hub only: no Web
// Push, no webhook.
func (s *Server) changeSubs(ctx context.Context, owner string, newTopics *bool, moved bool, fn func(tx *sql.Tx, cur bool) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cur := true
	if err := tx.QueryRowContext(ctx, `SELECT new_topics FROM sub_owners WHERE owner = ?`, owner).Scan(&cur); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if fn != nil {
		if err := fn(tx, cur); err != nil {
			return err
		}
	}
	if newTopics != nil {
		cur = *newTopics
	}
	e := Event{Type: EventSubscriptions, Notification: &Notification{Topic: subMark + owner}}
	res, err := tx.ExecContext(ctx, `INSERT INTO events (type, topic, data, created_at) VALUES (?, ?, '', ?)`, e.Type, e.Notification.Topic, ms(now()))
	if err != nil {
		return err
	}
	e.Seq, _ = res.LastInsertId()
	e.Raw = fmt.Appendf(nil, `{"seq":%d,"type":%q}`, e.Seq, e.Type)
	if _, err := tx.ExecContext(ctx, `UPDATE events SET data = ? WHERE seq = ?`, string(e.Raw), e.Seq); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sub_owners (owner, new_topics, seq, moved) VALUES (?, ?, ?, CASE WHEN ? THEN ?3 ELSE 0 END)
	  ON CONFLICT (owner) DO UPDATE SET new_topics = excluded.new_topics, seq = excluded.seq,
	  moved = CASE WHEN ?4 THEN excluded.seq ELSE sub_owners.moved END`, owner, cur, e.Seq, moved); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.seq.Store(e.Seq)
	s.hub.publish(e)
	return nil
}

// ownerReaders is the typed-in tokens of owner, with their user's access.
func (s *Server) ownerReaders(ctx context.Context, owner string) ([]*Token, error) {
	kind, id, _ := strings.Cut(owner, ":")
	where := `id = ?`
	if kind == "u" {
		where = `user_id = ? ORDER BY created_at`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE `+where, id)
	if err != nil {
		return nil, err
	}
	var out []*Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	err = rows.Err()
	rows.Close()
	for _, t := range out {
		if err != nil {
			break
		}
		err = s.withUser(ctx, t)
	}
	return out, err
}

// subsFor is the token whose set a request reads or sets: tok, or with
// ?for= (full admin only) that token, or for a user id one token that may
// read what any of the user's typed-in tokens may.
func (s *Server) subsFor(r *http.Request, tok *Token) (*Token, error) {
	id := r.URL.Query().Get("for")
	if id == "" {
		return tok, nil
	}
	if err := tok.needFullAdmin(); err != nil {
		return nil, err
	}
	ts, err := s.ownerReaders(r.Context(), "u:"+id)
	if err == nil && len(ts) > 0 {
		u := &Token{ID: ts[0].ID, User: id, Scopes: []string{}, union: ts}
		if slices.ContainsFunc(ts, func(t *Token) bool { return t.Has(ScopeRead) }) {
			u.Scopes = []string{ScopeRead}
		}
		return u, nil
	}
	if err == nil {
		ts, err = s.ownerReaders(r.Context(), "t:"+id)
	}
	if err == nil && len(ts) == 0 {
		err = errf(404, "not_found", "no user or token %q (a user needs a typed-in token first)", id)
	}
	if err != nil {
		return nil, err
	}
	return ts[0], nil
}

// subscribedAll reads subscribed=all on the routes the filter covers.
func subscribedAll(r *http.Request) bool { return r.URL.Query().Get("subscribed") == "all" }

func (s *Server) subsRoutes() {
	s.api("GET /api/v1/subscriptions", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		t, err := s.subsFor(r, tok)
		if err != nil {
			return err
		}
		out, err := s.Subscriptions(r.Context(), t)
		if err != nil {
			return err
		}
		writeJSON(w, 200, out)
		return nil
	})
	s.api("PUT /api/v1/subscriptions", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var in struct {
			NewTopics *bool            `json:"new_topics"`
			Topics    map[string]*bool `json:"topics"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		var topics map[string]bool // nil when not given, so "no change asked" stays a 400
		if in.Topics != nil {
			topics = map[string]bool{}
		}
		for t, on := range in.Topics {
			if on == nil { // null would read as false and unsubscribe
				return errf(400, "bad_request", "topics: %q must be true or false, not null", t)
			}
			topics[t] = *on
		}
		t, err := s.subsFor(r, tok)
		if err != nil {
			return err
		}
		out, err := s.SetSubscriptions(r.Context(), t, in.NewTopics, topics)
		if err != nil {
			return err
		}
		writeJSON(w, 200, out)
		return nil
	})
}
