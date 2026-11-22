package server

// Outgoing webhooks (issue #112): a topic's state changes, POSTed to a URL.
// Deliveries are queued in the event's own transaction, so none is lost to a
// crash or a slow receiver, then sent by a background job with retries.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
)

// hookEvents are the names a hook can subscribe to. "action" is a tapped
// reply button; it also reaches hooks that subscribe to "acknowledged".
var hookEvents = []string{Dismissed, Acknowledged, Resolved, Snoozed, "action"}

var (
	hookEvery        = 2 * time.Second  // how often due deliveries are sent
	hookBackoff      = 30 * time.Second // first retry; doubles each time
	hookDisableAfter = 24 * time.Hour   // failing this long turns the hook off
)

const (
	hookTries = 8 // attempts per delivery: about an hour of retries
	hookBatch = 50
)

// OutHook is one outgoing webhook on a topic.
type OutHook struct {
	Name         string     `json:"name"`
	URL          string     `json:"url"`
	Events       []string   `json:"events"`
	Secret       string     `json:"secret,omitempty"` // write only
	HasSecret    bool       `json:"has_secret"`
	Enabled      bool       `json:"enabled"`
	FailingSince *time.Time `json:"failing_since"`
	Body         string     `json:"body"` // a template for the POST body; "" sends the event (#239)
}

// queueHooks adds a delivery for every enabled hook on the event's topic that
// subscribes to it. Runs inside write's transaction.
func queueHooks(tx *sql.Tx, e *Event, t time.Time) error {
	if e.Type != EventState {
		return nil
	}
	ev, alt := e.Notification.State, e.Notification.State
	if e.replied {
		ev, alt = "action", Acknowledged
	}
	_, err := tx.Exec(`INSERT INTO hook_deliveries (topic, name, seq, event, body, next_at, updated_at)
		SELECT h.topic, h.name, ?, ?, ?, ?, ? FROM hooks_out h
		WHERE h.topic = ? AND h.enabled = 1 AND EXISTS (SELECT 1 FROM json_each(h.events) WHERE value IN (?, ?))`,
		e.Seq, ev, string(e.Raw), ms(t), ms(t), e.Notification.Topic, ev, alt)
	return err
}

type hookJob struct {
	id                 int64
	topic, name, event string
	url, secret, body  string
	tmpl               string // the hook's body template, rendered at each try
	attempts           int
}

// parseHookBody compiles a hook's body template with the incoming
// templates' functions and limits, plus json, which quotes a value so a title
// with a quote or a newline still makes valid JSON.
func parseHookBody(src string) (*template.Template, error) {
	if len(src) > webhookFieldMax {
		return nil, errf(400, "bad_request", "template body: over 16 KB")
	}
	t, err := template.New("body").Funcs(webhookFuncs(nil)).Funcs(template.FuncMap{"json": func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}}).Option("missingkey=zero").Parse(src)
	if err != nil {
		return nil, errf(400, "bad_request", "template body: %v", err)
	}
	if len(t.Templates()) > 1 {
		return nil, errf(400, "bad_request", "template: define and block are not allowed")
	}
	if err := plain(t.Root); err != nil {
		return nil, errf(400, "bad_request", "template body: %v", err)
	}
	return t, nil
}

// renderHookBody renders src against one sync stream event and checks the
// result is JSON, as the Content-Type says.
func renderHookBody(src, event string) (string, error) {
	t, err := parseHookBody(src)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(strings.NewReader(event))
	dec.UseNumber() // a seq of 1000000 prints as 1000000, not 1e+06
	var data any
	if err := dec.Decode(&data); err != nil {
		return "", err
	}
	out, err := render(t, "body", data)
	if err != nil {
		return "", err
	}
	if !json.Valid([]byte(out)) {
		return "", errf(400, "bad_request", "template body: output is not valid JSON")
	}
	return out, nil
}

