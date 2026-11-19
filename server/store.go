package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver, no cgo
)

// Notification states (spec section 2).
const (
	Active       = "active"
	Acknowledged = "acknowledged"
	Snoozed      = "snoozed"
	Dismissed    = "dismissed"
	Resolved     = "resolved"
)

// Event types (spec section 3).
const (
	EventCreated = "created"
	EventUpdated = "updated"
	EventState   = "state"
)

const schema = `
CREATE TABLE IF NOT EXISTS notifications (
  id TEXT PRIMARY KEY,
  topic TEXT NOT NULL,
  external_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL DEFAULT '',
  priority INTEGER NOT NULL DEFAULT 3,
  tags TEXT NOT NULL DEFAULT '[]',
  click TEXT NOT NULL DEFAULT '',
  actions TEXT NOT NULL DEFAULT '[]',
  reply TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  state_by TEXT NOT NULL DEFAULT '',
  snooze_until INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  seq INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS notifications_open_key ON notifications(topic, external_id)
  WHERE external_id != '' AND state NOT IN ('dismissed', 'resolved');
CREATE INDEX IF NOT EXISTS notifications_topic ON notifications(topic, state);
CREATE INDEX IF NOT EXISTS notifications_snoozed ON notifications(snooze_until) WHERE state = 'snoozed';
CREATE TABLE IF NOT EXISTS events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL,
  topic TEXT NOT NULL,
  data TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tokens (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  hash TEXT NOT NULL UNIQUE,
  scopes TEXT NOT NULL,
  topics TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER
);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
-- A web sign-in mints its own token; parent_id is the token typed in.
CREATE TABLE IF NOT EXISTS sessions (
  token_id TEXT PRIMARY KEY REFERENCES tokens(id) ON DELETE CASCADE,
  parent_id TEXT NOT NULL
);
`

// Action is a button on a notification: a link (URL) or an answer (Reply)
// that POST /api/v1/notifications/{id}/reply records.
type Action struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Reply string `json:"reply,omitempty"`
}

// Notification is the server-owned record every client mirrors.
type Notification struct {
	ID          string     `json:"id"`
	Topic       string     `json:"topic"`
	ExternalID  string     `json:"external_id"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	Priority    int        `json:"priority"`
	Tags        []string   `json:"tags"`
	Click       string     `json:"click"`
	Actions     []Action   `json:"actions"`
	Reply       string     `json:"reply"` // the reply action tapped, "" until one is
	State       string     `json:"state"`
	StateBy     string     `json:"state_by"`
	SnoozeUntil *time.Time `json:"snooze_until"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Seq         int64      `json:"seq"`
}

// Final reports whether the notification has left every inbox.
func (n *Notification) Final() bool { return n.State == Dismissed || n.State == Resolved }

// Event is one entry of the append-only log. Raw is its JSON as stored.
type Event struct {
	Seq          int64         `json:"seq"`
	Type         string        `json:"type"`
	Notification *Notification `json:"notification"`
	Raw          []byte        `json:"-"`
}

var topicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Reserved topic names: the server and mounted pages own these paths.
var reserved = map[string]bool{"api": true, "app": true, "assets": true, "docs": true, "healthz": true,
	"login": true, "logout": true, "mcp": true, "setup": true, "static": true, "v1": true}

func validTopic(t string) error {
	if !topicRe.MatchString(t) {
		return errf(400, "bad_request", "invalid topic %q: use letters, digits, _ and -, at most 64", t)
	}
	if reserved[strings.ToLower(t)] {
		return errf(400, "bad_request", "topic %q is reserved", t)
	}
	return nil
}

