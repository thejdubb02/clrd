package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// ntfy compatibility (spec section 5): ntfy's publish and subscribe API on
// top of the same store and event log, so the ntfy app, CLI and every tool
// that already speaks ntfy work unchanged. A dismiss or resolve reaches ntfy
// subscribers as message_clear / message_delete for that sequence_id.

const ntfyMaxBody = 4096

type ntfyAction struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	Clear  bool   `json:"clear"`
}

type ntfyMsg struct {
	ID         string       `json:"id"`
	Time       int64        `json:"time"`
	Event      string       `json:"event"`
	Topic      string       `json:"topic"`
	SequenceID string       `json:"sequence_id,omitempty"`
	Title      string       `json:"title,omitempty"`
	Message    string       `json:"message,omitempty"`
	Priority   int          `json:"priority,omitempty"`
	Tags       []string     `json:"tags,omitempty"`
	Click      string       `json:"click,omitempty"`
	Actions    []ntfyAction `json:"actions,omitempty"`
}

// ntfyID maps an event seq to a ntfy message id and back ("s" + 11 digits,
// 12 chars like ntfy's own), so since=<id> works.
func ntfyID(seq int64) string { return fmt.Sprintf("s%011d", seq) }

// ntfyMessage renders a notification as of event seq. The ntfy event comes
// from the state: active shows, acknowledged/snoozed clear, dismissed/resolved delete.
func ntfyMessage(n *Notification, seq int64) ntfyMsg {
	m := ntfyMsg{ID: ntfyID(seq), Time: n.UpdatedAt.Unix(), Event: "message", Topic: n.Topic, SequenceID: n.ExternalID}
	if m.SequenceID == "" {
		m.SequenceID = n.ID
	}
	switch n.State {
	case Acknowledged, Snoozed:
		m.Event = "message_clear"
		return m
	case Dismissed, Resolved:
		m.Event = "message_delete"
		return m
	}
	m.Title, m.Message, m.Priority, m.Tags, m.Click = n.Title, n.Message, n.Priority, n.Tags, n.Click
	for i, a := range n.Actions {
		m.Actions = append(m.Actions, ntfyAction{ID: strconv.Itoa(i), Action: "view", Label: a.Label, URL: a.URL})
	}
	return m
}

func ntfyEvent(event, topic string) ntfyMsg {
	t := time.Now()
	return ntfyMsg{ID: fmt.Sprintf("k%011d", t.UnixNano()%1e11), Time: t.Unix(), Event: event, Topic: topic}
}

func (s *Server) ntfyRoutes() {
	for _, m := range []string{"PUT", "POST"} {
		s.route(m+" /{topic}", s.ntfyPublish)
		s.route(m+" /{topic}/{sid}", s.ntfyPublish) // publish or update by sequence id
		for _, a := range []string{"clear", "read"} {
			s.route(m+" /{topic}/{sid}/"+a, s.ntfyAct("ack"))
		}
	}
	s.route("DELETE /{topic}/{sid}", s.ntfyAct("resolve"))
	s.route("POST /{$}", s.ntfyPublishJSON)
	for _, p := range []string{"publish", "send", "trigger"} {
		s.route("GET /{topic}/"+p, s.ntfyPublish)
	}
	for _, f := range []string{"json", "sse", "raw", "ws"} {
		s.route("GET /{topics}/"+f, s.ntfySubscribe(f))
	}
	s.route("GET /{topics}/auth", func(w http.ResponseWriter, r *http.Request) error {
		tok := s.auth(r, false)
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		for _, t := range splitList(r.PathValue("topics")) {
			if err := tok.need(ScopeRead, t); err != nil {
				return err
			}
		}
		writeJSON(w, 200, map[string]bool{"success": true})
		return nil
	})
	s.route("GET /v1/health", func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, 200, map[string]bool{"healthy": s.db.PingContext(r.Context()) == nil})
		return nil
	})
}

