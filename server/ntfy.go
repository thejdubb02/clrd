package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
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
	Icon       string       `json:"icon,omitempty"`
	Actions    []ntfyAction `json:"actions,omitempty"`
	Attachment *ntfyAttach  `json:"attachment,omitempty"`

	// ContentType is ntfy's Markdown flag: "text/markdown", or empty for plain text.
	ContentType string `json:"content_type,omitempty"`
}

type ntfyAttach struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Size int64  `json:"size,omitempty"`
	URL  string `json:"url"`
}

// ntfyID maps an event seq to a ntfy message id and back ("s" + 11 digits,
// 12 chars like ntfy's own), so since=<id> works.
func ntfyID(seq int64) string { return fmt.Sprintf("s%011d", seq) }

// ntfyMessage renders a notification as of event seq. The ntfy event comes
// from the state: active shows, acknowledged/snoozed clear, dismissed/resolved delete.
func (s *Server) ntfyMessage(n *Notification, seq int64) ntfyMsg {
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
	m.Title, m.Message, m.Priority, m.Tags, m.Click, m.Icon = n.Title, n.Message, n.Priority, n.Tags, n.Click, n.Icon
	if n.Format != FormatText {
		m.ContentType = "text/markdown"
	}
	if a := n.Attachment; a != nil {
		m.Attachment = &ntfyAttach{Name: a.Name, Type: a.Type, Size: a.Size, URL: a.URL}
		if strings.HasPrefix(a.URL, "/") { // ours: ntfy clients want it absolute
			m.Attachment.URL = s.opt.BaseURL + a.URL
		}
	}
	for i, a := range n.Actions {
		if a.URL != "" { // ntfy has no reply button; an http action would need our token
			m.Actions = append(m.Actions, ntfyAction{ID: strconv.Itoa(i), Action: "view", Label: a.Label, URL: a.URL})
		}
	}
	return m
}

func ntfyEvent(event, topic string) ntfyMsg {
	t := time.Now()
	return ntfyMsg{ID: fmt.Sprintf("k%011d", t.UnixNano()%1e11), Time: t.Unix(), Event: event, Topic: topic}
}

func (s *Server) ntfyRoutes() {
	// ntfy answers any origin, so web pages elsewhere can publish and
	// subscribe with a token. No cookies are read on these routes, so this
	// grants nothing a token holder could not already do.
	route := func(pattern string, h func(http.ResponseWriter, *http.Request) error) {
		s.route(pattern, func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			return h(w, r)
		})
	}
	for _, m := range []string{"PUT", "POST"} {
		route(m+" /{topic}", s.ntfyPublish)
		route(m+" /{topic}/{sid}", s.ntfyPublish) // publish or update by sequence id
		for _, a := range []string{"clear", "read"} {
			route(m+" /{topic}/{sid}/"+a, s.ntfyAct("ack"))
		}
	}
	route("DELETE /{topic}/{sid}", s.ntfyAct("resolve"))
	route("POST /{$}", s.ntfyPublishJSON)
	for _, p := range []string{"publish", "send", "trigger"} {
		route("GET /{topic}/"+p, s.ntfyPublish)
	}
	for _, f := range []string{"json", "sse", "raw", "ws"} {
		route("GET /{topics}/"+f, s.ntfySubscribe(f))
	}
	route("GET /{topics}/auth", func(w http.ResponseWriter, r *http.Request) error {
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
	route("GET /v1/health", func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, 200, map[string]bool{"healthy": s.db.PingContext(r.Context()) == nil})
		return nil
	})
}