func openDB(path string) (*sql.DB, error) {
	// Pragmas in the DSN apply to every pooled connection. _txlock=immediate
	// takes the write lock at BEGIN, so a read-then-write never hits BUSY.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	// Databases made before reply existed.
	if _, err := db.Exec(`ALTER TABLE notifications ADD COLUMN reply TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("add reply column: %w", err)
	}
	return db, nil
}

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }
func now() time.Time           { return time.Now().UTC().Truncate(time.Millisecond) }

func nullMS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ms(*t)
}

const notifCols = `id, topic, external_id, title, message, priority, tags, click, actions, reply,
  state, state_by, snooze_until, created_at, updated_at, seq`

type scanner interface{ Scan(...any) error }

func scanNotification(r scanner) (*Notification, error) {
	var n Notification
	var tags, actions string
	var snooze sql.NullInt64
	var created, updated int64
	err := r.Scan(&n.ID, &n.Topic, &n.ExternalID, &n.Title, &n.Message, &n.Priority, &tags, &n.Click,
		&actions, &n.Reply, &n.State, &n.StateBy, &snooze, &created, &updated, &n.Seq)
	if err != nil {
		return nil, err
	}
	n.CreatedAt, n.UpdatedAt = fromMS(created), fromMS(updated)
	if snooze.Valid {
		t := fromMS(snooze.Int64)
		n.SnoozeUntil = &t
	}
	if err := json.Unmarshal([]byte(tags), &n.Tags); err != nil {
		return nil, err
	}
	return &n, json.Unmarshal([]byte(actions), &n.Actions)
}

func saveNotification(tx *sql.Tx, n *Notification) error {
	tags, _ := json.Marshal(n.Tags)
	actions, _ := json.Marshal(n.Actions)
	_, err := tx.Exec(`INSERT INTO notifications (`+notifCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title, message=excluded.message,
		priority=excluded.priority, tags=excluded.tags, click=excluded.click, actions=excluded.actions,
		reply=excluded.reply, state=excluded.state, state_by=excluded.state_by, snooze_until=excluded.snooze_until,
		updated_at=excluded.updated_at, seq=excluded.seq`,
		n.ID, n.Topic, n.ExternalID, n.Title, n.Message, n.Priority, string(tags), n.Click, string(actions),
		n.Reply, n.State, n.StateBy, nullMS(n.SnoozeUntil), ms(n.CreatedAt), ms(n.UpdatedAt), n.Seq)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errf(409, "conflict", "an open notification with external_id %q already exists in %q", n.ExternalID, n.Topic)
	}
	return err
}