// param reads a ntfy parameter from X-Name, Name, or the query (?name=),
// trying each alias in turn.
func param(r *http.Request, names ...string) string {
	for _, n := range names {
		for _, v := range []string{r.Header.Get("X-" + n), r.Header.Get(n), r.URL.Query().Get(strings.ToLower(n))} {
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func ntfyPriority(v string) (int, error) {
	switch strings.ToLower(v) {
	case "":
		return 0, nil
	case "min", "1":
		return 1, nil
	case "low", "2":
		return 2, nil
	case "default", "3":
		return 3, nil
	case "high", "4":
		return 4, nil
	case "max", "urgent", "5":
		return 5, nil
	}
	return 0, errf(400, "bad_request", "invalid priority %q", v)
}

// ntfyActions keeps view actions from ntfy's short form
// ("view, Open, https://x; http, ...") or its JSON array form.
// http and broadcast actions are dropped (not supported yet).
func ntfyActions(v string) ([]Action, error) {
	var out []Action
	if v = strings.TrimSpace(v); v == "" {
		return nil, nil
	}
	if strings.HasPrefix(v, "[") {
		var as []ntfyAction
		if err := json.Unmarshal([]byte(v), &as); err != nil {
			return nil, errf(400, "bad_request", "invalid actions: %v", err)
		}
		for _, a := range as {
			if a.Action == "view" {
				out = append(out, Action{Label: a.Label, URL: a.URL})
			}
		}
		return out, nil
	}
	for _, part := range strings.Split(v, ";") {
		f := strings.Split(part, ",")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		if len(f) >= 3 && f[0] == "view" {
			out = append(out, Action{Label: f[1], URL: f[2]})
		}
	}
	return out, nil
}

// ntfyDelay reads a unix time or a duration (30m, 2h, 1d).
func ntfyDelay(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		t := time.Unix(n, 0)
		return &t, nil
	}
	if d, err := parseDuration(v); err == nil {
		t := time.Now().Add(d)
		return &t, nil
	}
	return nil, errf(400, "bad_request", "invalid delay %q: use a unix time or a duration like 30m", v)
}

var errNoCache = errf(400, "bad_request", "Cache: no is not supported: clrd stores every notification so it can sync")

func parseDuration(v string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err == nil && (n < 0 || n > 36500) { // bounded so the multiply cannot overflow
			err = errors.New("days out of range")
		}
		return time.Duration(n) * 24 * time.Hour, err
	}
	return time.ParseDuration(v)
}

// ntfyPublish: PUT/POST /{topic}[/{sid}] with the body as the message, or
// GET /{topic}/publish with everything in the query. Never the cookie.
func (s *Server) ntfyPublish(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodHead { // GET routes also match HEAD; a probe must not publish
		return errf(405, "bad_request", "use GET, PUT or POST to publish")
	}
	if strings.EqualFold(param(r, "Cache"), "no") { // ntfy drops these after delivery; clrd keeps every notification
		return errNoCache
	}
	tok := s.auth(r, false)
	if t := r.URL.Query().Get("template"); t != "" && r.Method != http.MethodGet {
		return s.ntfyTemplate(w, r, tok, t)
	}
	in := PublishInput{Topic: r.PathValue("topic"), ExternalID: r.PathValue("sid")}
	if in.ExternalID == "" {
		in.ExternalID = param(r, "Id", "Sequence-Id", "Sid")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, ntfyMaxBody+1))
	if err != nil {
		return err
	}
	if len(body) > ntfyMaxBody {
		return errf(413, "too_large", "message longer than %d bytes (attachments are not supported)", ntfyMaxBody)
	}
	in.Message = strings.TrimSpace(string(body))
	if in.Message == "" {
		in.Message = param(r, "Message", "M")
	}
	in.Title = param(r, "Title", "Ti", "T")
	in.Tags = splitList(param(r, "Tags", "Tag", "Ta"))
	in.Click = param(r, "Click")
	if in.Priority, err = ntfyPriority(param(r, "Priority", "Prio", "P")); err != nil {
		return err
	}
	if in.Actions, err = ntfyActions(param(r, "Actions", "Action")); err != nil {
		return err
	}
	if in.SnoozeUntil, err = ntfyDelay(param(r, "Delay", "At", "In")); err != nil {
		return err
	}
	in.Status = param(r, "Status")
	return s.ntfyReply(w, r.Context(), tok, in)
}

