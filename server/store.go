package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	EventRepeat  = "repeat" // a re-alert of an unanswered notification (#80)
	EventMark    = "mark"   // someone marked it seen or actioned (marks.go): no state change, no alert
)

const schema = `
CREATE TABLE IF NOT EXISTS notifications (
  id TEXT PRIMARY KEY,
  topic TEXT NOT NULL,
  external_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL DEFAULT '',
  format TEXT NOT NULL DEFAULT 'markdown',
  priority INTEGER NOT NULL DEFAULT 3,
  tags TEXT NOT NULL DEFAULT '[]',
  click TEXT NOT NULL DEFAULT '',
  icon TEXT NOT NULL DEFAULT '',
  actions TEXT NOT NULL DEFAULT '[]',
  attachment TEXT NOT NULL DEFAULT '',
  file TEXT NOT NULL DEFAULT '',
  reply TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  state_by TEXT NOT NULL DEFAULT '',
  state_by_id TEXT NOT NULL DEFAULT '',
  snooze_until INTEGER,
  expires_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  seq INTEGER NOT NULL DEFAULT 0,
  silenced INTEGER NOT NULL DEFAULT 0,
  repeat TEXT NOT NULL DEFAULT '',
  marks TEXT NOT NULL DEFAULT ''
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
  grants TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER
);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
-- Display settings every device follows, as one JSON document (spec, Message formatting).
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS topics (topic TEXT PRIMARY KEY, icon TEXT NOT NULL DEFAULT '',
	checkin_ms INTEGER, checkin_at INTEGER, checkin_late INTEGER NOT NULL DEFAULT 0, device_actions INTEGER NOT NULL DEFAULT 0);
-- Format check findings a user dismissed, by topic, rule and sending token ("" for every one); rule "" turns the check off for the topic (#320).
CREATE TABLE IF NOT EXISTS format_ignore (topic TEXT NOT NULL, rule TEXT NOT NULL, sender TEXT NOT NULL, PRIMARY KEY (topic, rule, sender));
-- Webhook templates a topic owner saved (webhooks.go); spec is the Webhook as JSON.
CREATE TABLE IF NOT EXISTS webhook_templates (topic TEXT NOT NULL, name TEXT NOT NULL, spec TEXT NOT NULL, PRIMARY KEY (topic, name));
-- People (issue #143): each owns the tokens whose user_id is its id. grants
-- is JSON like a token's, '' when the user limits nothing.
CREATE TABLE IF NOT EXISTS people (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  grants TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
-- ntfy passwords (issue #117): each user signs in as its own token, on ntfy routes only.
-- Not the people above: an ntfy login is one token.
CREATE TABLE IF NOT EXISTS users (
  name TEXT PRIMARY KEY,
  hash TEXT NOT NULL,
  token_id TEXT NOT NULL REFERENCES tokens(id) ON DELETE CASCADE
);
-- Gotify apps (issue #370): a token whose secret is a Gotify-shaped key,
-- taken only on POST /message. id is the appid Gotify senders see.
CREATE TABLE IF NOT EXISTS gotify_apps (
  id INTEGER PRIMARY KEY,
  token_id TEXT NOT NULL UNIQUE REFERENCES tokens(id) ON DELETE CASCADE,
  priority INTEGER -- Gotify's default_priority; NULL is clrd's default
);
-- ntfy message ids from an import, so since=<id> carries on where a phone left off.
CREATE TABLE IF NOT EXISTS ntfy_mids (mid TEXT PRIMARY KEY, seq INTEGER NOT NULL);
-- ntfy headers, params and JSON fields received but ignored, for the admin page.
CREATE TABLE IF NOT EXISTS ntfy_ignored (name TEXT PRIMARY KEY, count INTEGER NOT NULL, last_seen INTEGER NOT NULL);
-- A web sign-in mints its own token; parent_id is the token typed in.
CREATE TABLE IF NOT EXISTS sessions (
  token_id TEXT PRIMARY KEY REFERENCES tokens(id) ON DELETE CASCADE,
  parent_id TEXT NOT NULL,
  phone INTEGER NOT NULL DEFAULT 0 -- 1 for a phone linked from a browser (issue #180)
);
-- Links, sign-ins and unlinks (issue #246). owner is the top token id of the
-- device, so each person sees only their own lines; names, never secrets.
CREATE TABLE IF NOT EXISTS device_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  owner TEXT NOT NULL,
  action TEXT NOT NULL,
  device TEXT NOT NULL,
  device_id TEXT NOT NULL,
  by_name TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS device_log_owner ON device_log(owner, id);
-- Outgoing webhooks (issue #112): events is a JSON array of hook event names.
CREATE TABLE IF NOT EXISTS hooks_out (
  topic TEXT NOT NULL,
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  events TEXT NOT NULL,
  secret TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  failing_since INTEGER, -- first failure since the last 2xx
  PRIMARY KEY (topic, name)
);
-- One row per event per hook, written in the event's own transaction.
CREATE TABLE IF NOT EXISTS hook_deliveries (
  id INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused: it is X-Clrd-Delivery
  topic TEXT NOT NULL,
  name TEXT NOT NULL,
  seq INTEGER NOT NULL,
  event TEXT NOT NULL,
  body TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending', -- pending, ok or failed
  attempts INTEGER NOT NULL DEFAULT 0,
  next_at INTEGER NOT NULL,
  code INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS hook_deliveries_due ON hook_deliveries (status, next_at);
CREATE INDEX IF NOT EXISTS hook_deliveries_hook ON hook_deliveries (topic, name, id);
-- Silences (issue #81): topic "" matches every topic, tag "" every tag.
CREATE TABLE IF NOT EXISTS silences (
  id TEXT PRIMARY KEY,
  topic TEXT NOT NULL DEFAULT '',
  tag TEXT NOT NULL DEFAULT '',
  until_ms INTEGER NOT NULL,
  comment TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
`

// Action is a button on a notification: a link (URL), an answer (Reply)
// that POST /api/v1/notifications/{id}/reply records, or a request the
// reader's device makes (HTTP, Broadcast; deviceactions.go).
type Action struct {
	Label     string           `json:"label"`
	URL       string           `json:"url"`
	Reply     string           `json:"reply,omitempty"`
	HTTP      *HTTPAction      `json:"http,omitempty"`
	Broadcast *BroadcastAction `json:"broadcast,omitempty"`
	Clear     bool             `json:"clear,omitempty"`
}