func getNotification(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, where string, args ...any) (*Notification, error) {
	n, err := scanNotification(q.QueryRowContext(ctx, `SELECT `+notifCols+` FROM notifications WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(404, "not_found", "notification not found")
	}
	return n, err
}

// write runs fn inside one immediate transaction, appends an event for every
// change fn returns, commits, then broadcasts. Holding s.mu across commit and
// broadcast makes seq order equal delivery order, with no gaps.
func (s *Server) write(ctx context.Context, fn func(tx *sql.Tx) ([]Event, error)) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	evs, err := fn(tx)
	if err != nil {
		return nil, err
	}
	t := now()
	for i := range evs {
		e := &evs[i]
		res, err := tx.Exec(`INSERT INTO events (type, topic, data, created_at) VALUES (?, ?, '', ?)`,
			e.Type, e.Notification.Topic, ms(t))
		if err != nil {
			return nil, err
		}
		e.Seq, _ = res.LastInsertId()
		e.Notification.Seq, e.Notification.UpdatedAt = e.Seq, t
		if err := saveNotification(tx, e.Notification); err != nil {
			return nil, err
		}
		if e.Raw, err = json.Marshal(e); err != nil {
			return nil, errf(400, "bad_request", "cannot encode notification: %v", err)
		}
		if _, err := tx.Exec(`UPDATE events SET data = ? WHERE seq = ?`, string(e.Raw), e.Seq); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, e := range evs {
		s.seq.Store(e.Seq)
		s.hub.publish(e)
	}
	return evs, nil
}

// newID returns a monotonic ULID. Callers hold s.mu.
func (s *Server) newID(t time.Time) string {
	if t.UnixMilli() == s.lastMS {
		for i := len(s.lastRand) - 1; i >= 0; i-- { // increment, carrying
			s.lastRand[i]++
			if s.lastRand[i] != 0 {
				break
			}
		}
	} else {
		s.lastMS = t.UnixMilli()
		rand.Read(s.lastRand[:])
	}
	var u [16]byte
	for i := 0; i < 6; i++ {
		u[i] = byte(uint64(s.lastMS) >> (40 - 8*i))
	}
	copy(u[6:], s.lastRand[:])
	return ulidString(u)
}

func ulidString(u [16]byte) string {
	const hex32, crockford = "0123456789abcdefghijklmnopqrstuv", "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	s := new(big.Int).SetBytes(u[:]).Text(32)
	out := []byte(strings.Repeat("0", 26-len(s)) + s)
	for i, c := range out {
		out[i] = crockford[strings.IndexByte(hex32, c)]
	}
	return string(out)
}

// Publish input: the notification fields a sender sets.
type PublishInput struct {
	Topic       string     `json:"topic"`
	ExternalID  string     `json:"external_id"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	Priority    int        `json:"priority"`
	Tags        []string   `json:"tags"`
	Click       string     `json:"click"`
	Actions     []Action   `json:"actions"`
	SnoozeUntil *time.Time `json:"snooze_until"` // optional: deliver later (scheduled send)
	// Status "resolved" (Alertmanager, Grafana, Gatus) resolves the open
	// notification with this external_id instead of creating one; 404 if none.
	Status string `json:"status,omitempty"`
}

// Publish creates a notification, or updates the open one with the same
// external_id in the topic.
func (s *Server) Publish(ctx context.Context, tok *Token, in PublishInput) (*Notification, error) {
	if err := validTopic(in.Topic); err != nil {
		return nil, err
	}
	if err := tok.need(ScopePublish, in.Topic); err != nil {
		return nil, err
	}
	if in.Status == "resolved" {
		if in.ExternalID == "" {
			return nil, errf(400, "bad_request", "status resolved needs an external_id")
		}
		return s.ActByKey(ctx, tok, in.Topic, in.ExternalID, "resolve")
	}
	if in.Priority == 0 {
		in.Priority = 3
	}
	if in.Priority < 1 || in.Priority > 5 {
		return nil, errf(400, "bad_request", "priority must be 1 to 5")
	}
	if len(in.ExternalID) > 256 {
		return nil, errf(400, "bad_request", "external_id longer than 256")
	}
	if in.ExternalID == "." || in.ExternalID == ".." { // a URL path would drop it
		return nil, errf(400, "bad_request", "external_id cannot be . or ..")
	}
	if in.Title == "" && in.Message == "" {
		in.Message = "triggered" // what ntfy sends for an empty body
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	if in.Actions == nil {
		in.Actions = []Action{}
	}
	if err := checkActions(in.Actions); err != nil {
		return nil, err
	}
	evs, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		if in.ExternalID != "" {
			n, err := getNotification(ctx, tx, `topic = ? AND external_id = ? AND state NOT IN ('dismissed', 'resolved')`,
				in.Topic, in.ExternalID)
			if err == nil {
				n.Title, n.Message, n.Priority, n.Tags, n.Click, n.Actions, n.Reply =
					in.Title, in.Message, in.Priority, in.Tags, in.Click, in.Actions, "" // a new question starts unanswered
				if t := now(); in.SnoozeUntil != nil && in.SnoozeUntil.After(t) { // delayed: hide until then, like a new row
					u := in.SnoozeUntil.UTC().Truncate(time.Millisecond)
					n.State, n.StateBy, n.SnoozeUntil = Snoozed, tok.Name, &u
				}
				return []Event{{Type: EventUpdated, Notification: n}}, nil
			}
			if !isNotFound(err) {
				return nil, err
			}
		}
		t := now()
		n := &Notification{ID: s.newID(t), Topic: in.Topic, ExternalID: in.ExternalID, Title: in.Title,
			Message: in.Message, Priority: in.Priority, Tags: in.Tags, Click: in.Click, Actions: in.Actions,
			State: Active, StateBy: tok.Name, CreatedAt: t}
		if in.SnoozeUntil != nil && in.SnoozeUntil.After(t) {
			u := in.SnoozeUntil.UTC().Truncate(time.Millisecond)
			n.State, n.SnoozeUntil = Snoozed, &u
		}
		return []Event{{Type: EventCreated, Notification: n}}, nil
	})
	if err != nil {
		return nil, err
	}
	return evs[0].Notification, nil
}

// Change actions and the state each one sets (spec section 5).
var actionState = map[string]string{"ack": Acknowledged, "snooze": Snoozed, "dismiss": Dismissed,
	"resolve": Resolved, "reopen": Active}

// Act applies a state change to the notification with the given id.
// until is required for snooze.
func (s *Server) Act(ctx context.Context, tok *Token, id, action string, until *time.Time) (*Notification, error) {
	return s.act(ctx, tok, action, until, false, `id = ?`, id)
}

// ActByKey applies ack or resolve to the open notification with topic and external_id.
func (s *Server) ActByKey(ctx context.Context, tok *Token, topic, externalID, action string) (*Notification, error) {
	return s.act(ctx, tok, action, nil, true, `topic = ? AND external_id = ? AND state NOT IN ('dismissed', 'resolved')`,
		topic, externalID)
}

func (s *Server) act(ctx context.Context, tok *Token, action string, until *time.Time, byKey bool, where string, args ...any) (*Notification, error) {
	if tok == nil {
		return nil, errf(401, "unauthorized", "missing or invalid token")
	}
	target, ok := actionState[action]
	if !ok {
		return nil, errf(400, "bad_request", "unknown action %q", action)
	}
	if action == "snooze" && (until == nil || !until.After(time.Now())) {
		return nil, errf(400, "bad_request", "snooze needs a time in the future")
	}
	var out *Notification
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		n, err := getNotification(ctx, tx, where, args...)
		if err != nil {
			return nil, err
		}
		if err := tok.need(scopeFor(action, byKey), n.Topic); err != nil {
			return nil, err
		}
		out = n
		ev, err := transition(n, target, tok.Name, until)
		if err != nil || ev == nil {
			return nil, err
		}
		return []Event{*ev}, nil
	})
	return out, err
}