// param reads a ntfy parameter from X-Name, Name, or the query (?name=),
// trying each alias in turn.
func param(r *http.Request, names ...string) string {
	for _, n := range names {
		h := r.Header.Get(n)
		if n == "Priority" && (strings.Contains(h, "u=") || h == "i") {
			h = "" // browsers send RFC 9218 "Priority: u=3, i" on HTTP/2 and 3; not ours
		}
		for _, v := range []string{r.Header.Get("X-" + n), h, r.URL.Query().Get(strings.ToLower(n))} {
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

// ntfyDelay reads a unix time or a duration (30m, 2h, 1d), at most 3 days
// ahead like ntfy's default limit.
func ntfyDelay(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	var t time.Time
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		t = time.Unix(n, 0)
	} else if d, err := parseDuration(v); err == nil {
		t = time.Now().Add(d)
	}
	if !t.IsZero() {
		if t.After(time.Now().Add(72 * time.Hour)) {
			return nil, errf(400, "bad_request", "delay %q is more than 3 days ahead", v)
		}
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
		return s.webhookIn(w, r, tok, t) // webhooks.go
	}
	in := PublishInput{Topic: r.PathValue("topic"), ExternalID: r.PathValue("sid")}
	if in.ExternalID == "" {
		in.ExternalID = param(r, "Id", "Sequence-Id", "Sid")
	}
	file := ntfyFilename(r)
	if r.Method == http.MethodGet {
		file = "" // GET /{topic}/publish has no body to be the file
	}
	if file == "" {
		body, err := io.ReadAll(io.LimitReader(r.Body, ntfyMaxBody+1))
		if err != nil {
			return err
		}
		if len(body) > ntfyMaxBody {
			return errf(413, "too_large", "message longer than %d bytes (send a file with X-Filename)", ntfyMaxBody)
		}
		in.Message = strings.TrimSpace(string(body))
	}
	if in.Message == "" {
		in.Message = param(r, "Message", "M")
	}
	if in.Message == "" && file != "" {
		in.Message = "You received a file: " + cleanName(file)
	}
	if a := ntfyIcon(param(r, "Attach", "A")); a != "" { // same rule as icon: a bad URL is dropped
		in.Attachment = &Attachment{URL: a}
	}
	var err error
	in.Title = param(r, "Title", "Ti", "T")
	in.Tags = splitList(param(r, "Tags", "Tag", "Ta"))
	in.Click = param(r, "Click")
	in.Icon = ntfyIcon(param(r, "Icon"))
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
	in.Markdown = ntfyMarkdown(param(r, "Markdown", "Md"))
	if file != "" {
		n, err := s.PublishFile(r.Context(), tok, in, file, r.Body)
		if err != nil {
			return err
		}
		writeJSON(w, 200, s.ntfyMessage(n, n.Seq))
		return nil
	}
	return s.ntfyReply(w, r.Context(), tok, in)
}

// ntfyFilename is the file name that makes a ntfy publish body a file.
func ntfyFilename(r *http.Request) string { return param(r, "Filename", "File", "F") }

func (s *Server) ntfyReply(w http.ResponseWriter, ctx context.Context, tok *Token, in PublishInput) error {
	n, err := s.Publish(ctx, tok, in)
	if err != nil {
		return err
	}
	writeJSON(w, 200, s.ntfyMessage(n, n.Seq))
	return nil
}

// ntfyPublishJSON: POST / with ntfy's JSON body.
func (s *Server) ntfyPublishJSON(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Topic      string          `json:"topic"`
		Message    string          `json:"message"`
		Title      string          `json:"title"`
		Priority   int             `json:"priority"`
		Tags       []string        `json:"tags"`
		Click      string          `json:"click"`
		Icon       json.RawMessage `json:"icon"` // raw: ntfy drops a bad icon rather than failing the publish
		Attach     json.RawMessage `json:"attach"`
		Filename   string          `json:"filename"`
		Actions    []ntfyAction    `json:"actions"`
		Delay      string          `json:"delay"`
		ID         string          `json:"id"`
		SequenceID string          `json:"sequence_id"`
		ExternalID string          `json:"external_id"`
		Status     string          `json:"status"`
		Cache      string          `json:"cache"`
		Markdown   *bool           `json:"markdown"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if strings.EqualFold(in.Cache, "no") || strings.EqualFold(param(r, "Cache"), "no") {
		return errNoCache
	}
	p := PublishInput{Topic: in.Topic, Message: in.Message, Title: in.Title, Priority: in.Priority, Tags: in.Tags,
		Click: in.Click, ExternalID: in.ExternalID, Status: in.Status, Markdown: in.Markdown}
	var icon string
	_ = json.Unmarshal(in.Icon, &icon)
	p.Icon = ntfyIcon(icon)
	var attach string
	_ = json.Unmarshal(in.Attach, &attach)
	if attach = ntfyIcon(attach); attach != "" {
		p.Attachment = &Attachment{URL: attach, Name: in.Filename}
	}
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
		writeJSON(w, 200, s.ntfyMessage(n, n.Seq))
		return nil
	}
}

var (
	errStop   = errors.New("poll done")
	errPruned = errors.New("since was pruned")
)

// ntfySubscribe: GET /{t1,t2}/json|sse|raw|ws with since= and poll=1.
func (s *Server) ntfySubscribe(format string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		tok := s.auth(r, false)
		topics := splitList(r.PathValue("topics"))
		if len(topics) == 0 {
			return errf(400, "bad_request", "no topic given")
		}
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
		f, err := ntfyFilters(r)
		if err != nil {
			return err
		}

		var write func(m ntfyMsg) error
		ctx := r.Context()
		if format == "ws" {
			// Any origin, like the other ntfy routes: auth here is a token, never a cookie.
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
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
			defer boundEnd(rc)
			w.Header().Set("Content-Type", map[string]string{"json": "application/x-ndjson; charset=utf-8",
				"sse": "text/event-stream; charset=utf-8", "raw": "text/plain; charset=utf-8"}[format])
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			write = func(m ntfyMsg) error {
				b, _ := json.Marshal(m)
				switch {
				case format == "json":
					b = append(b, '\n')
				case format == "sse" && strings.HasPrefix(m.Event, "message"): // ntfy sends message, message_clear and message_delete without an event line
					b = fmt.Appendf(nil, "data: %s\n\n", b)
				case format == "sse":
					b = fmt.Appendf(nil, "event: %s\ndata: %s\n\n", m.Event, b)
				case m.Event == "message": // raw: one line per message
					b = []byte(strings.ReplaceAll(m.Message, "\n", " ") + "\n")
				default:
					b = []byte("\n")
				}
				return flushWrite(rc, w, b)
			}
		}
		joined := strings.Join(topics, ",")
		if !poll {
			if err := write(ntfyEvent("open", joined)); err != nil {
				return nil
			}
		}
		// ntfy has no reset: since= means all that is left after it, so a
		// prune (before or during replay) restarts from the oldest event left.
		for tries := 0; ; tries++ {
			err := s.follow(ctx, tok, since, topics, func(raw []byte, seq int64) error {
				if bytes.HasPrefix(raw, []byte(`{"type":"ready"`)) {
					if poll {
						return errStop
					}
					return nil
				}
				if seq < 0 {
					if tries < 3 {
						return errPruned
					}
					return nil
				}
				since = seq
				var e Event
				if err := json.Unmarshal(raw, &e); err != nil {
					return err
				}
				if !f.pass(e.Notification, ntfyID(seq)) {
					return nil
				}
				return write(s.ntfyMessage(e.Notification, seq))
			}, func() error { return write(ntfyEvent("keepalive", joined)) })
			if err != errPruned {
				return nil
			}
			var oldest int64
			if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(seq) - 1, ?) FROM events`, s.CurrentSeq()).Scan(&oldest); err != nil {
				return nil
			}
			since = max(since, oldest)
		}
	}
}

