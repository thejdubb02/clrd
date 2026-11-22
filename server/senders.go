package server

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	Rules     SenderRules     `json:"rules"`
	Alarm     string          `json:"alarm,omitempty"` // the topic of its quiet alarm, until it publishes
	Recent    []*Notification `json:"recent,omitempty"`
	recent    []string
}

// senderKey is the senders row for the token tree top: the tree itself, or
// for the shared anonymous token one row per address (no token id).
func senderKey(ctx context.Context, top string) (string, any) {
	if top != anonymousID && !strings.HasPrefix(top, anonymousID+"-") {
		return top, top
	}
	h, _ := ctx.Value(senderHintKey{}).(senderHint)
	sum := sha256.Sum256([]byte(h.addr))
	return anonymousID + "-" + hex.EncodeToString(sum[:6]), nil
}

// seeSender counts one stored publish for the sender whose top token is top,
// in the publish's own write. clrd's own alerts (top "") are no sender.
func seeSender(ctx context.Context, tx *sql.Tx, top, topic, template, id string) ([]Event, error) {
	if top == "" {
		return nil, nil
	}
	h, _ := ctx.Value(senderHintKey{}).(senderHint)
	key, tokID := senderKey(ctx, top)
	var topicsJSON, recentJSON string
	err := tx.QueryRowContext(ctx, `SELECT topics, recent FROM senders WHERE id = ?`, key).Scan(&topicsJSON, &recentJSON)
	if errors.Is(err, sql.ErrNoRows) {
		topicsJSON, recentJSON = "{}", "[]"
		if tokID == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM senders WHERE id IN (SELECT id FROM senders WHERE token_id IS NULL AND rules = '{}'
				ORDER BY last_seen DESC LIMIT -1 OFFSET ?)`, senderAnonLimit-1); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}
	topics, recent := map[string]int{}, []string{}
	json.Unmarshal([]byte(topicsJSON), &topics)
	json.Unmarshal([]byte(recentJSON), &recent)
	topics[topic]++
	if id != "" { // "" is a publish the sender's rules dropped: counted, nothing stored
		recent = append([]string{id}, slices.DeleteFunc(recent, func(r string) bool { return r == id })...)
		recent = recent[:min(len(recent), senderRecent)]
	}
	tb, _ := json.Marshal(topics)
	rb, _ := json.Marshal(recent)
	t := ms(now())
	// A token revoked mid-publish is no sender, never a failed publish.
	if _, err = tx.ExecContext(ctx, `INSERT INTO senders (id, token_id, user_agent, address, template, first_seen, last_seen, count, topics, recent, last_topic)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?6, 1, ?7, ?8, ?9 WHERE ?2 IS NULL OR EXISTS (SELECT 1 FROM tokens WHERE id = ?2)
		ON CONFLICT(id) DO UPDATE SET user_agent = excluded.user_agent, address = excluded.address,
		  template = CASE WHEN ?5 = '' THEN template ELSE ?5 END, first_seen = CASE WHEN count = 0 THEN ?6 ELSE first_seen END,
		  last_seen = ?6, count = count + 1, topics = ?7, recent = ?8, last_topic = ?9`,
		key, tokID, h.ua, h.addr, template, t, string(tb), string(rb), topic); err != nil {
		return nil, err
	}
	return endQuiet(ctx, tx, key) // it spoke: its quiet alarm is over
}

// quietPrefix starts the external_id of a sender's quiet alarm (#119).
const quietPrefix = "clrd-quiet-"

// endQuiet resolves a sender's open quiet alarm, if it has one.
func endQuiet(ctx context.Context, tx *sql.Tx, key string) ([]Event, error) {
	var topic string
	if err := tx.QueryRowContext(ctx, `SELECT alarm_topic FROM senders WHERE id = ?`, key).Scan(&topic); err != nil || topic == "" {
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE senders SET alarm_topic = '' WHERE id = ?`, key); err != nil {
		return nil, err
	}
	n, err := getNotification(ctx, tx, `topic = ? AND external_id = ? AND state NOT IN ('dismissed', 'resolved')`, topic, quietPrefix+key)
	if isNotFound(err) { // someone already cleared it
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ev, err := transition(n, Resolved, serverToken, "quiet", nil)
	if ev == nil {
		return nil, err
	}
	return []Event{*ev}, err
}

// quietSenders raises an urgent alert for each sender whose rules expect a
// message at least every so often and that has gone quiet that long. Its
// next publish, kept or dropped, resolves it.
func (s *Server) quietSenders(ctx context.Context) error {
	// A sender last heard before last_topic existed falls back to its busiest topic.
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, COALESCE(t.name, 'anonymous'), s.rules,
		  COALESCE(NULLIF(s.last_topic, ''), (SELECT key FROM json_each(s.topics) ORDER BY value DESC LIMIT 1), ''), MAX(s.last_seen, s.alarm_from)
		FROM senders s LEFT JOIN tokens t ON t.id = s.token_id WHERE s.alarm_topic = '' AND s.rules LIKE '%"expect"%'`)
	if err != nil {
		return err
	}
	var due []PublishInput
	for rows.Next() {
		var id, name, raw, last string
		var since int64
		if err := rows.Scan(&id, &name, &raw, &last, &since); err != nil {
			rows.Close()
			return err
		}
		var r SenderRules
		json.Unmarshal([]byte(raw), &r)
		every, _ := time.ParseDuration(r.Expect)
		topic := r.AlertTopic
		if to := r.route(last); topic == "" && len(to) > 0 {
			topic = to[0]
		} else if topic == "" {
			topic = last // its last topic drops everything: alert there
		}
		if every <= 0 || topic == "" || since+every.Milliseconds() > ms(now()) {
			continue // no topic yet: it has never published and has no alert_topic
		}
		due = append(due, PublishInput{Topic: topic, ExternalID: quietPrefix + id, Priority: 5, Title: name + " went quiet",
			Message: "No message in " + every.String() + ".", Tags: []string{"rotating_light"}, quiet: id, quietMS: every.Milliseconds()})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, in := range due {
		if _, err := s.Publish(ctx, serverToken, in); err != nil && !errors.Is(err, errOnTime) {
			s.log.Warn("quiet sender alarm", "sender", in.quiet, "topic", in.Topic, "err", err) // the rest still get theirs
		}
	}
	return nil
}

const senderCols = `s.id, COALESCE(s.token_id, ''), COALESCE(t.name, 'anonymous'),
  CASE WHEN s.token_id IS NULL THEN 'anonymous'
    WHEN EXISTS (SELECT 1 FROM users u WHERE u.token_id = s.token_id) THEN 'ntfy_user'
    WHEN EXISTS (SELECT 1 FROM gotify_apps g WHERE g.token_id = s.token_id) THEN 'gotify_app'
    WHEN s.token_id LIKE 'pushover-%' THEN 'pushover_app'
    WHEN t.user_id IS NOT NULL THEN 'person' ELSE 'token' END,
  s.template, s.user_agent, s.address, s.first_seen, s.last_seen, s.count, s.topics, s.recent, s.rules, s.alarm_topic
  FROM senders s LEFT JOIN tokens t ON t.id = s.token_id`

func scanSender(r scanner) (*Sender, error) {
	var sd Sender
	var first, last int64
	var topics, recent, rules string
	if err := r.Scan(&sd.ID, &sd.TokenID, &sd.Name, &sd.Kind, &sd.Template, &sd.UserAgent, &sd.Address,
		&first, &last, &sd.Count, &topics, &recent, &rules, &sd.Alarm); err != nil {
		return nil, err
	}
	sd.FirstSeen, sd.LastSeen = fromMS(first), fromMS(last)
	json.Unmarshal([]byte(rules), &sd.Rules)
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
	s.api("PUT /api/v1/senders/{id}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
		if err != nil {
			return errf(400, "bad_request", "cannot read body: %v", err)
		}
		sd, err := s.SetSenderRules(r.Context(), tok, r.PathValue("id"), body)
		if err != nil {
			return err
		}
		writeJSON(w, 200, sd)
		return nil
	})
	s.api("GET /api/v1/senders/{id}/changes", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		out, err := s.SenderChanges(r.Context(), tok, r.PathValue("id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"changes": out})
		return nil
	})
	s.api("POST /api/v1/senders/{id}/changes/{seq}/undo", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
		if err != nil {
			return errf(400, "bad_request", "seq is a number")
		}
		sd, err := s.UndoSenderChange(r.Context(), tok, r.PathValue("id"), seq)
		if err != nil {
			return err
		}
		writeJSON(w, 200, sd)
		return nil
	})
}

// SenderRules are what an admin sets on a sender (#119). The zero value
// changes nothing: a sender without rules publishes exactly as before.
type SenderRules struct {
	Paused      bool `json:"paused"`       // accept and drop every publish: 2xx, nothing stored
	Muted       bool `json:"muted"`        // store and show, never alert (listed silenced)
	PriorityMin int  `json:"priority_min"` // 1 to 5, 0 is none
	PriorityMax int  `json:"priority_max"`
	// Routes sends a publish to other topics: requested topic, or "*", to
	// targets. The exact topic wins over "*"; no match keeps the topic; an
	// empty list drops it.
	Routes map[string][]string `json:"routes,omitempty"`
	// Expect raises a quiet alarm when the sender sends nothing for this
	// long ("25h"), in AlertTopic, or else where its last topic routes to.
	Expect     string `json:"expect,omitempty"`
	AlertTopic string `json:"alert_topic,omitempty"`
}

// none is true when no rule touches a publish (Expect only watches).
func (r SenderRules) none() bool {
	return !r.Paused && !r.Muted && r.PriorityMin == 0 && r.PriorityMax == 0 && len(r.Routes) == 0
}

func (r SenderRules) check() error {
	if r.PriorityMin < 0 || r.PriorityMin > 5 || r.PriorityMax < 0 || r.PriorityMax > 5 ||
		(r.PriorityMin > 0 && r.PriorityMax > 0 && r.PriorityMin > r.PriorityMax) {
		return errf(400, "bad_request", "priority_min and priority_max are 0 (none) or 1 to 5, min at most max")
	}
	if d, err := time.ParseDuration(r.Expect); r.Expect != "" && (err != nil || d < time.Minute || d > 366*24*time.Hour) {
		return errf(400, "bad_request", `expect must be a duration from 1m to 8784h, like "25h"`)
	}
	if r.AlertTopic != "" {
		if err := validTopic(r.AlertTopic); err != nil {
			_, _, m := errorStatus(err) // named, so a form can show it by the field
			return errf(400, "bad_request", "alert_topic: %s", m)
		}
	}
	for from, to := range r.Routes {
		if from != "*" {
			if err := validTopic(from); err != nil {
				return err
			}
		}
		for _, t := range to {
			if err := validTopic(t); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r SenderRules) route(topic string) []string {
	if to, ok := r.Routes[topic]; ok {
		return to
	}
	if to, ok := r.Routes["*"]; ok {
		return to
	}
	return []string{topic}
}

// see counts the publish for its sender once, under the topic it named.
func (in *PublishInput) see(ctx context.Context, tx *sql.Tx, top, id string) ([]Event, error) {
	if in.counted {
		return nil, nil
	}
	return seeSender(ctx, tx, top, cmp.Or(in.requested, in.Topic), in.template, id)
}

// Publish applies the sender's rules, then creates or updates the
// notification in each topic the rules send it to.
func (s *Server) Publish(ctx context.Context, tok *Token, in PublishInput) (*Notification, error) {
	if err := validTopic(in.Topic); err != nil {
		return nil, err
	}
	if err := tok.need(ScopePublish, in.Topic); err != nil {
		return nil, err
	}
	top, err := topIn(ctx, s.db, tok.ID)
	if err != nil {
		return nil, err
	}
	var rules SenderRules
	if top != "" {
		key, _ := senderKey(ctx, top)
		var raw string
		if err := s.db.QueryRowContext(ctx, `SELECT rules FROM senders WHERE id = ?`, key).Scan(&raw); err != nil &&
			!errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		json.Unmarshal([]byte(raw), &rules)
	}
	if rules.none() {
		return s.publishTo(ctx, tok, in)
	}
	targets := rules.route(in.Topic)
	if rules.Paused || len(targets) == 0 {
		return s.discard(ctx, tok, top, in)
	}
	// The admin's route needs no grant on its target: the sender may publish where it asked.
	rt := *tok
	rt.Topics, rt.Grants = nil, nil
	in.muted, in.prioMin, in.prioMax = rules.Muted, rules.PriorityMin, rules.PriorityMax
	// Each copy owns its file, so clearing one leaves the rest. All are linked
	// before the first publish: a replace of that row deletes the source (#536).
	files := make([]string, len(targets))
	for i := 1; i < len(targets) && in.file != ""; i++ {
		if files[i], err = s.linkFile(in.file); err != nil {
			s.log.Warn("sender route: copy attachment", "topic", targets[i], "err", err)
			files[i] = ""
		}
	}
	var first *Notification
	var firstErr error
	for i, t := range targets {
		one := in
		one.Topic, one.counted = t, i > 0
		if t != in.Topic {
			one.requested = in.Topic
		}
		if i > 0 && in.file != "" {
			if one.file = files[i]; one.file == "" {
				continue
			}
			a := *in.Attachment
			a.URL, one.Attachment = "/api/v1/files/"+one.file, &a
		}
		n, err := s.publishTo(ctx, &rt, one)
		if i == 0 && routeGap != nil {
			routeGap(in.file)
		}
		if err != nil && i > 0 && one.file != "" {
			os.Remove(filepath.Join(s.opt.Attachments, one.file))
		}
		switch {
		case err == nil && first == nil:
			first = senderView(tok, n, in.Status != "resolved")
		case err != nil && i == 0:
			firstErr = err
			if in.Status != "resolved" || !isNotFound(err) {
				for _, f := range files[1:] {
					if f != "" {
						os.Remove(filepath.Join(s.opt.Attachments, f))
					}
				}
				return nil, err // the first target fails as an unrouted publish would
			}
		case err != nil && !(in.Status == "resolved" && isNotFound(err)):
			s.log.Warn("sender route: publish", "topic", t, "err", err)
		}
	}
	if first == nil {
		return nil, firstErr // a resolve no target had open
	}
	return first, nil
}

// discard is a publish the sender's rules drop: counted, its upload removed,
// and a 2xx with what was sent so every sender and dialect sees success.
func (s *Server) discard(ctx context.Context, tok *Token, top string, in PublishInput) (*Notification, error) {
	if in.file != "" {
		os.Remove(filepath.Join(s.opt.Attachments, in.file))
	} else if err := s.publishLimit(tok, in.Topic); err != nil {
		return nil, err
	}
	if err := in.fill(); err != nil { // a bad publish is refused, not counted
		return nil, err
	}
	if _, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		return seeSender(ctx, tx, top, in.Topic, in.template, "")
	}); err != nil {
		return nil, err
	}
	t := now()
	return &Notification{ID: s.newID(t), Topic: in.Topic, ExternalID: in.ExternalID, Title: in.Title, Message: in.Message,
		Priority: in.Priority, Tags: in.Tags, State: Active, CreatedAt: t, UpdatedAt: t, Live: true}, nil
}

// routeGap, set only by tests, runs right after a routed publish's first
// copy commits, where a replace of that row can land.
var routeGap func(file string)

// linkFile gives an uploaded file a second id on disk, sharing its bytes.
func (s *Server) linkFile(id string) (string, error) {
	b := make([]byte, 16)
	rand.Read(b)
	nid := hex.EncodeToString(b)
	return nid, os.Link(filepath.Join(s.opt.Attachments, id), filepath.Join(s.opt.Attachments, nid))
}

// SenderChange is one edit of a sender's rules, kept so it can be undone.
type SenderChange struct {
	Seq    int64       `json:"seq"`
	Before SenderRules `json:"before"`
	After  SenderRules `json:"after"`
	By     string      `json:"by"`
	ByID   string      `json:"by_id"`
	At     time.Time   `json:"at"`
}

// SetSenderRules merges patch (JSON, any subset of the rules; routes, when
// given, replaces them all) into the sender's rules and records the change.
// id is a sender listed, or a token not yet heard from.
func (s *Server) SetSenderRules(ctx context.Context, tok *Token, id string, patch []byte) (*Sender, error) {
	if err := tok.needFullAdmin(); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &fields); err != nil {
		return nil, errf(400, "bad_request", "bad JSON body: %v", err)
	}
	var base *int64 // the newest change seq the caller saw, if it says (#543)
	if raw, ok := fields["base_seq"]; ok {
		if err := json.Unmarshal(raw, &base); err != nil {
			return nil, errf(400, "bad_request", "base_seq: %v", err)
		}
	}
	return s.setRules(ctx, tok, id, func(r *SenderRules, last int64) error {
		if base != nil && *base != last {
			return errf(409, "conflict", "the rules changed since you loaded them; reload and try again")
		}
		if _, ok := fields["routes"]; ok {
			r.Routes = nil
		}
		if err := json.Unmarshal(patch, r); err != nil {
			return errf(400, "bad_request", "bad JSON body: %v", err)
		}
		return nil
	})
}

// UndoSenderChange puts back the rules from before change seq, as a new change.
func (s *Server) UndoSenderChange(ctx context.Context, tok *Token, id string, seq int64) (*Sender, error) {
	if err := tok.needFullAdmin(); err != nil {
		return nil, err
	}
	var before string
	err := s.db.QueryRowContext(ctx, `SELECT before FROM sender_changes WHERE sender = ? AND seq = ?`, id, seq).Scan(&before)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(404, "not_found", "no change %d on sender %q", seq, id)
	} else if err != nil {
		return nil, err
	}
	return s.setRules(ctx, tok, id, func(r *SenderRules, _ int64) error {
		*r = SenderRules{}
		return json.Unmarshal([]byte(before), r)
	})
}

// edit gets the rules and the seq of the sender's newest change (0 for none).
func (s *Server) setRules(ctx context.Context, tok *Token, id string, edit func(r *SenderRules, last int64) error) (*Sender, error) {
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT rules FROM senders WHERE id = ?`, id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) { // a token that has not published yet: its row starts here
			if err := tx.QueryRowContext(ctx, `SELECT '{}' FROM tokens WHERE id = ?
				AND id NOT IN (SELECT token_id FROM sessions)`, id).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
				return nil, errf(404, "not_found", "no sender or top-level token %q", id)
			} else if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO senders (id, token_id, first_seen, last_seen, count, topics, recent)
				VALUES (?1, ?1, 0, 0, 0, '{}', '[]')`, id); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		var before, after SenderRules
		json.Unmarshal([]byte(raw), &before)
		json.Unmarshal([]byte(raw), &after)
		var last int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM sender_changes WHERE sender = ?`, id).Scan(&last); err != nil {
			return nil, err
		}
		if err := edit(&after, last); err != nil {
			return nil, err
		}
		if err := after.check(); err != nil {
			return nil, err
		}
		bb, _ := json.Marshal(before)
		ab := []byte("{}")
		if !after.none() || after.Expect != "" || after.AlertTopic != "" {
			ab, _ = json.Marshal(after)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE senders SET rules = ? WHERE id = ?`, string(ab), id); err != nil {
			return nil, err
		}
		var evs []Event
		if before.Expect != after.Expect || before.AlertTopic != after.AlertTopic { // the quiet timer starts over
			if _, err := tx.ExecContext(ctx, `UPDATE senders SET alarm_from = ? WHERE id = ?`, ms(now()), id); err != nil {
				return nil, err
			}
			if evs, err = endQuiet(ctx, tx, id); err != nil {
				return nil, err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sender_changes (sender, before, after, by_name, by_id, at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, string(bb), string(ab), tok.Name, tok.ID, ms(now()))
		return evs, err
	})
	if err != nil {
		return nil, err
	}
	return s.Sender(ctx, tok, id)
}

// SenderChanges lists a sender's rule changes, newest first.
func (s *Server) SenderChanges(ctx context.Context, tok *Token, id string) ([]SenderChange, error) {
	if err := tok.needFullAdmin(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, before, after, by_name, by_id, at FROM sender_changes
		WHERE sender = ? ORDER BY seq DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SenderChange{}
	for rows.Next() {
		var c SenderChange
		var b, a string
		var at int64
		if err := rows.Scan(&c.Seq, &b, &a, &c.By, &c.ByID, &at); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(b), &c.Before)
		json.Unmarshal([]byte(a), &c.After)
		c.At = fromMS(at)
		out = append(out, c)
	}
	return out, rows.Err()
}