// checkActions: each action is a link or a reply, never both; reply values
// are short, unique, and at most 3 (what an Android tray entry shows).
func checkActions(as []Action) error {
	seen := map[string]bool{}
	for _, a := range as {
		if (a.URL == "") == (a.Reply == "") {
			return errf(400, "bad_request", "action %q needs a url or a reply, not both", a.Label)
		}
		if a.Reply == "" {
			continue
		}
		if !topicRe.MatchString(a.Reply) { // same rule as a topic name
			return errf(400, "bad_request", "invalid reply %q: use letters, digits, _ and -, at most 64", a.Reply)
		}
		if seen[a.Reply] {
			return errf(400, "bad_request", "reply %q used twice", a.Reply)
		}
		seen[a.Reply] = true
	}
	if len(seen) > 3 {
		return errf(400, "bad_request", "at most 3 reply actions")
	}
	return nil
}

// Reply records a tap on one of the notification's reply actions and
// acknowledges it. The first answer wins: a second one gets 409. A non-zero
// seq is the version the device showed; if the row changed since (say the
// sender put a new question under the same key), the tap gets 409.
func (s *Server) Reply(ctx context.Context, tok *Token, id, reply string, seq int64) (*Notification, error) {
	if tok == nil {
		return nil, errf(401, "unauthorized", "missing or invalid token")
	}
	evs, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		n, err := getNotification(ctx, tx, `id = ?`, id)
		if err != nil {
			return nil, err
		}
		if err := tok.need(ScopeRead, n.Topic); err != nil {
			return nil, err
		}
		if seq != 0 && seq != n.Seq {
			return nil, errf(409, "conflict", "notification changed since seq %d; show the current one", seq)
		}
		if reply == "" || !slices.ContainsFunc(n.Actions, func(a Action) bool { return a.Reply == reply }) {
			return nil, errf(400, "bad_request", "%q is not one of this notification's replies", reply)
		}
		if n.Reply != "" {
			return nil, errf(409, "conflict", "already answered %q by %s", n.Reply, n.StateBy)
		}
		if n.Final() {
			return nil, errf(409, "conflict", "notification is %s", n.State)
		}
		n.Reply, n.State, n.StateBy, n.SnoozeUntil = reply, Acknowledged, tok.Name, nil
		return []Event{{Type: EventState, Notification: n}}, nil
	})
	if err != nil {
		return nil, err
	}
	return evs[0].Notification, nil
}