// Notification is the server-owned record every client mirrors.
type Notification struct {
	ID          string      `json:"id"`
	Topic       string      `json:"topic"`
	ExternalID  string      `json:"external_id"`
	Title       string      `json:"title"`
	Message     string      `json:"message"`
	Format      string      `json:"format"` // FormatMarkdown or FormatText
	Priority    int         `json:"priority"`
	Tags        []string    `json:"tags"`
	Click       string      `json:"click"`
	Icon        string      `json:"icon"` // http(s) image URL; the topic's icon when the sender gave none
	Actions     []Action    `json:"actions"`
	Attachment  *Attachment `json:"attachment"`
	file        string      // uploaded file id, "" for none or external
	sender      string      // top token of the last sender's tree: it may not answer it (#187)
	confirm     string      // a confirm prompt: its confirmLink as JSON, "" for any other (#187)
	Reply       string      `json:"reply"` // the reply action tapped, "" until one is
	State       string      `json:"state"`
	StateBy     string      `json:"state_by"`
	StateByID   string      `json:"state_by_id"` // token id: names can repeat
	SnoozeUntil *time.Time  `json:"snooze_until"`
	ExpiresAt   *time.Time  `json:"expires_at"` // the server resolves it then
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Seq         int64       `json:"seq"`
	Silenced    bool        `json:"silenced"`       // a silence matched when this was written: show, do not alert
	Live        bool        `json:"live,omitempty"` // sent to open streams only, never stored (#400): ID is a live id, Seq the log seq at send
	Repeat      *Repeat     `json:"repeat"`         // re-alert until answered, or null
	Marks       Marks       `json:"marks"`          // who saw or acted on it (marks.go); never changes the state
}

// Attachment is a file on a notification: uploaded (URL is /api/v1/files/<id>)
// or external (an http(s) URL the server never fetches; Type "" and Size 0).
type Attachment struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// Final reports whether the notification has left every inbox.
func (n *Notification) Final() bool { return n.State == Dismissed || n.State == Resolved }

// Event is one entry of the append-only log. Raw is its JSON as stored.
type Event struct {
	Seq          int64         `json:"seq"`
	Type         string        `json:"type"`
	Notification *Notification `json:"notification"`
	// Via says how a state change was made (docs/SPEC.md section 5 lists the values), so a
	// report like "the phone cleared it and I did not" can be answered from the log (#265).
	Via     string `json:"via,omitempty"`
	Raw     []byte `json:"-"`
	replied bool   // a reply button was tapped: the "action" hook event
}

// viaRe bounds the client's label: it is stored and sent to every device.
var viaRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// cleanVia keeps a well-formed label from the client, else def. A bad label never blocks the action.
func cleanVia(v, def string) string {
	if viaRe.MatchString(v) {
		return v
	}
	return def
}

var topicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Reserved topic names: the server and mounted pages own these paths.
var reserved = map[string]bool{"api": true, "app": true, "assets": true, "docs": true, "healthz": true,
	"login": true, "logout": true, "mcp": true, "setup": true, "static": true, "v1": true}