// deliverHooks sends every due delivery. Only database errors fail the job;
// a receiver's failure is recorded on its delivery.
func (s *Server) deliverHooks(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.topic, d.name, d.event, h.url, h.secret, d.body, h.body, d.attempts
		FROM hook_deliveries d JOIN hooks_out h ON h.topic = d.topic AND h.name = d.name
		WHERE d.status = 'pending' AND d.next_at <= ? AND h.enabled = 1 ORDER BY d.id LIMIT ?`, ms(now()), hookBatch)
	if err != nil {
		return err
	}
	var jobs []hookJob
	for rows.Next() {
		var j hookJob
		if err := rows.Scan(&j.id, &j.topic, &j.name, &j.event, &j.url, &j.secret, &j.body, &j.tmpl, &j.attempts); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// ponytail: 8 sends at a time, so one dead receiver cannot hold up the rest
	// for long; deliveries to one hook may arrive out of order (body has seq).
	errs := make([]error, len(jobs))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			code, sendErr := s.sendHook(ctx, j)
			if ctx.Err() != nil {
				return // shutting down: try again next start
			}
			errs[i] = s.recordHook(ctx, j, code, sendErr)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// sendHook POSTs one delivery through the guarded webhook client (no private
// addresses, ports 80 and 443 only, unless the admin's allow list says
// otherwise, no proxy) and never follows a redirect. It returns the status code, or an error when none came back. A
// body template that fails to render is an error too, and nothing is sent.
func (s *Server) sendHook(ctx context.Context, j hookJob) (int, error) {
	if j.tmpl != "" {
		body, err := renderHookBody(j.tmpl, j.body)
		if err != nil {
			return 0, err
		}
		j.body = body // what is signed is what is sent
	}
	req, err := http.NewRequestWithContext(ctx, "POST", j.url, strings.NewReader(j.body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "clrd")
	req.Header.Set("X-Clrd-Event", j.event)
	req.Header.Set("X-Clrd-Delivery", strconv.FormatInt(j.id, 10))
	if j.secret != "" {
		mac := hmac.New(sha256.New, []byte(j.secret))
		mac.Write([]byte(j.body))
		req.Header.Set("X-Clrd-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	if _, err := s.hookAllowList(ctx); err != nil { // the dial checks the list in memory
		return 0, err
	}
	resp, err := s.hookHTTP.Load().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// recordHook stores a send's outcome: 2xx clears the hook's failure streak;
// anything else schedules a retry, and a streak past hookDisableAfter turns
// the hook off and tells the topic.
func (s *Server) recordHook(ctx context.Context, j hookJob, code int, sendErr error) error {
	// The send is over: finish recording it even if shutdown starts now, so a
	// hook turned off below always gets its notice (#240).
	ctx = context.WithoutCancel(ctx)
	t := now()
	// mine limits each hook update to a delivery that still exists: a hook
	// deleted while this send was out took its deliveries with it, and one
	// saved since under the same name must not inherit this outcome (#240).
	const mine = ` AND EXISTS (SELECT 1 FROM hook_deliveries WHERE id = ?)`
	if sendErr == nil && code >= 200 && code < 300 {
		if _, err := s.db.ExecContext(ctx, `UPDATE hook_deliveries SET status = 'ok', attempts = attempts + 1, code = ?, error = '', updated_at = ? WHERE id = ?`,
			code, ms(t), j.id); err != nil {
			return err
		}
		// Only while on: a hook another send just turned off keeps its streak.
		_, err := s.db.ExecContext(ctx, `UPDATE hooks_out SET failing_since = NULL WHERE topic = ? AND name = ? AND enabled = 1`+mine, j.topic, j.name, j.id)
		return err
	}
	msg := fmt.Sprintf("HTTP %d", code)
	if sendErr != nil {
		msg = sendErr.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
	}
	status, next := "pending", t.Add(hookBackoff<<j.attempts)
	if j.attempts+1 >= hookTries {
		status = "failed"
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE hook_deliveries SET status = ?, attempts = attempts + 1, next_at = ?, code = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, ms(next), code, msg, ms(t), j.id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE hooks_out SET failing_since = COALESCE(failing_since, ?) WHERE topic = ? AND name = ?`+mine,
		ms(t), j.topic, j.name, j.id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE hooks_out SET enabled = 0 WHERE topic = ? AND name = ? AND enabled = 1 AND failing_since <= ?`+mine,
		j.topic, j.name, ms(t.Add(-hookDisableAfter)), j.id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		host := j.url
		if u, err := url.Parse(j.url); err == nil {
			host = u.Host
		}
		_, err = s.Publish(ctx, serverToken, PublishInput{Topic: j.topic, ExternalID: "webhook-off-" + j.name, Priority: 4,
			Title: "Webhook turned off", Format: FormatText,
			Message: fmt.Sprintf("Webhook %q to %s failed for %.0f hours and is now off. Last error: %s. Save it again to turn it back on.",
				j.name, host, hookDisableAfter.Hours(), msg)})
	}
	return err
}

// hookOwner checks a hook's topic and name and that tok may manage hooks.
// ponytail: admin only. A hook sends a topic's answers to any URL, so a
// per-topic publish token could leak them; widen it if someone asks.
func hookOwner(tok *Token, topic, name string) error {
	if topic != "" {
		if err := validTopic(topic); err != nil {
			return err
		}
	}
	if name != "" && !webhookNameRe.MatchString(name) {
		return errf(400, "bad_request", "invalid webhook name %q: use letters, digits, _ and -, at most 64", name)
	}
	return tok.need(ScopeAdmin, "")
}

// TopicHook is an outgoing hook with its topic, for the web inbox.
type TopicHook struct {
	Topic string `json:"topic"`
	OutHook
}

// OutHooks lists the hooks on topic, or on every topic when topic is "".
func (s *Server) OutHooks(ctx context.Context, tok *Token, topic string) ([]TopicHook, error) {
	if err := hookOwner(tok, topic, ""); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT topic, name, url, events, secret != '', enabled, failing_since, body
		FROM hooks_out WHERE ? = '' OR topic = ? ORDER BY topic, name`, topic, topic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopicHook{}
	for rows.Next() {
		var h TopicHook
		var events string
		var since sql.NullInt64
		if err := rows.Scan(&h.Topic, &h.Name, &h.URL, &events, &h.HasSecret, &h.Enabled, &since, &h.Body); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(events), &h.Events)
		if since.Valid {
			v := fromMS(since.Int64)
			h.FailingSince = &v
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SaveHook adds or replaces a hook. With keepSecret, an empty h.Secret keeps
// the one already saved instead of removing it; keepBody does the same for
// h.Body, for the web form, which has no body field.
func (s *Server) SaveHook(ctx context.Context, tok *Token, topic, name string, h OutHook, keepSecret, keepBody bool) error {
	if err := hookOwner(tok, topic, name); err != nil {
		return err
	}
	if topic == "" || name == "" {
		return errf(400, "bad_request", "a webhook needs a topic and a name")
	}
	if u, err := url.Parse(h.URL); err != nil || len(h.URL) > 2048 || checkURL(u) != nil {
		return errf(400, "bad_request", "url must be an http(s) URL of at most 2048 characters")
	}
	if len(h.Events) == 0 {
		h.Events = hookEvents
	}
	for _, e := range h.Events {
		if !slices.Contains(hookEvents, e) {
			return errf(400, "bad_request", "unknown event %q: use %s", e, strings.Join(hookEvents, ", "))
		}
	}
	if _, err := parseHookBody(h.Body); err != nil {
		return err
	}
	events, _ := json.Marshal(h.Events)
	// Saving again turns a hook back on and starts a fresh failure streak.
	_, err := s.db.ExecContext(ctx, `INSERT INTO hooks_out (topic, name, url, events, secret, body) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(topic, name) DO UPDATE SET url = excluded.url, events = excluded.events,
		secret = CASE WHEN ? AND excluded.secret = '' THEN hooks_out.secret ELSE excluded.secret END,
		body = CASE WHEN ? AND excluded.body = '' THEN hooks_out.body ELSE excluded.body END, enabled = 1, failing_since = NULL`,
		topic, name, h.URL, string(events), h.Secret, h.Body, keepSecret, keepBody)
	return err
}

// DeleteHook removes a hook and its delivery log.
func (s *Server) DeleteHook(ctx context.Context, tok *Token, topic, name string) error {
	if err := hookOwner(tok, topic, name); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM hooks_out WHERE topic = ? AND name = ?`, topic, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errf(404, "not_found", "no webhook %q on topic %q", name, topic)
	}
	if _, err := tx.Exec(`DELETE FROM hook_deliveries WHERE topic = ? AND name = ?`, topic, name); err != nil {
		return err
	}
	return tx.Commit()
}

// HookDelivery is one queued or sent delivery of a hook.
type HookDelivery struct {
	ID        int64      `json:"id"`
	Seq       int64      `json:"seq"`
	Event     string     `json:"event"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	NextAt    *time.Time `json:"next_at"` // the next try while pending
	Code      int        `json:"code"`
	Error     string     `json:"error"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// HookDeliveries lists a hook's newest deliveries first, at most limit.
func (s *Server) HookDeliveries(ctx context.Context, tok *Token, topic, name string, limit int) ([]HookDelivery, error) {
	if err := hookOwner(tok, topic, name); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, seq, event, status, attempts, next_at, code, error, updated_at
		FROM hook_deliveries WHERE topic = ? AND name = ? ORDER BY id DESC LIMIT ?`, topic, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HookDelivery{}
	for rows.Next() {
		var d HookDelivery
		var next, upd int64
		if err := rows.Scan(&d.ID, &d.Seq, &d.Event, &d.Status, &d.Attempts, &next, &d.Code, &d.Error, &upd); err != nil {
			return nil, err
		}
		if d.Status == "pending" {
			v := fromMS(next)
			d.NextAt = &v
		}
		d.UpdatedAt = fromMS(upd)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Server) hookOutRoutes() {
	s.api("GET /api/v1/topics/{topic}/hooks", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		topic := r.PathValue("topic")
		if err := validTopic(topic); err != nil {
			return err
		}
		hooks, err := s.OutHooks(r.Context(), tok, topic)
		if err != nil {
			return err
		}
		out := make([]OutHook, len(hooks))
		for i, h := range hooks {
			out[i] = h.OutHook
		}
		writeJSON(w, 200, map[string]any{"hooks": out})
		return nil
	})
	s.api("PUT /api/v1/topics/{topic}/hooks/{name}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		topic, name := r.PathValue("topic"), r.PathValue("name")
		if err := hookOwner(tok, topic, name); err != nil {
			return err // before reading the body, as it always was
		}
		var h OutHook
		if err := decodeJSON(r, &h); err != nil {
			return err
		}
		if err := s.SaveHook(r.Context(), tok, topic, name, h, false, false); err != nil {
			return err
		}
		if len(h.Events) == 0 {
			h.Events = hookEvents
		}
		h.Name, h.HasSecret, h.Secret, h.Enabled = name, h.Secret != "", "", true
		writeJSON(w, 200, h)
		return nil
	})
	s.api("DELETE /api/v1/topics/{topic}/hooks/{name}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := s.DeleteHook(r.Context(), tok, r.PathValue("topic"), r.PathValue("name")); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
		return nil
	})
	// preview: what a body template renders for an event, sent nowhere (#241).
	s.api("POST /api/v1/topics/{topic}/hooks/preview", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		topic := r.PathValue("topic")
		if err := hookOwner(tok, topic, ""); err != nil {
			return err
		}
		var in struct {
			Body  string          `json:"body"`
			Event json.RawMessage `json:"event"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.Body == "" {
			return errf(400, "bad_request", "body is required: the template to try")
		}
		ev := string(in.Event)
		if ev == "" {
			now := time.Now().UTC()
			b, _ := json.Marshal(Event{Seq: 1, Type: "state", Notification: &Notification{
				ID: "01JD3K8W2Q6V9X0YB4N5M7P1RS", Topic: topic, ExternalID: "issue-42", Title: "Disk full",
				Message: "Disk full on backup", Format: FormatText, Priority: 3, Tags: []string{}, Actions: []Action{},
				State: "resolved", StateBy: "pixel-9", CreatedAt: now, UpdatedAt: now, Seq: 1}})
			ev = string(b)
		}
		out, err := renderHookBody(in.Body, ev)
		if err != nil {
			return errf(400, "bad_request", "%v", err)
		}
		writeJSON(w, 200, map[string]string{"body": out}) // a string: the exact bytes sent
		return nil
	})
	s.api("GET /api/v1/topics/{topic}/hooks/{name}/deliveries", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		topic, name := r.PathValue("topic"), r.PathValue("name")
		if err := validTopic(topic); err != nil {
			return err
		}
		out, err := s.HookDeliveries(r.Context(), tok, topic, name, 100)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"deliveries": out})
		return nil
	})
}