func (s *Server) ntfyReply(w http.ResponseWriter, ctx context.Context, tok *Token, in PublishInput) error {
	n, err := s.Publish(ctx, tok, in)
	if err != nil {
		return err
	}
	writeJSON(w, 200, ntfyMessage(n, n.Seq))
	return nil
}

// ntfyPublishJSON: POST / with ntfy's JSON body.
func (s *Server) ntfyPublishJSON(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Topic      string       `json:"topic"`
		Message    string       `json:"message"`
		Title      string       `json:"title"`
		Priority   int          `json:"priority"`
		Tags       []string     `json:"tags"`
		Click      string       `json:"click"`
		Actions    []ntfyAction `json:"actions"`
		Delay      string       `json:"delay"`
		ID         string       `json:"id"`
		SequenceID string       `json:"sequence_id"`
		ExternalID string       `json:"external_id"`
		Status     string       `json:"status"`
		Cache      string       `json:"cache"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if strings.EqualFold(in.Cache, "no") || strings.EqualFold(param(r, "Cache"), "no") {
		return errNoCache
	}
	p := PublishInput{Topic: in.Topic, Message: in.Message, Title: in.Title, Priority: in.Priority, Tags: in.Tags,
		Click: in.Click, ExternalID: in.ExternalID, Status: in.Status}
	for _, id := range []string{in.SequenceID, in.ID} {
		if p.ExternalID == "" {
			p.ExternalID = id
		}
	}
	for _, a := range in.Actions {
		if a.Action == "view" {
			p.Actions = append(p.Actions, Action{Label: a.Label, URL: a.URL})
		}
	}
	var err error
	if p.SnoozeUntil, err = ntfyDelay(in.Delay); err != nil {
		return err
	}
	return s.ntfyReply(w, r.Context(), s.auth(r, false), p)
}

// ntfyAct: PUT /{topic}/{sid}/clear acks, DELETE /{topic}/{sid} resolves.
// sid is the external id, or the notification id when it had none.
func (s *Server) ntfyAct(action string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		tok, topic, sid := s.auth(r, false), r.PathValue("topic"), r.PathValue("sid")
		n, err := s.ActByKey(r.Context(), tok, topic, sid, action)
		if isNotFound(err) {
			n, err = s.act(r.Context(), tok, action, nil, false, `id = ? AND topic = ?`, sid, topic)
		}
		if err != nil {
			return err
		}
		writeJSON(w, 200, ntfyMessage(n, n.Seq))
		return nil
	}
}

// ntfyTemplate turns an Alertmanager or Grafana webhook into one
// notification per alert, keyed by fingerprint, so a resolved alert
// resolves its notification instead of adding another.
func (s *Server) ntfyTemplate(w http.ResponseWriter, r *http.Request, tok *Token, name string) error {
	if name != "alertmanager" && name != "grafana" {
		return errf(400, "bad_request", "unknown template %q (want alertmanager or grafana)", name)
	}
	var in struct {
		Alerts []struct {
			Status       string            `json:"status"`
			Fingerprint  string            `json:"fingerprint"`
			Labels       map[string]string `json:"labels"`
			Annotations  map[string]string `json:"annotations"`
			GeneratorURL string            `json:"generatorURL"`
		} `json:"alerts"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	prio, err := ntfyPriority(param(r, "Priority", "Prio", "P"))
	if err != nil {
		return err
	}
	var created, resolved int
	for _, a := range in.Alerts {
		p := PublishInput{Topic: r.PathValue("topic"), ExternalID: a.Fingerprint, Status: a.Status, Priority: prio,
			Title: a.Labels["alertname"], Message: a.Annotations["summary"], Click: a.GeneratorURL,
			Tags: splitList(param(r, "Tags", "Tag", "Ta"))}
		if p.Message == "" {
			p.Message = a.Annotations["description"]
		}
		_, err := s.Publish(r.Context(), tok, p)
		switch {
		case a.Status == "resolved" && isNotFound(err): // already cleared, nothing to do
		case err != nil:
			return err
		case a.Status == "resolved":
			resolved++
		default:
			created++
		}
	}
	writeJSON(w, 200, map[string]int{"published": created, "resolved": resolved})
	return nil
}

var errStop = errors.New("poll done")