func validTopic(t string) error {
	if t == "" {
		return errf(400, "bad_request", "topic is required: name one, like \"alerts\" (letters, digits, _ and -, at most 64)")
	}
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
	// Databases made before these columns existed.
	for _, col := range []string{"reply", "state_by_id", "icon", "attachment", "file", "repeat", "sender", "confirm", "marks"} {
		if _, err := db.Exec(`ALTER TABLE notifications ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("add %s column: %w", col, err)
		}
	}
	// Rows from before formats were rendered as Markdown, the default.
	if _, err := db.Exec(`ALTER TABLE notifications ADD COLUMN format TEXT NOT NULL DEFAULT 'markdown'`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("add format column: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE notifications ADD COLUMN expires_at INTEGER`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("add expires_at column: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS notifications_expires ON notifications(expires_at) WHERE expires_at IS NOT NULL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("expires index: %w", err)
	}
	for _, col := range []string{"checkin_ms INTEGER", "checkin_at INTEGER", "checkin_late INTEGER NOT NULL DEFAULT 0", "device_actions INTEGER NOT NULL DEFAULT 0"} {
		if _, err := db.Exec(`ALTER TABLE topics ADD COLUMN ` + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("add topics %s: %w", col, err)
		}
	}
	if _, err := db.Exec(`ALTER TABLE tokens ADD COLUMN grants TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("add grants column: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE notifications ADD COLUMN silenced INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("add silenced column: %w", err)
	}
	// qseq: the seq of the row's last event that changed the question, so a
	// mark or re-alert after a prune still lets an old tap through (#452).
	// Filled once from the log; a row whose such event was pruned takes its
	// seq, which refuses an older tap as before.
	if _, err := db.Exec(`ALTER TABLE notifications ADD COLUMN qseq INTEGER NOT NULL DEFAULT 0`); err == nil {
		if _, err := db.Exec(`UPDATE notifications SET qseq = m.s FROM (SELECT json_extract(data, '$.notification.id') AS id, MAX(seq) AS s
			FROM events WHERE type NOT IN (?, ?) GROUP BY 1) AS m WHERE m.id = notifications.id`, EventMark, EventRepeat); err != nil {
			db.Close()
			return nil, fmt.Errorf("fill qseq: %w", err)
		}
		if _, err := db.Exec(`UPDATE notifications SET qseq = seq WHERE qseq = 0`); err != nil {
			db.Close()
			return nil, fmt.Errorf("fill qseq: %w", err)
		}
	} else if !strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("add qseq column: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN phone INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("add sessions phone column: %w", err)
	}
	// The person a typed-in token belongs to (#143); NULL for none.
	if _, err := db.Exec(`ALTER TABLE tokens ADD COLUMN user_id TEXT REFERENCES people(id)`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("add tokens user_id column: %w", err)
	}
	// Agents (#454): kind is person or agent; owner is the top token whose
	// invite made an agent, so it shows in that person's device list.
	for _, c := range []string{`kind TEXT NOT NULL DEFAULT 'person'`, `owner TEXT NOT NULL DEFAULT ''`} {
		if _, err := db.Exec(`ALTER TABLE people ADD COLUMN ` + c); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("add people column: %w", err)
		}
	}
	// Rows the CLRD_CONFIG file made (#155): only these does a start remove.
	for _, t := range []string{"tokens", "people"} {
		if _, err := db.Exec(`ALTER TABLE ` + t + ` ADD COLUMN config INTEGER NOT NULL DEFAULT 0`); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("add %s config column: %w", t, err)
		}
	}
	// Who may answer approval requests (#187). Only on the run that adds the
	// column, browsers and phones the user linked (a sessions row) start
	// allowed so they keep working; plain tokens start denied. Never again,
	// or a restart would undo a device the user set to denied. One
	// transaction, so a crash between the two never leaves them denied.
	mig, err := db.Begin()
	if err == nil {
		if _, err = mig.Exec(`ALTER TABLE tokens ADD COLUMN approve TEXT NOT NULL DEFAULT 'denied'`); err == nil {
			if _, err = mig.Exec(`UPDATE tokens SET approve = 'allowed' WHERE id IN (SELECT token_id FROM sessions)`); err == nil {
				err = mig.Commit()
			}
		}
		mig.Rollback()
	}
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("add approve column: %w", err)
	}
	// After the loop: an old database only has the column once it ran.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS notifications_file ON notifications(file) WHERE file != ''`); err != nil {
		db.Close()
		return nil, fmt.Errorf("file index: %w", err)
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

const notifCols = `id, topic, external_id, title, message, format, priority, tags, click, icon, actions, attachment, file, reply,
  state, state_by, state_by_id, snooze_until, created_at, updated_at, seq, expires_at, silenced, repeat, sender, confirm, marks`

type scanner interface{ Scan(...any) error }

func scanNotification(r scanner) (*Notification, error) {
	var n Notification
	var tags, actions, att, rep, marks string
	var snooze, expires sql.NullInt64
	var created, updated int64
	err := r.Scan(&n.ID, &n.Topic, &n.ExternalID, &n.Title, &n.Message, &n.Format, &n.Priority, &tags, &n.Click,
		&n.Icon, &actions, &att, &n.file, &n.Reply, &n.State, &n.StateBy, &n.StateByID, &snooze, &created, &updated, &n.Seq, &expires,
		&n.Silenced, &rep, &n.sender, &n.confirm, &marks)
	if err != nil {
		return nil, err
	}
	if marks != "" {
		if err := json.Unmarshal([]byte(marks), &n.Marks); err != nil {
			return nil, err
		}
	}
	if att != "" {
		if err := json.Unmarshal([]byte(att), &n.Attachment); err != nil {
			return nil, err
		}
	}
	if rep != "" {
		if err := json.Unmarshal([]byte(rep), &n.Repeat); err != nil {
			return nil, err
		}
	}
	n.CreatedAt, n.UpdatedAt = fromMS(created), fromMS(updated)
	if snooze.Valid {
		t := fromMS(snooze.Int64)
		n.SnoozeUntil = &t
	}
	if expires.Valid {
		t := fromMS(expires.Int64)
		n.ExpiresAt = &t
	}
	if err := json.Unmarshal([]byte(tags), &n.Tags); err != nil {
		return nil, err
	}
	return &n, json.Unmarshal([]byte(actions), &n.Actions)
}

func saveNotification(tx *sql.Tx, n *Notification, typ string) error {
	tags, _ := json.Marshal(n.Tags)
	actions, _ := json.Marshal(n.Actions)
	att := ""
	if n.Attachment != nil {
		b, _ := json.Marshal(n.Attachment)
		att = string(b)
	}
	rep := ""
	if n.Repeat != nil {
		b, _ := json.Marshal(n.Repeat)
		rep = string(b)
	}
	marks := ""
	if len(n.Marks) > 0 {
		b, _ := json.Marshal(n.Marks)
		marks = string(b)
	}
	_, err := tx.Exec(`INSERT INTO notifications (`+notifCols+`) VALUES (`+placeholders(27)+`)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title, message=excluded.message, format=excluded.format,
		priority=excluded.priority, tags=excluded.tags, click=excluded.click, icon=excluded.icon, actions=excluded.actions,
		attachment=excluded.attachment, file=excluded.file,
		reply=excluded.reply, state=excluded.state, state_by=excluded.state_by, state_by_id=excluded.state_by_id, snooze_until=excluded.snooze_until,
		updated_at=excluded.updated_at, seq=excluded.seq, expires_at=excluded.expires_at, silenced=excluded.silenced, repeat=excluded.repeat, sender=excluded.sender, confirm=excluded.confirm, marks=excluded.marks`,
		n.ID, n.Topic, n.ExternalID, n.Title, n.Message, n.Format, n.Priority, string(tags), n.Click, n.Icon, string(actions),
		att, n.file, n.Reply, n.State, n.StateBy, n.StateByID, nullMS(n.SnoozeUntil), ms(n.CreatedAt), ms(n.UpdatedAt), n.Seq, nullMS(n.ExpiresAt),
		n.Silenced, rep, n.sender, n.confirm, marks)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errf(409, "conflict", "an open notification with external_id %q already exists in %q", n.ExternalID, n.Topic)
	}
	if err == nil && typ != EventMark && typ != EventRepeat { // the question changed (#452)
		_, err = tx.Exec(`UPDATE notifications SET qseq = ? WHERE id = ?`, n.Seq, n.ID)
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
	var sils []Silence
	if len(evs) > 0 {
		if sils, err = activeSilences(ctx, tx, t); err != nil {
			return nil, err
		}
	}
	for i := range evs {
		e := &evs[i]
		e.Notification.Silenced = silenced(sils, e.Notification)
		res, err := tx.Exec(`INSERT INTO events (type, topic, data, created_at) VALUES (?, ?, '', ?)`,
			e.Type, e.Notification.Topic, ms(t))
		if err != nil {
			return nil, err
		}
		e.Seq, _ = res.LastInsertId()
		e.Notification.Seq, e.Notification.UpdatedAt = e.Seq, t
		if err := saveNotification(tx, e.Notification, e.Type); err != nil {
			return nil, err
		}
		if e.Raw, err = json.Marshal(e); err != nil {
			return nil, errf(400, "bad_request", "cannot encode notification: %v", err)
		}
		if _, err := tx.Exec(`UPDATE events SET data = ? WHERE seq = ?`, string(e.Raw), e.Seq); err != nil {
			return nil, err
		}
		if err := queueHooks(tx, e, t); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, e := range evs {
		if e.Type == EventState { // one line per change, so the server log alone shows who cleared what and how
			n := e.Notification
			s.log.Info("state", "seq", e.Seq, "id", n.ID, "topic", n.Topic, "state", n.State, "by", n.StateBy, "by_id", n.StateByID, "via", e.Via)
		}
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
	Topic       string      `json:"topic"`
	ExternalID  string      `json:"external_id"`
	Title       string      `json:"title"`
	Message     string      `json:"message"`
	Format      string      `json:"format"`   // "markdown" (default) or "text"
	Markdown    *bool       `json:"markdown"` // ntfy's flag: false means "text"; Format wins when both are set
	Priority    int         `json:"priority"`
	Tags        []string    `json:"tags"`
	Click       string      `json:"click"`
	Icon        string      `json:"icon"` // http(s) image URL; the topic's icon when the sender gave none
	Actions     []Action    `json:"actions"`
	Attachment  *Attachment `json:"attachment"` // external only: {"url","name"}; uploads go through PublishFile
	file        string
	dropDevice  bool       // compat: drop http and broadcast actions a topic does not allow, instead of refusing
	checkin     bool       // clrd's own missed check-in alert: does not reset the timer
	SnoozeUntil *time.Time `json:"snooze_until"` // optional: deliver later (scheduled send)
	Expires     string     `json:"expires"`      // optional: a duration ("30m") or RFC3339 time; then the server resolves it
	Repeat      string     `json:"repeat"`       // optional: re-alert every this long ("5m") until someone acts
	RepeatTimes int        `json:"repeat_times"` // how many re-alerts at most; 10 when repeat is set and this is 0
	// Status "resolved" (Alertmanager, Grafana, Gatus) resolves the open
	// notification with this external_id instead of creating one; 404 if none.
	Status string `json:"status,omitempty"`
	// Reopen puts an acknowledged notification under this external_id back
	// to active in the same write, so a new question cannot inherit the old answer.
	Reopen bool `json:"reopen,omitempty"`
	// Live sends it to the streams open now and stores nothing: no inbox,
	// no replay, nothing to act on (ntfy's Cache: no, #400).
	Live bool `json:"live,omitempty"`
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
	if err := s.gateDeviceActions(ctx, s.db, &in); err != nil {
		return nil, err
	}
	if in.file == "" { // PublishFile already spent it, before the upload
		if err := s.publishLimit(tok, in.Topic); err != nil {
			return nil, err
		}
	}
	if err := checkImageURL("icon", in.Icon); err != nil {
		return nil, err
	}
	if a := in.Attachment; a != nil && in.file == "" { // external: only the URL and name are the sender's
		if a.URL == "" {
			in.Attachment = nil
		} else if err := checkImageURL("attachment.url", a.URL); err != nil {
			return nil, err
		} else {
			name := a.Name
			if name == "" {
				u, _ := url.Parse(a.URL)
				name = u.Path
			}
			in.Attachment = &Attachment{Name: cleanName(name), URL: a.URL}
		}
	}
	format, err := publishFormat(in.Format, in.Markdown)
	if err != nil {
		return nil, err
	}
	if in.Live {
		return s.publishLive(ctx, tok, in, format)
	}
	if in.Status == "resolved" {
		if in.ExternalID == "" {
			return nil, errf(400, "bad_request", "status resolved needs an external_id")
		}
		return s.ActByKey(ctx, tok, in.Topic, in.ExternalID, "resolve", "publish")
	}
	if err := in.fill(); err != nil {
		return nil, err
	}
	expires, err := parseExpires(in.Expires)
	if err != nil {
		return nil, err
	}
	from := now()
	if in.SnoozeUntil != nil && in.SnoozeUntil.After(from) { // scheduled: count from delivery
		from = in.SnoozeUntil.UTC().Truncate(time.Millisecond)
	}
	repeat, err := parseRepeat(in.Repeat, in.RepeatTimes, from)
	if err != nil {
		return nil, err
	}
	var replaced string // file id an upsert drops, removed after commit
	var extra []Event   // a check-in resolving the topic's missed check-in alert
	evs, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		if err := s.gateDeviceActions(ctx, tx, &in); err != nil { // again, as it is now (#447)
			return nil, err
		}
		// The sender's token tree, kept now so signing the sending browser
		// out later cannot free its parent to answer (#187).
		sender, err := topIn(ctx, tx, tok.ID)
		if err != nil {
			return nil, err
		}
		if in.file != "" {
			var used int64
			// The row this upsert replaces does not count: its file goes.
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(json_extract(attachment, '$.size')), 0)
				FROM notifications WHERE file != '' AND NOT (external_id != '' AND topic = ? AND external_id = ?
				AND state NOT IN ('dismissed', 'resolved'))`, in.Topic, in.ExternalID).Scan(&used); err != nil {
				return nil, err
			}
			if used+in.Attachment.Size > s.opt.AttachmentTotal {
				return nil, errf(507, "storage_full", "attachment storage is full")
			}
		}
		if in.Icon == "" { // the topic's icon, copied so a later change leaves old rows alone
			if err := tx.QueryRowContext(ctx, `SELECT icon FROM topics WHERE topic = ?`, in.Topic).Scan(&in.Icon); err != nil &&
				!errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		if ev, err := s.checkIn(ctx, tx, in); err != nil {
			return nil, err
		} else if ev != nil {
			extra = append(extra, *ev)
		}
		if in.ExternalID != "" {
			n, err := getNotification(ctx, tx, `topic = ? AND external_id = ? AND state NOT IN ('dismissed', 'resolved')`,
				in.Topic, in.ExternalID)
			if err == nil {
				n.Title, n.Message, n.Format, n.Priority, n.Tags, n.Click, n.Icon, n.Actions, n.Reply =
					in.Title, in.Message, format, in.Priority, in.Tags, in.Click, in.Icon, in.Actions, "" // a new question starts unanswered
				replaced, n.Attachment, n.file, n.ExpiresAt, n.Repeat, n.sender = n.file, in.Attachment, in.file, expires, repeat, sender
				if in.Reopen && n.State == Acknowledged {
					n.State = Active
					n.setBy(tok)
				}
				if t := now(); in.SnoozeUntil != nil && in.SnoozeUntil.After(t) { // delayed: hide until then, like a new row
					u := in.SnoozeUntil.UTC().Truncate(time.Millisecond)
					n.State, n.SnoozeUntil = Snoozed, &u
					n.setBy(tok)
				}
				if n.State == Acknowledged || (n.State == Snoozed && !n.SnoozeUntil.Equal(from)) {
					n.stopRepeat() // someone already answered this row
				}
				return append([]Event{{Type: EventUpdated, Notification: n}}, extra...), nil
			}
			if !isNotFound(err) {
				return nil, err
			}
		}
		t := now()
		n := &Notification{ID: s.newID(t), Topic: in.Topic, ExternalID: in.ExternalID, Title: in.Title,
			Message: in.Message, Format: format, Priority: in.Priority, Tags: in.Tags, Click: in.Click, Icon: in.Icon, Actions: in.Actions,
			Attachment: in.Attachment, file: in.file, State: Active, StateBy: tok.Name, StateByID: tok.ID, CreatedAt: t, ExpiresAt: expires,
			Repeat: repeat, sender: sender}
		if in.SnoozeUntil != nil && in.SnoozeUntil.After(t) {
			u := in.SnoozeUntil.UTC().Truncate(time.Millisecond)
			n.State, n.SnoozeUntil = Snoozed, &u
		}
		return append([]Event{{Type: EventCreated, Notification: n}}, extra...), nil
	})
	if err != nil {
		return nil, err
	}
	if replaced != "" && replaced != in.file {
		os.Remove(filepath.Join(s.opt.Attachments, replaced))
	}
	return senderView(tok, evs[0].Notification, true), nil
}

// senderView is what a token that may not read the topic gets back: a
// publish can land on someone else's notification (same external_id), and
// resolve acts on one, so only the id and what the sender set come back.
func senderView(tok *Token, n *Notification, own bool) *Notification {
	if n == nil || tok.may(ScopeRead, n.Topic) {
		return n
	}
	v := &Notification{ID: n.ID, Topic: n.Topic, ExternalID: n.ExternalID, State: n.State, CreatedAt: n.UpdatedAt, UpdatedAt: n.UpdatedAt, Seq: n.Seq, Live: n.Live}
	if own { // a publish: the content is the sender's own; the state is not
		v.Title, v.Message, v.Priority, v.Tags, v.Click, v.Icon, v.Actions, v.Attachment, v.ExpiresAt =
			n.Title, n.Message, n.Priority, n.Tags, n.Click, n.Icon, n.Actions, n.Attachment, n.ExpiresAt
		v.State = Active
	}
	return v
}

// fileID is an uploaded file's name on disk and in its URL.
var fileID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// PublishFile stores body as an uploaded attachment and publishes in with it.
// Nothing reaches disk unless the token may publish to the topic, and the
// file is removed again if the publish fails.
func (s *Server) PublishFile(ctx context.Context, tok *Token, in PublishInput, name string, body io.Reader) (*Notification, error) {
	if err := validTopic(in.Topic); err != nil {
		return nil, err
	}
	if err := tok.need(ScopePublish, in.Topic); err != nil {
		return nil, err
	}
	if in.Status == "resolved" {
		return nil, errf(400, "bad_request", "status resolved cannot carry a file")
	}
	if in.Live {
		return nil, errLiveFile
	}
	if err := s.gateDeviceActions(ctx, s.db, &in); err != nil { // before the upload, not after
		return nil, err
	}
	if err := s.publishLimit(tok, in.Topic); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(s.opt.Attachments, ".up-*") // dot: the sweep never mistakes it for a finished file
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name()) // no-op once renamed
	size, err := io.Copy(f, io.LimitReader(body, s.opt.AttachmentMax+1))
	head := make([]byte, 512)
	k, _ := f.ReadAt(head, 0)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe) || size > s.opt.AttachmentMax:
		return nil, errf(413, "too_large", "file over %d bytes", s.opt.AttachmentMax)
	case errors.Is(err, os.ErrDeadlineExceeded):
		return nil, errf(408, "timeout", "upload too slow")
	case err != nil:
		return nil, errf(400, "bad_request", "cannot read file: %v", err)
	}
	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)
	path := filepath.Join(s.opt.Attachments, id)
	if err := os.Rename(f.Name(), path); err != nil {
		return nil, err
	}
	in.Attachment = &Attachment{Name: cleanName(name), URL: "/api/v1/files/" + id, Type: http.DetectContentType(head[:k]), Size: size}
	in.file = id
	n, err := s.Publish(ctx, tok, in)
	if err != nil {
		os.Remove(path)
	}
	return n, err
}

// cleanName keeps a sender's file name safe to show and to put in a header.
func cleanName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	for len(name) > 255 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	if name == "" || name == "." || name == ".." || name == "/" {
		return "attachment"
	}
	return name
}

// Change actions and the state each one sets (spec section 5).
var actionState = map[string]string{"ack": Acknowledged, "snooze": Snoozed, "dismiss": Dismissed,
	"resolve": Resolved, "reopen": Active}

// Act applies a state change to the notification with the given id.
// until is required for snooze.
// via is how the person made the change (see [Event.Via]).
func (s *Server) Act(ctx context.Context, tok *Token, id, action, via string, until *time.Time) (*Notification, error) {
	return s.act(ctx, tok, action, via, until, false, `id = ?`, id)
}

// Edit replaces the message of an open notification in place: same id, same
// state, every device redraws it. An agent's progress line, not a new alert.
func (s *Server) Edit(ctx context.Context, tok *Token, id, message string) (*Notification, error) {
	if tok == nil {
		return nil, errf(401, "unauthorized", noToken)
	}
	if message == "" {
		return nil, errf(400, "bad_request", "message is required")
	}
	var out *Notification
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		n, err := getNotification(ctx, tx, `id = ?`, id)
		if err != nil {
			return nil, err
		}
		if err := tok.need(ScopePublish, n.Topic); err != nil {
			return nil, err
		}
		if n.Final() {
			return nil, errf(409, "conflict", "notification is %s", n.State)
		}
		// A tap answers the text shown with the buttons (an approve prompt's
		// Allow), so that text must not change under them.
		if slices.ContainsFunc(n.Actions, func(a Action) bool { return a.Reply != "" }) {
			return nil, errf(409, "conflict", "notification has answer buttons")
		}
		if err := s.publishLimit(tok, n.Topic); err != nil {
			return nil, err
		}
		n.Message = message
		out = n
		return []Event{{Type: EventUpdated, Notification: n}}, nil
	})
	if err != nil {
		return nil, err
	}
	return senderView(tok, out, false), nil
}

// ActByKey applies ack or resolve to the open notification with topic and external_id.
func (s *Server) ActByKey(ctx context.Context, tok *Token, topic, externalID, action, via string) (*Notification, error) {
	return s.act(ctx, tok, action, via, nil, true, `topic = ? AND external_id = ? AND state NOT IN ('dismissed', 'resolved')`,
		topic, externalID)
}

func (s *Server) act(ctx context.Context, tok *Token, action, via string, until *time.Time, byKey bool, where string, args ...any) (*Notification, error) {
	if tok == nil {
		return nil, errf(401, "unauthorized", noToken)
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
		ev, err := transition(n, target, tok, via, until)
		if err != nil || ev == nil {
			return nil, err
		}
		return []Event{*ev}, nil
	})
	if err != nil {
		return nil, err
	}
	return senderView(tok, out, false), nil
}

// checkActions: each action is one of a link, a reply, an http request or a
// broadcast; reply values are short, unique, and at most 3 (what an Android
// tray entry shows).
func checkActions(as []Action) error {
	seen, dev := map[string]bool{}, 0
	for _, a := range as {
		if dev += btoi(isDeviceAction(a)); dev > 3 {
			return errf(400, "bad_request", "at most 3 http or broadcast actions")
		}
		if n := btoi(a.URL != "") + btoi(a.Reply != "") + btoi(a.HTTP != nil) + btoi(a.Broadcast != nil); n != 1 {
			return errf(400, "bad_request", "action %q needs exactly one of url, reply, http or broadcast", a.Label)
		}
		if err := checkDeviceAction(a); err != nil {
			return err
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
// sender put a new question under the same key), the tap gets 409. A tap
// from an ask device leaves it unanswered and returns the confirm prompt
// instead, a notification with another id (#187).
func (s *Server) Reply(ctx context.Context, tok *Token, id, reply, via string, seq int64) (*Notification, error) {
	if tok == nil {
		return nil, errf(401, "unauthorized", noToken)
	}
	var out *Notification
	var raised bool // a setting request was confirmed
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		n, err := getNotification(ctx, tx, `id = ?`, id)
		if err != nil {
			return nil, err
		}
		if err := tok.need(ScopeRead, n.Topic); err != nil {
			return nil, err
		}
		if err := s.changedSince(ctx, tx, n, seq); err != nil {
			return nil, err
		}
		if reply == "" || !slices.ContainsFunc(n.Actions, func(a Action) bool { return a.Reply == reply }) {
			return nil, errf(400, "bad_request", "%q is not one of this notification's replies", reply)
		}
		v, err := mayApprove(ctx, tx, tok, n)
		if err != nil {
			return nil, err
		}
		if n.Reply != "" {
			return nil, errf(409, "conflict", "already answered %q by %s", n.Reply, n.StateBy)
		}
		if n.Final() {
			return nil, errf(409, "conflict", "notification is %s", n.State)
		}
		var evs []Event
		if v == ApproveAsk {
			out, evs, err = s.askConfirm(ctx, tx, tok, n, reply)
			return evs, err
		}
		prompt := n.confirm != ""
		if prompt {
			if evs, err = s.confirmed(ctx, tx, tok, n, reply, via, &raised); err != nil {
				return nil, err
			}
		}
		out, n.Reply, n.State, n.SnoozeUntil = n, reply, Acknowledged, nil
		n.setBy(tok)
		n.stopRepeat()
		// A tap on a confirm prompt is not an answer to the topic's hooks.
		return append([]Event{{Type: EventState, Notification: n, Via: via, replied: !prompt}}, evs...), nil
	})
	if err != nil {
		return nil, err
	}
	if raised {
		s.devicesChanged.fire()
	}
	return out, nil
}

// changedSince refuses a tap on n from a device that showed it at seq, when
// the question changed since; a re-alert or a mark changes the seq, not the
// question. The row keeps the seq of its last change to the question (qseq),
// so this holds after a prune too (#452). A seq the row never had is refused.
func (s *Server) changedSince(ctx context.Context, tx *sql.Tx, n *Notification, seq int64) error {
	if seq == 0 || seq == n.Seq {
		return nil
	}
	changed := seq > n.Seq
	if seq < n.Seq {
		var q int64
		if err := tx.QueryRowContext(ctx, `SELECT qseq FROM notifications WHERE id = ?`, n.ID).Scan(&q); err != nil {
			return err
		}
		changed = seq < q
	}
	if changed {
		return errf(409, "conflict", "notification changed since seq %d; show the current one", seq)
	}
	return nil
}

// scopeFor: the sender's actions need publish; the user's own need read.
func scopeFor(action string, byKey bool) string {
	if action == "resolve" || byKey {
		return ScopePublish
	}
	return ScopeRead
}

// setBy records who made the change: the device name to show, the token id to compare.
func (n *Notification) setBy(t *Token) { n.StateBy, n.StateByID = t.Name, t.ID }

// transition is the one place state rules live. It mutates n and returns
// the event to log, or nil when nothing changes (same state again).
func transition(n *Notification, target string, by *Token, via string, until *time.Time) (*Event, error) {
	switch {
	case target == n.State && target != Snoozed:
		return nil, nil
	case n.Final() && (target == Dismissed || target == Resolved):
		return nil, nil // already gone from every inbox
	case n.Final() && target != Active:
		return nil, errf(409, "conflict", "notification is %s; reopen it first", n.State)
	}
	n.State, n.SnoozeUntil = target, nil
	n.setBy(by)
	if target != Active { // any answer ends the re-alerts; a wake or reopen does not restart them
		n.stopRepeat()
	} else if r := n.Repeat; r != nil && r.Next != nil { // a scheduled send delivered now: count from now
		d, _ := parseDuration(r.Every)
		next := time.Now().Add(d)
		r.Next = &next
	}
	if target != Dismissed && target != Resolved {
		n.Reply = "" // an answer stands only with the tap's own ack; dismiss and resolve keep it as history
	}
	if target == Snoozed {
		u := until.UTC().Truncate(time.Millisecond)
		n.SnoozeUntil = &u
	}
	return &Event{Type: EventState, Notification: n, Via: via}, nil
}

// Clear dismisses every open notification in topic, or the given ids.
func (s *Server) Clear(ctx context.Context, tok *Token, topic string, ids []string, via string) (int, error) {
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
			ev, _ := transition(n, Dismissed, tok, via, nil)
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
			WHERE state = 'snoozed' AND snooze_until <= ? AND (expires_at IS NULL OR expires_at > ?)`, ms(time.Now()), ms(time.Now()))
		var evs []Event
		for _, n := range ns {
			ev, _ := transition(n, Active, &Token{Name: "server"}, "snooze_end", nil)
			evs = append(evs, *ev)
		}
		return evs, err
	})
	return err
}

// parseExpires reads a publish's expires: "" for none, a duration from now,
// or an RFC3339 time. It must be in the future.
func parseExpires(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	t := time.Now()
	if d, err := time.ParseDuration(v); err == nil {
		t = t.Add(d)
	} else if at, err := time.Parse(time.RFC3339, v); err == nil {
		t = at
	} else {
		return nil, errf(400, "bad_request", "expires: want a duration like 30m or an RFC3339 time, got %q", v)
	}
	t = t.UTC().Truncate(time.Millisecond)
	if !t.After(time.Now()) {
		return nil, errf(400, "bad_request", "expires must be in the future")
	}
	return &t, nil
}

// expireDue resolves open notifications whose expires_at has passed.
func (s *Server) expireDue(ctx context.Context) error {
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		ns, err := queryNotifications(tx, `SELECT `+notifCols+` FROM notifications
			WHERE expires_at <= ? AND state NOT IN ('dismissed', 'resolved')`, ms(time.Now()))
		var evs []Event
		for _, n := range ns {
			n.ExpiresAt = nil // done: a reopen must not expire it again at once
			ev, _ := transition(n, Resolved, &Token{Name: "server"}, "expired", nil)
			evs = append(evs, *ev)
		}
		return evs, err
	})
	return err
}

// prune drops events older than the retention and cleared notifications
// older than the keep_cleared_days setting. The event log never outlives
// the cleared rows, so a client further behind gets a reset instead of
// replaying changes to notifications that are gone.
func (s *Server) prune(ctx context.Context) error {
	now := time.Now()
	cutoff := ms(now.Add(-s.opt.Retention))
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := s.loadSettings(ctx, tx)
	if err != nil {
		return err
	}
	keep := s.keepCleared(st)
	// Events go once the shortest keep in use has passed, so a replay never
	// brings back a notification a prune removed.
	shortest := s.opt.Retention
	if keep > 0 && keep < shortest {
		shortest = keep
	}
	for _, k := range st.TopicKeepClearedDays {
		if d := days(k); d > 0 && d < shortest {
			shortest = d
		}
	}
	evCutoff := cutoff
	if shortest < s.opt.Retention {
		evCutoff = ms(now.Add(-shortest))
	}
	var upTo sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM events WHERE created_at < ?`, evCutoff).Scan(&upTo); err != nil {
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
	// [] not null, since NOT IN (NULL) matches nothing.
	own, _ := json.Marshal(append([]string{}, slices.Collect(maps.Keys(st.TopicKeepClearedDays))...))
	if keep != 0 { // 0 = forever
		if _, err := tx.Exec(`DELETE FROM notifications WHERE `+prunable+` AND topic NOT IN (SELECT value FROM json_each(?))`,
			ms(now.Add(-keep)), string(own)); err != nil {
			return err
		}
	}
	for t, k := range st.TopicKeepClearedDays { // topics with their own keep (#262)
		if k != 0 {
			if _, err := tx.Exec(`DELETE FROM notifications WHERE `+prunable+` AND topic = ?`, ms(now.Add(-days(k))), t); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`DELETE FROM hook_deliveries WHERE updated_at < ?`, cutoff); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM silences WHERE until_ms < ?`, ms(time.Now())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if upTo.Valid && upTo.Int64 > s.pruned.Load() {
		s.pruned.Store(upTo.Int64)
	}
	return s.sweepFiles(ctx)
}

// sweepFiles removes uploaded files no notification points at: pruned or
// replaced ones, and uploads cut off by a crash. Anything younger than an
// hour is left alone, so an upload still being published survives.
func (s *Server) sweepFiles(ctx context.Context) error {
	entries, err := os.ReadDir(s.opt.Attachments)
	if err != nil {
		return err
	}
	for _, e := range entries {
		info, err := e.Info()
		// Only our own names: the folder may be shared with other files.
		if err != nil || e.IsDir() || time.Since(info.ModTime()) < time.Hour ||
			!fileID.MatchString(e.Name()) && !strings.HasPrefix(e.Name(), ".up-") {
			continue
		}
		var used bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM notifications WHERE file = ? AND file != '')`, e.Name()).Scan(&used); err != nil {
			return err
		}
		if !used {
			os.Remove(filepath.Join(s.opt.Attachments, e.Name()))
		}
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
	Skip   []string // topics to leave out (#320)
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
	for _, t := range topics {
		if !tok.may(ScopeRead, t) {
			return nil, errf(403, "forbidden", "token may not read topic %q", t)
		}
	}
	if len(topics) == 0 && tok.limited() {
		all, err := s.Topics(ctx, tok)
		if err != nil {
			return nil, err
		}
		for _, t := range all {
			topics = append(topics, t.Topic)
		}
		if len(topics) == 0 {
			return []*Notification{}, nil
		}
	}
	if len(topics) > 0 {
		in("topic", topics)
	}
	if len(lq.Skip) > 0 {
		in("topic NOT", lq.Skip)
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
	Icon  string `json:"icon"`
	// DeviceActions: http and broadcast actions may be published here (#401).
	DeviceActions bool `json:"device_actions,omitempty"`
}

// Topics lists topics tok may read, with open counts.
func (s *Server) Topics(ctx context.Context, tok *Token) ([]TopicCount, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT topic, SUM(open), MAX(icon), MAX(da) FROM (
		SELECT topic, state IN ('active', 'acknowledged', 'snoozed') AS open, '' AS icon, 0 AS da FROM notifications
		UNION ALL SELECT topic, 0, icon, device_actions FROM topics) GROUP BY topic ORDER BY topic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopicCount{}
	for rows.Next() {
		var t TopicCount
		if err := rows.Scan(&t.Topic, &t.Open, &t.Icon, &t.DeviceActions); err != nil {
			return nil, err
		}
		if tok.may(ScopeRead, t.Topic) {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// Counts returns exactly how many notifications tok may read are in each
// state, plus inbox (active and acknowledged) and cleared (dismissed and
// resolved), as the inbox sidebar shows them.
func (s *Server) Counts(ctx context.Context, tok *Token) (map[string]int, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT topic, state, COUNT(*) FROM notifications GROUP BY topic, state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{Active: 0, Acknowledged: 0, Snoozed: 0, Dismissed: 0, Resolved: 0}
	for rows.Next() {
		var topic, state string
		var k int
		if err := rows.Scan(&topic, &state, &k); err != nil {
			return nil, err
		}
		if tok.may(ScopeRead, topic) {
			out[state] += k
		}
	}
	out["inbox"], out["cleared"] = out[Active]+out[Acknowledged], out[Dismissed]+out[Resolved]
	return out, rows.Err()
}

// SetTopicIcon sets the icon new notifications in topic get when their
// sender gives none. "" removes it. Rows already published keep theirs.
func (s *Server) SetTopicIcon(ctx context.Context, tok *Token, topic, icon string) error {
	if err := validTopic(topic); err != nil {
		return err
	}
	if err := tok.need(ScopePublish, topic); err != nil {
		return err
	}
	if err := checkImageURL("icon", icon); err != nil {
		return err
	}
	if icon == "" { // keep the row while it holds a check-in or the device actions switch
		if _, err := s.db.ExecContext(ctx, `DELETE FROM topics WHERE topic = ? AND checkin_ms IS NULL AND device_actions = 0`, topic); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `UPDATE topics SET icon = '' WHERE topic = ?`, topic)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO topics (topic, icon) VALUES (?, ?)
		ON CONFLICT(topic) DO UPDATE SET icon = excluded.icon`, topic, icon)
	return err
}