// scopeFor: the sender's actions need publish; the user's own need read.
func scopeFor(action string, byKey bool) string {
	if action == "resolve" || byKey {
		return ScopePublish
	}
	return ScopeRead
}

// transition is the one place state rules live. It mutates n and returns
// the event to log, or nil when nothing changes (same state again).
func transition(n *Notification, target, by string, until *time.Time) (*Event, error) {
	switch {
	case target == n.State && target != Snoozed:
		return nil, nil
	case n.Final() && (target == Dismissed || target == Resolved):
		return nil, nil // already gone from every inbox
	case n.Final() && target != Active:
		return nil, errf(409, "conflict", "notification is %s; reopen it first", n.State)
	}
	n.State, n.StateBy, n.SnoozeUntil = target, by, nil
	if target != Dismissed && target != Resolved {
		n.Reply = "" // an answer stands only with the tap's own ack; dismiss and resolve keep it as history
	}
	if target == Snoozed {
		u := until.UTC().Truncate(time.Millisecond)
		n.SnoozeUntil = &u
	}
	return &Event{Type: EventState, Notification: n}, nil
}

// Clear dismisses every open notification in topic, or the given ids.
func (s *Server) Clear(ctx context.Context, tok *Token, topic string, ids []string) (int, error) {
	if (topic == "") == (len(ids) == 0) {
		return 0, errf(400, "bad_request", "give topic or ids, not both")
	}
	evs, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		q := `SELECT ` + notifCols + ` FROM notifications WHERE state NOT IN ('dismissed', 'resolved') AND `
		var args []any
		if topic != "" {
			q += `topic = ?`
			args = append(args, topic)
		} else {
			q += `id IN (` + placeholders(len(ids)) + `)`
			for _, id := range ids {
				args = append(args, id)
			}
		}
		ns, err := queryNotifications(tx, q, args...)
		if err != nil {
			return nil, err
		}
		var evs []Event
		for _, n := range ns {
			if tok.need(ScopeRead, n.Topic) != nil {
				continue
			}
			ev, _ := transition(n, Dismissed, tok.Name, nil)
			evs = append(evs, *ev)
		}
		return evs, nil
	})
	return len(evs), err
}

// wakeSnoozed moves snoozed notifications whose time has come back to active.
func (s *Server) wakeSnoozed(ctx context.Context) error {
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		ns, err := queryNotifications(tx, `SELECT `+notifCols+` FROM notifications
			WHERE state = 'snoozed' AND snooze_until <= ?`, ms(time.Now()))
		var evs []Event
		for _, n := range ns {
			ev, _ := transition(n, Active, "server", nil)
			evs = append(evs, *ev)
		}
		return evs, err
	})
	return err
}