// ntfySubscribe: GET /{t1,t2}/json|sse|raw|ws with since= and poll=1.
func (s *Server) ntfySubscribe(format string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		tok := s.auth(r, false)
		topics := splitList(r.PathValue("topics"))
		for _, t := range topics {
			if err := validTopic(t); err != nil {
				return err
			}
			if err := tok.need(ScopeRead, t); err != nil {
				return err
			}
		}
		q := r.URL.Query()
		poll := q.Get("poll") == "1" || q.Get("poll") == "true"
		sinceArg := q.Get("since")
		if sinceArg == "" && poll {
			sinceArg = "all"
		}
		since, err := s.ntfySince(r.Context(), sinceArg, topics)
		if err != nil {
			return err
		}
		since = max(since, s.pruned.Load()) // "all" means all that is left, not a reset

		var write func(m ntfyMsg) error
		ctx := r.Context()
		if format == "ws" {
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return nil
			}
			defer c.CloseNow()
			ctx = c.CloseRead(ctx)
			write = func(m ntfyMsg) error {
				b, _ := json.Marshal(m)
				wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				return c.Write(wctx, websocket.MessageText, b)
			}
		} else {
			rc := http.NewResponseController(w)
			w.Header().Set("Content-Type", map[string]string{"json": "application/x-ndjson; charset=utf-8",
				"sse": "text/event-stream; charset=utf-8", "raw": "text/plain; charset=utf-8"}[format])
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			write = func(m ntfyMsg) error {
				b, _ := json.Marshal(m)
				switch {
				case format == "json":
					b = append(b, '\n')
				case format == "sse" && m.Event == "message":
					b = fmt.Appendf(nil, "data: %s\n\n", b)
				case format == "sse":
					b = fmt.Appendf(nil, "event: %s\ndata: %s\n\n", m.Event, b)
				case m.Event == "message": // raw: one line per message
					b = []byte(strings.ReplaceAll(m.Message, "\n", " ") + "\n")
				default:
					b = []byte("\n")
				}
				if _, err := w.Write(b); err != nil {
					return err
				}
				return rc.Flush()
			}
		}
		joined := strings.Join(topics, ",")
		if !poll {
			if err := write(ntfyEvent("open", joined)); err != nil {
				return nil
			}
		}
		s.follow(ctx, tok, since, topics, func(raw []byte, seq int64) error {
			if bytes.HasPrefix(raw, []byte(`{"type":"ready"`)) {
				if poll {
					return errStop
				}
				return nil
			}
			if seq == 0 { // reset: ntfy has no such event
				return nil
			}
			var e Event
			if err := json.Unmarshal(raw, &e); err != nil {
				return err
			}
			return write(ntfyMessage(e.Notification, seq))
		}, func() error { return write(ntfyEvent("keepalive", joined)) })
		return nil
	}
}

// ntfySince maps ntfy's since= to an event seq to replay after: "" or
// "none" = now, "all", "latest", a message id, a unix time or a duration.
func (s *Server) ntfySince(ctx context.Context, v string, topics []string) (int64, error) {
	cur := s.CurrentSeq()
	firstAfter := func(t time.Time) (int64, error) {
		var seq *int64
		err := s.db.QueryRowContext(ctx, `SELECT MIN(seq) FROM events WHERE created_at >= ?`, ms(t)).Scan(&seq)
		if err != nil || seq == nil {
			return cur, err
		}
		return *seq - 1, nil
	}
	switch v {
	case "", "none":
		return cur, nil
	case "all":
		return 0, nil
	case "latest":
		var seq *int64
		args := make([]any, len(topics))
		for i, t := range topics {
			args[i] = t
		}
		err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM events WHERE topic IN (`+placeholders(len(topics))+`)`, args...).Scan(&seq)
		if err != nil || seq == nil {
			return cur, err
		}
		return *seq - 1, nil
	}
	if id, ok := strings.CutPrefix(v, "s"); ok && len(v) == 12 {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			return min(n, cur), nil
		}
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return firstAfter(time.Unix(n, 0))
	}
	if d, err := parseDuration(v); err == nil {
		return firstAfter(time.Now().Add(-d))
	}
	if len(v) == 12 { // a message id from another server (or ntfy itself): send everything
		return 0, nil
	}
	return 0, errf(400, "bad_request", "invalid since %q", v)
}