// checkinID is the external_id of clrd's own missed check-in alert.
const checkinID = "clrd-checkin"

// serverToken publishes and resolves clrd's own alerts.
var serverToken = &Token{Name: "server", Scopes: []string{ScopeAdmin}}

// SetTopicCheckin makes topic expect a message at least every interval (a
// dead man's switch); 0 turns it off. The timer starts now.
func (s *Server) SetTopicCheckin(ctx context.Context, tok *Token, topic string, every time.Duration) error {
	if err := validTopic(topic); err != nil {
		return err
	}
	if err := tok.need(ScopePublish, topic); err != nil {
		return err
	}
	if every != 0 && (every < time.Minute || every > 366*24*time.Hour) {
		return errf(400, "bad_request", "checkin must be 1m to 8784h")
	}
	// Turning it off or changing it starts over, so an open alert is resolved.
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		q, args := `INSERT INTO topics (topic, checkin_ms, checkin_at) VALUES (?, ?, ?)
			ON CONFLICT(topic) DO UPDATE SET checkin_ms = excluded.checkin_ms, checkin_at = excluded.checkin_at, checkin_late = 0`,
			[]any{topic, every.Milliseconds(), ms(time.Now())}
		if every == 0 {
			q, args = `UPDATE topics SET checkin_ms = NULL, checkin_at = NULL, checkin_late = 0 WHERE topic = ?`, []any{topic}
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return nil, err
		}
		n, err := getNotification(ctx, tx, `topic = ? AND external_id = ? AND state NOT IN ('dismissed', 'resolved')`, topic, checkinID)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		ev, err := transition(n, Resolved, serverToken, "checkin", nil)
		if ev == nil {
			return nil, err
		}
		return []Event{*ev}, err
	})
	return err
}

