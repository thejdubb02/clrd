package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// ntfy compatibility (spec section 5): ntfy's publish and subscribe API on
// top of the same store and event log, so the ntfy app, CLI and every tool
// that already speaks ntfy work unchanged. A dismiss or resolve reaches ntfy
// subscribers as message_clear / message_delete for that sequence_id.

const ntfyMaxBody = 4096

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

// ntfyMsgID is n's ntfy id as of seq: a live message keeps its own (live.go).
func ntfyMsgID(n *Notification, seq int64) string {
	if n.Live {
		return n.ID
	}
	return ntfyID(seq)
}

// ntfyMessage renders a notification as of event seq. The ntfy event comes
// from the state: active shows, acknowledged/snoozed clear, dismissed/resolved delete.
// With sched (ntfy's ?scheduled=1, and the reply to a delayed publish), a
// notification snoozed until later shows as a message at that time, as ntfy
// lists a delayed message.
func (s *Server) ntfyMessage(n *Notification, seq int64, sched bool) ntfyMsg {
	m := ntfyMsg{ID: ntfyMsgID(n, seq), Time: n.UpdatedAt.Unix(), Event: "message", Topic: n.Topic, SequenceID: n.ExternalID}
	if m.SequenceID == "" {
		m.SequenceID = n.ID
	}
	later := sched && n.State == Snoozed && n.SnoozeUntil != nil && n.SnoozeUntil.After(now())
	if later {
		m.Time = n.SnoozeUntil.Unix()
	}
	switch {
	case later:
	case n.State == Acknowledged || n.State == Snoozed:
		m.Event = "message_clear"
		return m
	case n.State == Dismissed || n.State == Resolved:
		m.Event = "message_delete"
		return m
	}
	m.Title, m.Message = ntfyText(n)
	m.Priority, m.Tags, m.Click, m.Icon = n.Priority, n.Tags, n.Click, n.Icon
	if n.Silenced { // ntfy apps have no "silenced": min priority shows it without a sound
		m.Priority = 1
	}
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
		if na, ok := ntfyActionOut(a); ok { // ntfy has no reply button
			na.ID = strconv.Itoa(i)
			m.Actions = append(m.Actions, na)
		}
	}
	return m
}

func ntfyEvent(event, topic string) ntfyMsg {
	t := time.Now()
	return ntfyMsg{ID: fmt.Sprintf("k%011d", t.UnixNano()%1e11), Time: t.Unix(), Event: event, Topic: topic}
}