// prune drops events and final notifications older than the retention.
func (s *Server) prune(ctx context.Context) error {
	cutoff := ms(time.Now().Add(-s.opt.Retention))
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var upTo sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM events WHERE created_at < ?`, cutoff).Scan(&upTo); err != nil {
		return err
	}
	if upTo.Valid {
		if _, err := tx.Exec(`DELETE FROM events WHERE seq <= ?`, upTo.Int64); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('pruned_through', ?)
			ON CONFLICT(key) DO UPDATE SET value = MAX(value, excluded.value)`, upTo.Int64); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM notifications WHERE state IN ('dismissed', 'resolved') AND updated_at < ?`, cutoff); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if upTo.Valid && upTo.Int64 > s.pruned.Load() {
		s.pruned.Store(upTo.Int64)
	}
	return nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func queryNotifications(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]*Notification, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListQuery filters List. State is open (default), all, or one state.
type ListQuery struct {
	Topics []string
	State  string
	Q      string
	Before string
	Limit  int
}

// List returns notifications newest first, limited to what tok may read.
func (s *Server) List(ctx context.Context, tok *Token, lq ListQuery) ([]*Notification, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return nil, err
	}
	var where []string
	var args []any
	in := func(col string, vals []string) {
		where = append(where, col+` IN (`+placeholders(len(vals))+`)`)
		for _, v := range vals {
			args = append(args, v)
		}
	}
	topics := lq.Topics
	if len(tok.Topics) > 0 {
		if len(topics) == 0 {
			topics = tok.Topics
		}
		for _, t := range topics {
			if !tok.Allows(t) {
				return nil, errf(403, "forbidden", "token may not read topic %q", t)
			}
		}
	}
	if len(topics) > 0 {
		in("topic", topics)
	}
	switch lq.State {
	case "", "open":
		in("state", []string{Active, Acknowledged, Snoozed})
	case "all":
	default:
		if _, ok := map[string]bool{Active: true, Acknowledged: true, Snoozed: true, Dismissed: true, Resolved: true}[lq.State]; !ok {
			return nil, errf(400, "bad_request", "unknown state %q", lq.State)
		}
		in("state", []string{lq.State})
	}
	if lq.Q != "" {
		where = append(where, `(title LIKE ? OR message LIKE ?)`)
		like := "%" + lq.Q + "%"
		args = append(args, like, like)
	}
	if lq.Before != "" {
		where = append(where, `id < ?`)
		args = append(args, lq.Before)
	}
	if lq.Limit <= 0 {
		lq.Limit = 100
	}
	lq.Limit = min(lq.Limit, 500)
	q := `SELECT ` + notifCols + ` FROM notifications`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	return queryNotifications(s.db, q+fmt.Sprintf(` ORDER BY id DESC LIMIT %d`, lq.Limit), args...)
}

// Get returns one notification.
func (s *Server) Get(ctx context.Context, tok *Token, id string) (*Notification, error) {
	n, err := getNotification(ctx, s.db, `id = ?`, id)
	if err != nil {
		return nil, err
	}
	if err := tok.need(ScopeRead, n.Topic); err != nil {
		return nil, err
	}
	return n, nil
}

// TopicCount is a topic with how many of its notifications are open.
type TopicCount struct {
	Topic string `json:"topic"`
	Open  int    `json:"open"`
}

// Topics lists topics tok may read, with open counts.
func (s *Server) Topics(ctx context.Context, tok *Token) ([]TopicCount, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT topic, SUM(state IN ('active', 'acknowledged', 'snoozed'))
		FROM notifications GROUP BY topic ORDER BY topic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopicCount{}
	for rows.Next() {
		var t TopicCount
		if err := rows.Scan(&t.Topic, &t.Open); err != nil {
			return nil, err
		}
		if tok.Allows(t.Topic) {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// eventsAfter returns up to limit logged events with seq > since, oldest
// first. seq has no gaps, so a missing since+1 means a prune ran between the
// caller's check and this query: that is a 410 reset, never a silent skip.
func (s *Server) eventsAfter(ctx context.Context, since int64, limit int) ([]Event, error) {
	head := s.seq.Load() // stored only after commit, so every seq <= head was written
	rows, err := s.db.QueryContext(ctx, `SELECT seq, topic, data FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var topic, data string
		if err := rows.Scan(&e.Seq, &topic, &data); err != nil {
			return nil, err
		}
		e.Raw, e.Notification = []byte(data), &Notification{Topic: topic}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if (len(out) > 0 && out[0].Seq != since+1) || (len(out) == 0 && since < head) {
		return nil, errReset
	}
	return out, nil
}

func isNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == 404
}