// errOnTime: the topic checked in between the sweep's query and its alert.
var errOnTime = errors.New("checked in")

// checkIn runs in a publish's write. clrd's own alert flags the topic late,
// or gives up with errOnTime if a message got there first. Any other message
// restarts the timer and, if the topic was late, resolves the alert.
func (s *Server) checkIn(ctx context.Context, tx *sql.Tx, in PublishInput) (*Event, error) {
	t := time.Now()
	if in.checkin {
		res, err := tx.ExecContext(ctx, `UPDATE topics SET checkin_late = 1 WHERE topic = ?
			AND checkin_ms IS NOT NULL AND checkin_late = 0 AND checkin_at + checkin_ms <= ?`, in.Topic, ms(t))
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, errOnTime
		}
		return nil, nil
	}
	var late bool
	if err := tx.QueryRowContext(ctx, `SELECT checkin_late FROM topics WHERE topic = ? AND checkin_ms IS NOT NULL`,
		in.Topic).Scan(&late); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE topics SET checkin_at = ?, checkin_late = 0 WHERE topic = ?`, ms(t), in.Topic); err != nil {
		return nil, err
	}
	if !late {
		return nil, nil
	}
	n, err := getNotification(ctx, tx, `topic = ? AND external_id = ? AND state NOT IN ('dismissed', 'resolved')`, in.Topic, checkinID)
	if isNotFound(err) { // someone already cleared it
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return transition(n, Resolved, serverToken, "checkin", nil)
}

// checkins raises an urgent alert in each topic whose check-in is overdue.
// The next message to the topic resolves it (see Publish).
func (s *Server) checkins(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT topic, checkin_ms FROM topics
		WHERE checkin_ms IS NOT NULL AND checkin_late = 0 AND checkin_at + checkin_ms <= ?`, ms(time.Now()))
	if err != nil {
		return err
	}
	type due struct {
		topic string
		every time.Duration
	}
	var ds []due
	for rows.Next() {
		var d due
		var every int64
		if err := rows.Scan(&d.topic, &every); err != nil {
			rows.Close()
			return err
		}
		d.every = time.Duration(every) * time.Millisecond
		ds = append(ds, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range ds {
		if _, err := s.Publish(ctx, serverToken, PublishInput{Topic: d.topic, ExternalID: checkinID, Priority: 5,
			Title: d.topic + " missed its check-in", Message: "No message in " + d.every.String() + ".",
			Tags: []string{"rotating_light"}, checkin: true}); err != nil && !errors.Is(err, errOnTime) {
			return err
		}
	}
	return nil
}

// checkImageURL accepts "" or an absolute http(s) URL: clients load it as an
// image, so no javascript:, data: or relative paths.
func checkImageURL(field, v string) error {
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err == nil && u.Port() != "" {
		// Go accepts any digits as a port; browsers refuse anything over 65535.
		_, err = strconv.ParseUint(u.Port(), 10, 16)
	}
	if err == nil && strings.HasPrefix(u.Host, "[") && net.ParseIP(u.Hostname()) == nil {
		err = errors.New("bracketed host is not an IP")
	}
	if len(v) > 2048 || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return errf(400, "bad_request", "%s must be an http(s) URL of at most 2048 characters", field)
	}
	return nil
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

// fill checks a new notification and sets the defaults Publish stores; a
// template preview runs it too, so it shows what a post would store.
func (in *PublishInput) fill() error {
	if in.Priority == 0 {
		in.Priority = 3
	}
	if in.Priority < 1 || in.Priority > 5 {
		return errf(400, "bad_request", "priority must be 1 to 5")
	}
	if len(in.ExternalID) > 256 {
		return errf(400, "bad_request", "external_id longer than 256")
	}
	if in.ExternalID == checkinID && !in.checkin {
		return errf(400, "bad_request", "external_id %s is reserved for missed check-in alerts", checkinID)
	}
	if in.ExternalID == "." || in.ExternalID == ".." { // a URL path would drop it
		return errf(400, "bad_request", "external_id cannot be . or ..")
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
		return err
	}
	return nil
}