// ntfyFilter is ntfy's subscribe filters: id, message and title match
// exactly, priority is any of a list, tags must all be there. Clear and
// delete events are matched on the notification they clear.
type ntfyFilter struct {
	id, message, title string
	prio               []int
	tags               []string
}

func ntfyFilters(r *http.Request) (f ntfyFilter, err error) {
	f.id, f.message, f.title = param(r, "Id"), param(r, "Message", "M"), param(r, "Title", "T")
	f.tags = splitList(param(r, "Tags", "Tag", "Ta"))
	for _, v := range splitList(param(r, "Priority", "Prio", "P")) {
		p, err := ntfyPriority(v)
		if err != nil {
			return f, err
		}
		f.prio = append(f.prio, p)
	}
	return f, nil
}

func (f ntfyFilter) pass(n *Notification, id string) bool {
	if f.id != "" && f.id != id || f.message != "" && f.message != n.Message || f.title != "" && f.title != n.Title {
		return false
	}
	if len(f.prio) > 0 && !slices.Contains(f.prio, n.Priority) {
		return false
	}
	for _, t := range f.tags {
		if !slices.Contains(n.Tags, t) {
			return false
		}
	}
	return true
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

// ntfyIcon keeps an http(s) icon URL and drops anything else, never a 400,
// so a publish ntfy accepts still succeeds.
func ntfyIcon(v string) string {
	if checkImageURL("icon", v) != nil {
		return ""
	}
	return v
}