func (s *Server) ntfyRoutes() {
	// ntfy answers any origin; clrd answers only the sites an admin allowed
	// (ntfyorigins.go), so a page elsewhere cannot use anonymous access or
	// a server reachable only from its visitor's network.
	route := func(pattern string, h func(http.ResponseWriter, *http.Request) error) {
		s.route(pattern, func(w http.ResponseWriter, r *http.Request) error {
			if err := s.ntfyCORS(w, r); err != nil {
				return err
			}
			if _, _, ok := ntfyPassword(r); ok { // taken before bcrypt, kept only if the password was wrong
				ip := clientIP(r, s.opt.TrustedProxies)
				if wait := s.pwLimit.take(ip, time.Now()); wait > 0 {
					return retryAfter(wait, "too many wrong passwords from this address, try again later")
				}
				var once sync.Once
				settle := func(wrong bool) { // a right or unchecked password gets its token back
					once.Do(func() {
						if !wrong {
							s.pwLimit.give(ip)
						}
					})
				}
				defer settle(false)
				r = r.WithContext(context.WithValue(r.Context(), passwordSettle{}, settle))
			}
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
	route("POST /message", s.gotifyMessage) // Gotify's publish, else ntfy's topic "message" (gotify.go)
	for _, p := range []string{"publish", "send", "trigger"} {
		route("GET /{topic}/"+p, s.ntfyPublish)
	}
	for _, f := range []string{"json", "sse", "raw", "ws"} {
		route("GET /{topics}/"+f, s.ntfySubscribe(f))
	}
	route("GET /{topics}/auth", func(w http.ResponseWriter, r *http.Request) error {
		tok := s.ntfyAuth(r)
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
	// ntfy's app and CLI call this to check a login; scripts use it to
	// check a token still works. Only the fields they read.
	route("GET /v1/account", func(w http.ResponseWriter, r *http.Request) error {
		tok := s.ntfyAuth(r)
		if tok == nil && authHeader(r) != "" {
			return tok.need(ScopeRead, "")
		}
		acct := map[string]string{"username": "*", "role": "anonymous"}
		if tok != nil && !isAnonymous(tok) {
			acct = map[string]string{"username": tok.Name, "role": "user"}
			if tok.Has(ScopeAdmin) {
				acct["role"] = "admin"
			}
		}
		writeJSON(w, 200, acct)
		return nil
	})
	route("GET /v1/health", func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, 200, map[string]bool{"healthy": s.db.PingContext(r.Context()) == nil})
		return nil
	})
}

// param reads a ntfy parameter from X-Name or Name for every alias first,
// then from the query (?name=), the same order as ntfy's readParam. A header
// may carry RFC 2047 encoded words (=?UTF-8?B?...?=), since headers hold only
// ASCII; they are decoded as ntfy does, and one that cannot be is kept as sent.
func param(r *http.Request, names ...string) string {
	for _, n := range names {
		h := r.Header.Get(n)
		if n == "Priority" && (strings.Contains(h, "u=") || h == "i") {
			h = "" // browsers send RFC 9218 "Priority: u=3, i" on HTTP/2 and 3; not ours
		}
		for _, v := range []string{r.Header.Get("X-" + n), h} {
			if v != "" {
				if d, err := new(mime.WordDecoder).DecodeHeader(v); err == nil {
					return d
				}
				return v
			}
		}
	}
	for _, n := range names {
		if v := r.URL.Query().Get(strings.ToLower(n)); v != "" {
			return v
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

// ntfyDelay reads when a delayed message fires, in the forms ntfy documents:
// a unix time, a duration (30m, 2h, 1d, 2 days), an RFC 3339 time, or a time
// of day (10am, 8:30pm, tomorrow 3pm, Tuesday, 7am). tz is an IANA zone name
// for the time of day, else the server's own zone. At most 3 days ahead, like
// ntfy's default limit. A timezone with no delay is ignored, as on ntfy.
func ntfyDelay(v, tz string) (*time.Time, error) {
	if v = strings.TrimSpace(v); v == "" {
		return nil, nil
	}
	loc := time.Local
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return nil, errf(400, "bad_request", "invalid timezone %q: use an IANA name like America/New_York", tz)
		}
		loc = l
	}
	now := time.Now().In(loc)
	t, err := delayTime(v, now)
	if err != nil {
		return nil, err
	}
	if t.After(now.Add(72 * time.Hour)) {
		return nil, errf(400, "bad_request", "delay %q is more than 3 days ahead", v)
	}
	return &t, nil
}

var spokenDuration = regexp.MustCompile(`^(\d+) ?([a-z]+)$`)

const errDelay = "invalid delay %q: use a unix time, a duration like 30m, an RFC 3339 time or a time like tomorrow 10am"

// delayTime is ntfyDelay's parser, with the clock (and its zone) passed in.
func delayTime(v string, now time.Time) (time.Time, error) {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	if d, err := parseDuration(v); err == nil {
		return now.Add(d), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		if !t.After(now) {
			return t, errf(400, "bad_request", "delay %q is in the past", v)
		}
		return t, nil
	}
	f := strings.Fields(strings.NewReplacer(",", " ", " am", "am", " pm", "pm").Replace(strings.ToLower(v)))
	if m := spokenDuration.FindStringSubmatch(strings.Join(f, " ")); m != nil { // "2 days", "30min"
		if n, err := strconv.Atoi(m[1]); err == nil && n <= 36500 {
			for _, u := range []struct {
				names string
				d     time.Duration
			}{{" d day days ", 24 * time.Hour}, {" h hr hrs hour hours ", time.Hour}, {" m min mins minute minutes ", time.Minute}, {" s sec secs second seconds ", time.Second}} {
				if strings.Contains(u.names, " "+m[2]+" ") {
					return now.Add(time.Duration(n) * u.d), nil
				}
			}
		}
	}
	// A day word and a time of day, each at most once, in either order.
	day, clock := -1, -1 // days ahead, minutes after midnight
	weekday := false
	for _, w := range f {
		c := -1
		switch w {
		case "noon":
			c = 12 * 60
		case "midnight":
			c = 0
		default:
			for _, layout := range []string{"3pm", "3:04pm", "15:04"} {
				if t, err := time.Parse(layout, w); err == nil {
					c = t.Hour()*60 + t.Minute()
					break
				}
			}
		}
		d := -1
		if w == "today" {
			d = 0
		} else if w == "tomorrow" {
			d = 1
		} else {
			for i := range 7 {
				if name := strings.ToLower(time.Weekday(i).String()); w == name || w == name[:3] {
					d, weekday = (i-int(now.Weekday())+7)%7, true
				}
			}
		}
		switch {
		case c >= 0 && clock < 0:
			clock = c
		case d >= 0 && day < 0:
			day = d
		default:
			return now, errf(400, "bad_request", errDelay, v)
		}
	}
	if clock < 0 {
		if day < 0 {
			return now, errf(400, "bad_request", errDelay, v)
		}
		clock = now.Hour()*60 + now.Minute()
	}
	at := func(d int) time.Time {
		return time.Date(now.Year(), now.Month(), now.Day()+d, clock/60, clock%60, 0, 0, now.Location())
	}
	t := at(max(day, 0))
	if !t.After(now) {
		switch {
		case day < 0: // a bare time already gone today means tomorrow, as on ntfy
			t = at(1)
		case weekday:
			t = at(day + 7)
		default:
			return t, errf(400, "bad_request", "delay %q is in the past", v)
		}
	}
	return t, nil
}

var errNoCache = errf(400, "bad_request", "Cache: no is not supported with a webhook template")

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

// ntfyImplicitUpload reports publishes whose bodies may be detected as files.
func (s *Server) ntfyImplicitUpload(r *http.Request) bool {
	if !s.opt.NtfyImplicitUploads || (r.Method != http.MethodPost && r.Method != http.MethodPut) ||
		ntfyFilename(r) != "" || param(r, "Template", "Tpl") != "" || r.URL.Query().Get("template") != "" ||
		ntfyIcon(param(r, "Attach", "A")) != "" {
		return false
	}
	_, pattern := s.mux.Handler(r)
	switch pattern {
	case "PUT /{topic}", "PUT /{topic}/{sid}", "POST /{topic}", "POST /{topic}/{sid}":
		return true
	case "POST /message":
		return !hasGotifyKey(r)
	}
	return false
}

// ntfyPublish: PUT/POST /{topic}[/{sid}] with the body as the message, or
// GET /{topic}/publish with everything in the query. Never the cookie.
func (s *Server) ntfyPublish(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodHead { // GET routes also match HEAD; a probe must not publish
		return errf(405, "bad_request", "use GET, PUT or POST to publish")
	}
	tok := s.ntfyAuth(r)
	s.noteIgnored(r)
	live := strings.EqualFold(param(r, "Cache"), "no") // sent to open streams, stored nowhere (live.go)
	file := ntfyFilename(r)
	if r.Method == http.MethodGet {
		file = "" // GET /{topic}/publish has no body to be the file
	}
	inline := ntfyMarkdown(param(r, "Template", "Tpl")) // ntfy's yes/true/1 (#272); any other ?template= is a named one
	yes := inline != nil && *inline
	tpl := yes && file == "" && r.Method != http.MethodGet
	if t := r.URL.Query().Get("template"); t != "" && !yes && r.Method != http.MethodGet {
		if live {
			return errNoCache
		}
		return s.webhookIn(w, r, tok, t) // webhooks.go
	}
	in := PublishInput{Topic: r.PathValue("topic"), ExternalID: r.PathValue("sid"), Live: live, dropDevice: true}
	if in.ExternalID == "" {
		in.ExternalID = param(r, "Id", "Sequence-Id", "Sid")
	}
	title, prio := param(r, "Title", "Ti", "T"), param(r, "Priority", "Prio", "P")
	payload := io.Reader(r.Body)
	if file == "" {
		body, err := io.ReadAll(io.LimitReader(r.Body, ntfyMaxBody+1))
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return errf(408, "timeout", "request body too slow")
		}
		if err != nil {
			return errf(400, "bad_request", "cannot read request body")
		}
		if candidate, ok := r.Body.(*implicitUploadBody); ok && (len(body) > ntfyMaxBody || !utf8.Valid(body)) {
			file = "attachment"
			payload = io.MultiReader(bytes.NewReader(body), r.Body)
			http.NewResponseController(w).SetReadDeadline(candidate.deadline)
		} else {
			if len(body) > ntfyMaxBody {
				return errf(413, "too_large", "message longer than %d bytes (send a file with X-Filename)", ntfyMaxBody)
			}
			in.Message = strings.TrimSpace(string(body))
		}
		if tpl {
			msg := param(r, "Message", "M")
			if err := ntfyTemplate(r, body, &msg, &title, &prio); err != nil {
				return err
			}
			if param(r, "Message", "M") != "" {
				in.Message = msg
			}
		}
	}
	if in.Message == "" && !tpl {
		in.Message = param(r, "Message", "M")
	}
	if in.Message == "" && file != "" {
		in.Message = "You received a file: " + cleanName(file)
	}
	if a := ntfyIcon(param(r, "Attach", "A")); a != "" { // same rule as icon: a bad URL is dropped
		in.Attachment = &Attachment{URL: a}
	}
	var err error
	in.Title = title
	in.Tags = splitList(param(r, "Tags", "Tag", "Ta"))
	in.Click = param(r, "Click")
	in.Icon = ntfyIcon(param(r, "Icon"))
	if in.Priority, err = ntfyPriority(prio); err != nil {
		return err
	}
	if in.Actions, err = ntfyActions(param(r, "Actions", "Action")); err != nil {
		return err
	}
	if in.SnoozeUntil, err = ntfyDelay(param(r, "Delay", "At", "In"), param(r, "Timezone")); err != nil {
		return err
	}
	in.Status = param(r, "Status")
	in.Markdown = ntfyMarkdown(param(r, "Markdown", "Md"))
	if file != "" {
		n, err := s.PublishFile(r.Context(), tok, in, file, payload)
		if err != nil {
			return err
		}
		s.ntfyPublished(w, n, in)
		return nil
	}
	return s.ntfyReply(w, r.Context(), tok, in)
}

// ntfyTemplate renders ntfy's inline templates (?tpl=yes): the message,
// title and priority are Go templates over the JSON body, with the same
// functions and limits as a saved webhook template. An empty one stays empty.
func ntfyTemplate(r *http.Request, body []byte, msg, title, prio *string) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var data any
	if err := dec.Decode(&data); err != nil {
		return errf(400, "bad_request", "template: the body must be JSON: %v", err)
	}
	h := Webhook{Message: *msg, Title: *title, Priority: *prio}
	t, err := h.parse(r.Header)
	if err != nil {
		return err
	}
	for f, p := range map[string]*string{"message": msg, "title": title, "priority": prio} {
		if *p, err = render(t, f, data); err != nil {
			return err
		}
	}
	return nil
}

// ntfyFilename is the file name that makes a ntfy publish body a file.
func ntfyFilename(r *http.Request) string { return param(r, "Filename", "File", "F") }

func (s *Server) ntfyReply(w http.ResponseWriter, ctx context.Context, tok *Token, in PublishInput) error {
	n, err := s.Publish(ctx, tok, in)
	if err != nil {
		return err
	}
	s.ntfyPublished(w, n, in)
	return nil
}

// ntfyPublished replies to a publish. A delayed one is the message at its
// delivery time, as ntfy replies; the delay is the sender's own, so a
// publish-only token (which never sees the state) gets it too.
func (s *Server) ntfyPublished(w http.ResponseWriter, n *Notification, in PublishInput) {
	if d := in.SnoozeUntil; d != nil && (n.State == Active || n.State == Snoozed) {
		c := *n
		c.State, c.SnoozeUntil = Snoozed, d
		n = &c
	}
	writeJSON(w, 200, s.ntfyMessage(n, n.Seq, true))
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
		Timezone   string          `json:"timezone"`
		ID         string          `json:"id"`
		SequenceID string          `json:"sequence_id"`
		ExternalID string          `json:"external_id"`
		Status     string          `json:"status"`
		Cache      string          `json:"cache"`
		Markdown   *bool           `json:"markdown"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return errf(400, "bad_request", "bad JSON body: %v", err)
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return errf(400, "bad_request", "bad JSON body: %v", err)
	}
	var keys map[string]json.RawMessage
	json.Unmarshal(body, &keys)
	var unknown []string
	for k := range keys {
		if !ntfyJSONKeys[k] {
			unknown = append(unknown, k)
		}
	}
	s.noteIgnored(r, unknown...)
	p := PublishInput{Topic: in.Topic, Message: in.Message, Title: in.Title, Priority: in.Priority, Tags: in.Tags,
		Click: in.Click, ExternalID: in.ExternalID, Status: in.Status, Markdown: in.Markdown,
		Live: strings.EqualFold(in.Cache, "no") || strings.EqualFold(param(r, "Cache"), "no"), dropDevice: true}
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
		if pa, ok := a.action(); ok {
			p.Actions = append(p.Actions, pa)
		}
	}
	if p.SnoozeUntil, err = ntfyDelay(in.Delay, cmp.Or(in.Timezone, param(r, "Timezone"))); err != nil {
		return err
	}
	// Like ntfy, headers and query params fill what the JSON leaves out
	// (Apprise sends X-Priority, X-Tags, X-Click next to a JSON body).
	if p.Title == "" {
		p.Title = param(r, "Title", "Ti", "T")
	}
	if len(p.Tags) == 0 {
		p.Tags = splitList(param(r, "Tags", "Tag", "Ta"))
	}
	if p.Click == "" {
		p.Click = param(r, "Click")
	}
	if p.Icon == "" {
		p.Icon = ntfyIcon(param(r, "Icon"))
	}
	if p.Priority == 0 {
		if p.Priority, err = ntfyPriority(param(r, "Priority", "Prio", "P")); err != nil {
			return err
		}
	}
	return s.ntfyReply(w, r.Context(), s.ntfyAuth(r), p)
}

// ntfyJSONKeys are the JSON publish fields clrd reads; any other key is logged as ignored.
var ntfyJSONKeys = map[string]bool{"topic": true, "message": true, "title": true, "priority": true, "tags": true,
	"click": true, "icon": true, "attach": true, "filename": true, "actions": true, "delay": true, "timezone": true, "id": true,
	"sequence_id": true, "external_id": true, "status": true, "cache": true, "markdown": true}

// ntfyIgnoredParams are ntfy publish options clrd accepts but does not act on.
var ntfyIgnoredParams = [][]string{{"Email", "E-Mail", "Mail", "E"}, {"Call"},
	{"Firebase"}, {"UnifiedPush", "Up"}, {"Poll-Id"}}

var ignoredName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// noteIgnored counts each ntfy option a publish sent that clrd ignores, so
// an admin can see what a migrated service relies on (GET /api/v1/ntfy/ignored).
// The table holds at most 200 names; a full table only counts names it has.
func (s *Server) noteIgnored(r *http.Request, jsonKeys ...string) {
	var names []string
	for _, p := range ntfyIgnoredParams {
		if param(r, p...) != "" {
			names = append(names, p[0])
		}
	}
	for _, k := range jsonKeys {
		if !ignoredName.MatchString(k) {
			k = "(other)"
		}
		names = append(names, "json:"+k)
	}
	for _, n := range names {
		if _, err := s.db.ExecContext(r.Context(), `INSERT INTO ntfy_ignored (name, count, last_seen)
			SELECT ?1, 1, ?2 WHERE EXISTS (SELECT 1 FROM ntfy_ignored WHERE name = ?1) OR (SELECT COUNT(*) FROM ntfy_ignored) < 200
			ON CONFLICT(name) DO UPDATE SET count = count + 1, last_seen = excluded.last_seen`, n, ms(now())); err != nil {
			s.log.Warn("ntfy ignored log", "err", err)
		}
	}
}

var ignoredPage = template.Must(template.New("").Parse(`<!doctype html><meta charset="utf-8"><title>Ignored ntfy options</title>
<h1>ntfy options received but ignored</h1>
<table><tr><th>Option</th><th>Count</th><th>Last seen</th></tr>
{{range .}}<tr><td>{{.Name}}</td><td>{{.Count}}</td><td>{{.LastSeen.Format "2006-01-02 15:04:05Z07:00"}}</td></tr>
{{else}}<tr><td colspan="3">None yet.</td></tr>{{end}}</table>`))

func (s *Server) handleNtfyIgnored(w http.ResponseWriter, r *http.Request, tok *Token) error {
	if err := tok.need(ScopeAdmin, ""); err != nil {
		return err
	}
	type row struct {
		Name     string    `json:"name"`
		Count    int64     `json:"count"`
		LastSeen time.Time `json:"last_seen"`
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT name, count, last_seen FROM ntfy_ignored ORDER BY count DESC, name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []row{}
	for rows.Next() {
		var x row
		var seen int64
		if err := rows.Scan(&x.Name, &x.Count, &seen); err != nil {
			return err
		}
		x.LastSeen = fromMS(seen)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		return ignoredPage.Execute(w, out)
	}
	writeJSON(w, 200, map[string]any{"ignored": out})
	return nil
}

// ntfyAct: PUT /{topic}/{sid}/clear acks, DELETE /{topic}/{sid} resolves.
// sid is the external id, or the notification id when it had none.
func (s *Server) ntfyAct(action string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		tok, topic, sid := s.ntfyAuth(r), r.PathValue("topic"), r.PathValue("sid")
		n, err := s.ActByKey(r.Context(), tok, topic, sid, action, "ntfy")
		if isNotFound(err) {
			n, err = s.act(r.Context(), tok, action, "ntfy", nil, false, `id = ? AND topic = ?`, sid, topic)
		}
		if err != nil {
			return err
		}
		writeJSON(w, 200, s.ntfyMessage(n, n.Seq, false))
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
		tok := s.ntfyAuth(r)
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
		cur := s.CurrentSeq() // before the floors: anything newer is replayed in full
		since, err := s.ntfySince(r.Context(), sinceArg, topics)
		if err != nil {
			return err
		}
		floor, truncated, err := s.ntfyFloors(r.Context(), since, cur, sinceArg == "latest", topics)
		if err != nil {
			return err
		}
		lo := cur // a topic with nothing to replay needs nothing before now
		for _, f := range floor {
			lo = min(lo, f-1)
		}
		since = max(since, lo)
		if truncated {
			w.Header().Set("X-Messages-Truncated", "1") // ntfy's header; a no-op on the WebSocket
		}
		f, err := ntfyFilters(r)
		if err != nil {
			return err
		}

		var write func(m ntfyMsg) error
		ctx := r.Context()
		if format == "ws" {
			// The origin was checked by ntfyCORS, as on every ntfy route.
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
		// Only the replay lists delayed messages; live ones come when they
		// fire. The replay ends here, before "open": an event after this was
		// published once the client could see the stream was open, so it is live.
		// A poll has no live part: it lists all it returns.
		replayEnd := s.CurrentSeq()
		opened := func() error { return write(ntfyEvent("open", joined)) }
		if poll {
			opened = nil
		}
		// ntfy has no reset: since= means all that is left after it, so a
		// prune (before or during replay) restarts from the oldest event left,
		// up to 3 times, then goes on from now.
		resets := 0
		s.follow(ctx, tok, since, topics, opened, func(raw []byte, seq int64) error {
			if bytes.HasPrefix(raw, []byte(`{"type":"ready"`)) {
				if poll {
					return errStop
				}
				return nil
			}
			if seq < 0 {
				if resets++; resets <= 3 {
					return errPruned
				}
				return nil
			}
			var e Event
			if err := json.Unmarshal(raw, &e); err != nil {
				return err
			}
			if e.Type == EventMark { // ntfy has no marks; sending it again would alert again
				return nil
			}
			if f, ok := floor[e.Notification.Topic]; ok && seq < f {
				return nil
			}
			if !f.pass(e.Notification, ntfyMsgID(e.Notification, seq)) {
				return nil
			}
			return write(s.ntfyMessage(e.Notification, seq, f.sched && (poll || seq <= replayEnd)))
		}, func() error { return write(ntfyEvent("keepalive", joined)) })
		return nil
	}
}

// ntfyFilter is ntfy's subscribe filters: id, message and title match
// exactly, priority is any of a list, tags must all be there. Clear and
// delete events are matched on the notification they clear.
type ntfyFilter struct {
	id, message, title string
	prio               []int
	tags               []string
	sched              bool // ?scheduled=1: delayed messages show before they fire
}

func ntfyFilters(r *http.Request) (f ntfyFilter, err error) {
	f.id, f.message, f.title = param(r, "Id"), param(r, "Message", "M"), param(r, "Title", "T")
	if b := ntfyMarkdown(param(r, "Scheduled", "Sched")); b != nil {
		f.sched = *b
	}
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

// ntfyText is the shown title and message without the tags' emoji in front:
// ntfy apps put those there themselves.
func ntfyText(n *Notification) (title, message string) {
	title, message = n.shown()
	if p := tagEmoji(n.Tags); p != "" {
		if title != "" {
			title = strings.TrimPrefix(title, p)
		} else {
			message = strings.TrimPrefix(message, p)
		}
	}
	return
}

func (f ntfyFilter) pass(n *Notification, id string) bool {
	// n comes from the sent JSON: match the filter's text shown the same way,
	// with or without the tags' emoji in front (the sender may have typed them).
	title, message := ntfyText(n)
	fullTitle, fullMessage := n.shown()
	is := func(want, a, b string) bool { w := emojify(want); return w == a || w == b }
	if f.id != "" && f.id != id || f.message != "" && !is(f.message, message, fullMessage) ||
		f.title != "" && !is(f.title, title, fullTitle) {
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
		return 0, nil // ntfyFloors keeps each topic's latest
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
	if len(v) == 12 { // an imported ntfy message id, else one from another server: send everything
		var seq int64
		if s.db.QueryRowContext(ctx, `SELECT seq FROM ntfy_mids WHERE mid = ?`, v).Scan(&seq) == nil {
			return min(seq, cur), nil
		}
		if n, ok := liveSeq(v); ok { // after the imported ids, which can start with l too
			return min(n, cur), nil
		}
		return 0, nil
	}
	return 0, errf(400, "bad_request", "invalid since %q", v)
}

// ntfyReplayMax caps a replay per topic, as ntfy's 10 MB message poll limit.
var ntfyReplayMax = 10 << 20 // a var so a test can shrink it

// ntfyFloors is the first seq each topic replays in (since, cur], as ntfy does
// per topic (#845): its latest message for since=latest (marks and
// device-only events never count), else the oldest of its newest events whose
// bytes fit in ntfyReplayMax. truncated says some topic had more.
func (s *Server) ntfyFloors(ctx context.Context, since, cur int64, latest bool, topics []string) (map[string]int64, bool, error) {
	q := `SELECT topic, MIN(CASE WHEN run <= ? THEN seq ELSE 1 << 62 END), MAX(run > ?) FROM (
		SELECT topic, seq, SUM(length(CAST(data AS BLOB))) OVER (PARTITION BY topic ORDER BY seq DESC) AS run
		FROM events WHERE seq > ? AND seq <= ? AND type != ? AND data NOT LIKE '%"only":"%' AND topic IN (` + placeholders(len(topics)) + `)) GROUP BY topic`
	args := []any{ntfyReplayMax, ntfyReplayMax, since, cur, EventMark}
	if latest {
		q = `SELECT topic, MAX(seq), 0 FROM events WHERE seq > ? AND seq <= ? AND type != ? AND data NOT LIKE '%"only":"%'
			AND topic IN (` + placeholders(len(topics)) + `) GROUP BY topic`
		args = args[2:]
	}
	for _, t := range topics {
		args = append(args, t)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	floor, truncated := map[string]int64{}, false
	for rows.Next() {
		var t string
		var seq int64
		var cut bool
		if err := rows.Scan(&t, &seq, &cut); err != nil {
			return nil, false, err
		}
		floor[t], truncated = seq, truncated || cut
	}
	return floor, truncated, rows.Err()
}

// ntfyIcon keeps an http(s) icon URL and drops anything else, never a 400,
// so a publish ntfy accepts still succeeds.
func ntfyIcon(v string) string {
	if checkImageURL("icon", v) != nil {
		return ""
	}
	return v
}
