// Package mcp serves clrd's MCP endpoint (streamable HTTP, stateless, JSON
// responses). Every tool call is replayed against the server's own REST API
// in-process with the caller's Authorization header, so token scopes, topic
// allow-lists and state_by come from the server's normal auth, not from here.
//
// Mount: s.Mount("/mcp", mcp.New(s))
//
// Hand-rolled JSON-RPC rather than the official Go SDK: the SDK needs Go 1.25
// and pulls dependencies; this package is stdlib only.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/thejdubb02/clrd/server"
)

// MaxWait caps wait_for_reply. Keep it under the server's WriteTimeout and any
// proxy idle timeout in front of it.
var MaxWait = 600 * time.Second

// viaMCP marks a state change an agent made, so the log tells it from a person's tap.
var viaMCP = map[string]any{"via": "mcp"}

const latestProtocol = "2025-06-18"

// Streamable HTTP versions. 2024-11-05 is the older HTTP+SSE transport, which
// this endpoint does not speak.
var protocols = map[string]bool{"2025-03-26": true, "2025-06-18": true, "2025-11-25": true}

// API is what the handler needs from the server: its REST handler (the one
// that serves /api/v1/... and /{topic}/{external_id}/resolve) and its stream
// of committed events. *server.Server is one.
type API interface {
	http.Handler
	Subscribe() (<-chan server.Event, func())
}

// Handler is the /mcp endpoint.
type Handler struct{ rest API }

// New returns the MCP handler.
func New(api API) *Handler { return &Handler{rest: api} }

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a := r.Header.Get("Authorization"); len(a) < 8 || !strings.EqualFold(a[:7], "Bearer ") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="clrd"`)
		http.Error(w, `{"error":"bearer token required","code":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	// MCP transport rule against DNS rebinding: a browser Origin must match the host.
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			http.Error(w, `{"error":"origin not allowed","code":"forbidden"}`, http.StatusForbidden)
			return
		}
	}
	// Transport rule: an unsupported version header is a 400. No header means
	// 2025-03-26, which is supported.
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !protocols[v] {
		http.Error(w, `{"error":"unsupported MCP-Protocol-Version","code":"bad_request"}`, http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost {
		// No server-initiated stream: stateless server.
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/approve") {
		h.approve(w, r)
		return
	}
	var req rpcReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeRPC(w, nil, nil, &rpcErr{-32700, "parse error"})
		return
	}
	// Tool calls go through the API, which checks the token. Everything else
	// would answer any Bearer value, so check it here.
	if req.Method != "tools/call" && !h.tokenOK(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="clrd", error="invalid_token"`)
		http.Error(w, `{"error":"missing or invalid token","code":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if req.ID == nil { // notification, e.g. notifications/initialized
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := latestProtocol
		if protocols[p.ProtocolVersion] {
			v = p.ProtocolVersion
		}
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "clrd", "version": "0.1.0"},
			"instructions":    "clrd notifications. send() to alert a human, wait_for_reply() to block until they ack or dismiss it, resolve() when the problem clears.",
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": tools}, nil)
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeRPC(w, req.ID, nil, &rpcErr{-32602, "invalid params"})
			return
		}
		res, err := h.call(r, p.Name, p.Arguments)
		if err != nil {
			writeRPC(w, req.ID, toolResult(err.Error(), true), nil)
			return
		}
		b, _ := json.Marshal(res)
		writeRPC(w, req.ID, toolResult(string(b), false), nil)
	default:
		writeRPC(w, req.ID, nil, &rpcErr{-32601, "method not found: " + req.Method})
	}
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, e *rpcErr) {
	if id == nil {
		id = json.RawMessage("null")
	}
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		out["error"] = e
	} else {
		out["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// api calls the REST handler in-process with the caller's auth header.
func (h *Handler) api(r *http.Request, method, path string, body any) (map[string]any, error) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd).WithContext(r.Context())
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = r.RemoteAddr
	req.Host, req.TLS = r.Host, r.TLS // links the server builds point at the caller's address, not example.com
	for _, k := range []string{"Origin", "X-Forwarded-Proto"} {
		req.Header[k] = r.Header[k]
	}
	rec := httptest.NewRecorder()
	h.rest.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		var v any
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err == nil {
			if m, ok := v.(map[string]any); ok {
				out = m
			} else {
				out = map[string]any{"items": v}
			}
		}
	}
	if rec.Code >= 300 {
		msg := strings.TrimSpace(rec.Body.String())
		if out != nil && out["error"] != nil {
			msg = fmt.Sprint(out["error"])
		}
		return nil, fmt.Errorf("%s %s: %d %s", method, path, rec.Code, msg)
	}
	return out, nil
}

// tokenOK reports whether the bearer token is real. Reading an id that
// cannot exist costs one token lookup and one indexed read: the API answers
// 401 for a bad token and 404 for any real one, whatever its scopes.
func (h *Handler) tokenOK(r *http.Request) bool {
	req := httptest.NewRequest("GET", "/api/v1/notifications/-", nil).WithContext(r.Context())
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	req.RemoteAddr = r.RemoteAddr
	rec := httptest.NewRecorder()
	h.rest.ServeHTTP(rec, req)
	return rec.Code != http.StatusUnauthorized
}

// query is the arguments named in keys that are set, as a URL query.
func query(a map[string]any, keys ...string) string {
	q := url.Values{}
	for _, k := range keys {
		if v := str(a, k); v != "" {
			q.Set(k, v)
		}
	}
	return q.Encode()
}

func str(a map[string]any, k string) string {
	if v, ok := a[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func (h *Handler) call(r *http.Request, name string, a map[string]any) (any, error) {
	if a == nil {
		a = map[string]any{}
	}
	id := url.PathEscape(str(a, "id"))
	need := func(keys ...string) error {
		for _, k := range keys {
			if str(a, k) == "" {
				return fmt.Errorf("%s is required", k)
			}
		}
		return nil
	}
	if t, ok := restTools[name]; ok {
		body, path := map[string]any{}, t.path
		for k, v := range a {
			body[k] = v
		}
		for strings.Contains(path, "{") {
			i := strings.Index(path, "{")
			j := i + strings.Index(path[i:], "}")
			k := path[i+1 : j]
			if err := need(k); err != nil {
				return nil, err
			}
			path = path[:i] + url.PathEscape(str(a, k)) + path[j+1:]
			delete(body, k)
		}
		if t.method == "GET" || t.method == "DELETE" {
			return h.api(r, t.method, path, nil)
		}
		if t.via {
			body["via"] = "mcp"
		}
		return h.api(r, t.method, path, body)
	}
	switch name {
	case "send":
		if err := need("topic", "message"); err != nil {
			return nil, err
		}
		body := map[string]any{"topic": str(a, "topic"), "message": str(a, "message")}
		for _, k := range []string{"title", "format", "icon"} {
			if v := str(a, k); v != "" {
				body[k] = v
			}
		}
		if v, ok := a["priority"].(float64); ok {
			body["priority"] = int(v)
		}
		if v := str(a, "repeat"); v != "" {
			body["repeat"] = v
		}
		if v, ok := a["repeat_times"].(float64); ok {
			body["repeat_times"] = int(v)
		}
		if v := str(a, "id"); v != "" {
			body["external_id"] = v // send's id is the sender's key, not the server ULID
		}
		if v, ok := a["actions"].([]any); ok {
			body["actions"] = v
		}
		if v, ok := a["attachment"].(map[string]any); ok {
			if str(v, "url") == "" {
				return nil, fmt.Errorf("attachment.url is required")
			}
			body["attachment"] = map[string]any{"url": str(v, "url"), "name": str(v, "name")} // link only; type and size are the server's
		}
		// Core keeps an existing row's state when a key is reused, so a new
		// question could inherit the last answer. Reopen it in the same write.
		body["reopen"] = true
		return h.api(r, "POST", "/api/v1/notifications", body)
	case "list":
		q := url.Values{}
		if v := str(a, "topic"); v != "" {
			q.Set("topic", v)
		}
		st := str(a, "state")
		if st == "" {
			st = "open"
		}
		q.Set("state", st)
		return h.api(r, "GET", "/api/v1/notifications?"+q.Encode(), nil)
	case "topics":
		return h.api(r, "GET", "/api/v1/topics", nil)
	case "counts":
		return h.api(r, "GET", "/api/v1/counts?"+query(a, "keep_cleared_days", "topic"), nil)
	case "format_check":
		return h.api(r, "GET", "/api/v1/format-check?"+query(a, "topic", "by"), nil)
	case "devices":
		return h.api(r, "GET", "/api/v1/devices?all=1", nil)
	case "device_log":
		return h.api(r, "GET", "/api/v1/devices/log", nil)
	case "unlink":
		if err := need("id"); err != nil {
			return nil, err
		}
		return h.api(r, "DELETE", "/api/v1/devices/"+id, nil)
	case "sound":
		if _, set := a["sound"]; !set { // an empty name goes to the server, which refuses it (#299)
			return h.api(r, "GET", "/api/v1/sounds", nil)
		}
		if err := need("id"); err != nil {
			return nil, err
		}
		return h.api(r, "PUT", "/api/v1/devices/"+id, map[string]any{"sound": str(a, "sound")})
	case "request_approve":
		if err := need("approve"); err != nil {
			return nil, err
		}
		return h.api(r, "POST", "/api/v1/devices/approve-request", map[string]any{"approve": str(a, "approve"), "topic": str(a, "topic")})
	case "settings":
		if len(a) == 0 {
			return h.api(r, "GET", "/api/v1/settings", nil)
		}
		return h.api(r, "PUT", "/api/v1/settings", a)
	case "get":
		if err := need("id"); err != nil {
			return nil, err
		}
		return h.api(r, "GET", "/api/v1/notifications/"+id, nil)
	case "update":
		if err := need("id", "message"); err != nil {
			return nil, err
		}
		return h.api(r, "PATCH", "/api/v1/notifications/"+id, map[string]any{"message": str(a, "message")})
	case "ack":
		if err := need("id"); err != nil {
			return nil, err
		}
		return h.api(r, "POST", "/api/v1/notifications/"+id+"/ack", viaMCP)
	case "resolve":
		if str(a, "topic") != "" && str(a, "external_id") != "" {
			return h.api(r, "POST", "/"+url.PathEscape(str(a, "topic"))+"/"+url.PathEscape(str(a, "external_id"))+"/resolve", viaMCP)
		}
		if err := need("id"); err != nil {
			return nil, fmt.Errorf("id, or topic and external_id, is required")
		}
		return h.api(r, "POST", "/api/v1/notifications/"+id+"/resolve", viaMCP)
	case "snooze":
		if err := need("id", "for"); err != nil {
			return nil, err
		}
		return h.api(r, "POST", "/api/v1/notifications/"+id+"/snooze", map[string]any{"for": str(a, "for"), "via": "mcp"})
	case "wait_for_reply":
		if err := need("id"); err != nil {
			return nil, err
		}
		return h.wait(r, id, a)
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

// wait blocks until a human acknowledges or dismisses, the sender resolves, or
// the timeout passes. It follows committed events rather than sampling the
// state, so an ack quickly followed by a snooze still counts as the reply.
func (h *Handler) wait(r *http.Request, id string, a map[string]any) (any, error) {
	d := 60 * time.Second
	if v, ok := a["timeout_s"].(float64); ok && v > 0 {
		d = time.Duration(v * float64(time.Second))
	}
	if d > MaxWait {
		d = MaxWait
	}
	parent := r.Context()
	ctx, cancel := context.WithTimeout(parent, d)
	defer cancel()
	r = r.WithContext(ctx) // the lookup shares the deadline
	for {
		evs, stop := h.rest.Subscribe() // before the read, so a change right after it is not missed
		res, err := h.watch(r, id, evs)
		stop()
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		if res != nil || err != nil {
			return res, err
		}
		// Dropped for falling behind: read the state again and carry on.
	}
}

func replied(state string) bool { return state != server.Active && state != server.Snoozed } // snoozed = "ask me later"

// watch reads the notification, then follows its events until a reply or the
// deadline. nil, nil means the stream dropped us.
func (h *Handler) watch(r *http.Request, id string, evs <-chan server.Event) (map[string]any, error) {
	n, err := h.api(r, "GET", "/api/v1/notifications/"+id, nil)
	if err != nil {
		if r.Context().Err() != nil {
			return map[string]any{"reply": "timeout"}, nil
		}
		return nil, err
	}
	if st := str(n, "state"); replied(st) {
		return answered(map[string]any{"reply": st, "by": n["state_by"], "notification": n}, st, str(n, "reply")), nil
	}
	var last any = n
	for {
		select {
		case <-r.Context().Done():
			return map[string]any{"reply": "timeout", "notification": last}, nil
		case e, ok := <-evs:
			if !ok {
				return nil, nil
			}
			// Every event here committed after Subscribe, so after the call
			// started, even one the read above already shows overwritten. The
			// stream is not filtered by token, but the read proved this id is
			// visible.
			x := e.Notification
			if x == nil || x.ID != str(n, "id") {
				continue
			}
			last = x
			if !replied(x.State) {
				continue
			}
			// Read again so a token revoked mid-wait gets an error, not the reply.
			if _, err := h.api(r, "GET", "/api/v1/notifications/"+id, nil); err != nil {
				if r.Context().Err() != nil { // unproven token: show nothing
					return map[string]any{"reply": "timeout"}, nil
				}
				return nil, err
			}
			return answered(map[string]any{"reply": x.State, "by": x.StateBy, "notification": x}, x.State, x.Reply), nil
		}
	}
}

// answered adds which reply action was tapped. Only while the tap's own
// acknowledged state stands, so "by" is the device that tapped it.
func answered(out map[string]any, state, reply string) map[string]any {
	if reply != "" && state == server.Acknowledged {
		out["answer"] = reply
	}
	return out
}

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func p(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

var tools = []map[string]any{
	{"name": "send", "description": "Send a notification to a topic. Reusing an id while the previous one is still open updates it instead of making a new one.",
		"inputSchema": schema(map[string]any{
			"topic":        p("string", "Topic, e.g. infra"),
			"message":      p("string", "Body text"),
			"title":        p("string", "Optional title"),
			"format":       map[string]any{"type": "string", "enum": []string{"markdown", "text"}, "description": "markdown (default) or text, which is shown exactly as sent"},
			"icon":         p("string", "Optional http(s) image URL shown with the notification; the topic's icon when omitted"),
			"attachment":   schema(map[string]any{"url": p("string", "http(s) URL of the file"), "name": p("string", "Optional file name")}, "url"),
			"priority":     map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "description": "1..5, 5 = urgent"},
			"repeat":       p("string", "Optional: alert again every this long (1m to 24h, e.g. 5m) until someone acknowledges, snoozes, dismisses or resolves it"),
			"repeat_times": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "How many times to repeat at most (default 10)"},
			"id":           p("string", "Optional stable key (external_id), e.g. disk-build. Use it later with resolve(topic, external_id)."),
			"actions": map[string]any{"type": "array", "maxItems": 3,
				"description": "Optional answer buttons, e.g. [{\"label\":\"Yes\",\"reply\":\"yes\"},{\"label\":\"No\",\"reply\":\"no\"}]. wait_for_reply returns the tapped one as answer.",
				"items":       schema(map[string]any{"label": p("string", "Button text"), "reply": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,64}$", "description": "Value returned as answer, unique"}}, "label", "reply")},
		}, "topic", "message")},
	{"name": "list", "description": "List notifications.",
		"inputSchema": schema(map[string]any{
			"topic": p("string", "Filter by topic"),
			"state": p("string", "open (default), all, active, acknowledged, snoozed, dismissed or resolved"),
		})},
	{"name": "topics", "description": "List the topics this token can read, with how many notifications are open in each.",
		"inputSchema": schema(map[string]any{})},
	{"name": "counts", "description": "Exact number of notifications in each state (active, acknowledged, snoozed, dismissed, resolved), plus inbox (active and acknowledged) and cleared (dismissed and resolved). With keep_cleared_days, also would_remove: how many cleared messages that setting would delete.",
		"inputSchema": schema(map[string]any{"keep_cleared_days": p("integer", "Preview a keep_cleared_days value before setting it"),
			"topic": p("string", "With keep_cleared_days, would_remove counts only this topic, to preview a topic_keep_cleared_days value")})},
	{"name": "format_check", "description": "Find formatting problems in the newest 500 messages, by topic: Markdown sent as plain text, unknown :shortcodes:, no title, very long messages, raw JSON. Each topic has a count and an example per problem, and a prompt that says how to fix the sender. Fix the sending code with it, then send a test message. With by=sender, one group per topic and sending token. Dismissed findings and topics turned off are left out and listed in dismissed and off.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Check only this topic"), "by": p("string", "topic (the default) or sender")})},
	{"name": "format_dismiss", "description": "Stop a format_check rule showing for a topic, for one sending token (sender_id from format_check by=sender) or every sender. dismissed=false shows it again. To turn the whole check off for a topic, use set_topic with format_check=false.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic"), "rule": p("string", "Rule id, like no_title"),
			"sender_id": p("string", "Sending token id; leave out for every sender"), "dismissed": p("boolean", "false to show it again; default true")}, "topic", "rule")},
	{"name": "settings", "description": "Read the server settings, or change them: pass only the fields to change. keep_cleared_days (0 = forever) deletes cleared messages older than that on the next hourly cleanup and needs an admin token; preview it with counts first.",
		"inputSchema": schema(map[string]any{
			"keep_cleared_days":       p("integer", "Days to keep dismissed and resolved messages, counted from when they were cleared; 0 = forever"),
			"topic_keep_cleared_days": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": []string{"integer", "null"}}, "description": "Per-topic keep_cleared_days, like {\"alerts\": 7}; null drops a topic's own value so it follows keep_cleared_days again"},
			"render_formatting":       p("boolean", "Show Markdown formatting"),
			"autolink":                p("boolean", "Turn web addresses into links"),
			"favicons":                p("boolean", "Show site icons next to links"),
			"link_previews":           p("boolean", "Show link previews"),
			"snooze_intervals":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Snooze menu times as durations like 30m or 4h, 1 to 8"},
			"snooze_default":          p("string", "The snooze time a plain tap uses, one of snooze_intervals"),
		})},
	{"name": "get", "description": "Get one notification by its server id, with its current state.",
		"inputSchema": schema(map[string]any{"id": p("string", "Server id")}, "id")},
	{"name": "update", "description": "Replace the message of an open notification, keeping its id and state. Use it for progress (step 3 of 10) instead of sending a new one each time.",
		"inputSchema": schema(map[string]any{"id": p("string", "Server id from send"), "message": p("string", "New body text")}, "id", "message")},
	{"name": "ack", "description": "Acknowledge a notification by its server id.",
		"inputSchema": schema(map[string]any{"id": p("string", "Server id")}, "id")},
	{"name": "resolve", "description": "Resolve (sender clears) a notification by server id, or by topic plus external_id. Clears it from every device.",
		"inputSchema": schema(map[string]any{
			"id":          p("string", "Server id"),
			"topic":       p("string", "Topic, with external_id"),
			"external_id": p("string", "The id given to send"),
		})},
	{"name": "snooze", "description": "Snooze a notification by server id.",
		"inputSchema": schema(map[string]any{"id": p("string", "Server id"), "for": p("string", "Duration, e.g. 30m or 1h")}, "id", "for")},
	{"name": "wait_for_reply", "description": "Block until a human acknowledges or dismisses, or the sender resolves the notification, or the timeout passes. Returns reply = the new state, or timeout, and answer = the reply value when one of send's actions was tapped.",
		"inputSchema": schema(map[string]any{"id": p("string", "Server id from send"), "timeout_s": map[string]any{"type": "number", "description": "Seconds to wait, default 60, max 600"}}, "id")},
	{"name": "devices", "description": "List every linked device this token can see: id, name, kind (token, browser or phone), linked and last seen times, who may approve (allowed, ask or denied), and whether it can be unlinked.",
		"inputSchema": schema(map[string]any{})},
	{"name": "device_log", "description": "Recent links, sign-ins and unlinks of those devices, newest first, with the device that did each.",
		"inputSchema": schema(map[string]any{})},
	{"name": "unlink", "description": "Unlink a browser or phone by its id from devices. It is signed out at once, with every browser it approved.",
		"inputSchema": schema(map[string]any{"id": p("string", "Device id from devices")}, "id")},
	{"name": "sound", "description": "Notification sounds, chosen per device. No arguments lists the valid sound names and the default; devices shows each device's current sound. With id and sound, sets that device's sound.",
		"inputSchema": schema(map[string]any{"id": p("string", "Device id from devices"), "sound": p("string", "A name from the list, e.g. chime or none")})},
	{"name": "request_approve", "description": "Ask the user to let this token answer approval requests. devices shows its current approve setting. Posts a Confirm prompt on topic; the setting changes only when an allowed device taps Confirm. Returns the prompt: wait_for_reply on its id gives confirm or reject.",
		"inputSchema": schema(map[string]any{"approve": p("string", "allowed, or ask (its taps then wait for an allowed device to confirm)"), "topic": p("string", "Topic for the prompt, default agents; needs publish there")}, "approve")},
	{"name": "dismiss", "description": "Dismiss (clear) a notification by its server id. It goes away on every device; reopen undoes it.",
		"inputSchema": schema(map[string]any{"id": p("string", "Server id")}, "id")},
	{"name": "reopen", "description": "Make a dismissed, acknowledged, snoozed or resolved notification active again, by its server id.",
		"inputSchema": schema(map[string]any{"id": p("string", "Server id")}, "id")},
	{"name": "clear", "description": "Dismiss every open notification in a topic, or a list of them by server id. Give topic or ids, not both. Returns how many were cleared.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic to clear"), "ids": strs("Server ids to clear")})},
	{"name": "silences", "description": "List silences that have not ended yet.",
		"inputSchema": schema(map[string]any{})},
	{"name": "silence", "description": "Mute a topic, a tag, or a tag in one topic until a set time. Notifications still arrive and sync but do not ring. Give for or until.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic to mute"), "tag": p("string", "Tag to mute, in every topic unless topic is set"),
			"for": p("string", "How long, like 2h or 30m"), "until": p("string", "End time, RFC 3339"), "comment": p("string", "Why, shown in the list")})},
	{"name": "end_silence", "description": "End a silence now, by its id from silences.",
		"inputSchema": schema(map[string]any{"id": p("string", "Silence id")}, "id")},
	{"name": "set_topic", "description": "Set a topic's icon, its check-in timer, or whether format_check looks at it. With checkin, the topic alerts if nothing arrives for that long (a dead man's switch). \"\" removes the icon or the timer.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic"), "icon": p("string", "Image URL, or \"\" to remove"), "checkin": p("string", "Duration like 25h, or \"\" for off"),
			"format_check": p("boolean", "false turns the format check off for this topic, true back on")}, "topic")},
	{"name": "templates", "description": "List a topic's incoming webhook templates, saved and built in. A service posts to /{topic}?template={name}.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic")}, "topic")},
	{"name": "set_template", "description": "Save an incoming webhook template: how a service's JSON becomes a notification. Fields other than each and secret are Go templates over the posted JSON, like {{.alert.name}}. Needs title or message unless name is a built-in template.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic"), "name": p("string", "Template name, used as ?template={name}"),
			"title": p("string", "Title template"), "message": p("string", "Message template"), "priority": p("string", "Renders to 1-5 or min, low, default, high, urgent"),
			"tags": p("string", "Renders to a comma separated list"), "click": p("string", "Link to open"), "id": p("string", "Renders to an external id, so the same id updates one notification"),
			"resolve": p("string", "Renders to true to resolve the notification with that id"), "each": p("string", "Dotted path to an array: one notification per item"),
			"secret": p("string", "Shared secret the service sends; never returned")}, "topic", "name")},
	{"name": "delete_template", "description": "Delete a saved incoming webhook template.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic"), "name": p("string", "Template name")}, "topic", "name")},
	{"name": "preview_template", "description": "Dry run an incoming webhook template over a sample payload: returns the notifications a post would publish, stores and sends nothing. Give template to try an unsaved one; otherwise the saved or built-in template named name is used.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic"), "name": p("string", "Template name"),
			"payload":  map[string]any{"type": []string{"object", "array"}, "description": "The JSON a service would post"},
			"headers":  map[string]any{"type": "object", "description": "Request headers the service would send, like X-GitHub-Event"},
			"template": map[string]any{"type": "object", "description": "An unsaved template with the set_template fields"}}, "topic", "name", "payload")},
	{"name": "hooks", "description": "List a topic's outgoing webhooks: URLs clrd calls when its notifications are dismissed, acknowledged, resolved, snoozed or an action is tapped. Needs an admin token.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic")}, "topic")},
	{"name": "set_hook", "description": "Add or replace an outgoing webhook. Saving again turns a failing hook back on. Needs an admin token.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic"), "name": p("string", "Hook name"), "url": p("string", "http(s) URL to call"),
			"events": strs("Any of dismissed, acknowledged, resolved, snoozed, action; default all"), "secret": p("string", "Signs each call with HMAC-SHA256; never returned")}, "topic", "name", "url")},
	{"name": "delete_hook", "description": "Delete an outgoing webhook. Needs an admin token.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic"), "name": p("string", "Hook name")}, "topic", "name")},
	{"name": "hook_deliveries", "description": "Recent calls of one outgoing webhook, with status and error. Needs an admin token.",
		"inputSchema": schema(map[string]any{"topic": p("string", "Topic"), "name": p("string", "Hook name")}, "topic", "name")},
	{"name": "tokens", "description": "List API tokens (never their secrets). Needs an admin token.",
		"inputSchema": schema(map[string]any{})},
	{"name": "create_token", "description": "Create an API token for a phone, script or agent. The secret is in the result once and never again. It starts denied for approval requests; raise it with request_approve or from an allowed device. Limit it with topics, or with grants (per-topic scopes, first match wins), not both. Needs an admin token.",
		"inputSchema": schema(map[string]any{"name": p("string", "Who uses it, e.g. backup-job"), "scopes": strs("Any of read, publish, admin"),
			"topics": strs("Topics it may use; leave out for every topic"),
			"grants": map[string]any{"type": "array", "description": "Per-topic rules, like [{\"topic\":\"home-*\",\"scopes\":[\"publish\"]}]; each grant's scopes must be in scopes",
				"items": schema(map[string]any{"topic": p("string", "Topic or prefix ending in *"), "scopes": strs("Scopes on that topic")}, "topic", "scopes")}}, "name", "scopes")},
	{"name": "revoke_token", "description": "Revoke an API token by its id from tokens. Its open streams close at once. Needs an admin token.",
		"inputSchema": schema(map[string]any{"id": p("string", "Token id")}, "id")},
	{"name": "create_gotify_app", "description": "Create a Gotify app key for one topic, for senders built for Gotify (shoutrrr, Watchtower, the gotify CLI). The key is in the result once; it only publishes, only to that topic, only on POST /message. Revoke it with revoke_token and the result's token_id. Needs an admin token.",
		"inputSchema": schema(map[string]any{"name": p("string", "App name, the title when a message has none"), "topic": p("string", "Topic it posts to"),
			"priority": p("integer", "Gotify priority 0 to 10 for messages that send none; leave out for clrd's default")}, "name", "topic")},
	{"name": "server_env", "description": "Every CLRD_* environment setting in effect, defaults filled in. Read only: these change by setting the variable and restarting. Needs an admin token.",
		"inputSchema": schema(map[string]any{})},
	{"name": "ntfy_ignored", "description": "ntfy publish options senders used that clrd accepts but ignores (email, call and the like), with counts, so a migrated sender can be fixed. Needs an admin token.",
		"inputSchema": schema(map[string]any{})},
}

// restTools are tools that are one REST call: {x} in the path comes from the
// argument x, the other arguments are the JSON body.
var restTools = map[string]struct {
	method, path string
	via          bool // a state change: log it as made by an agent
}{
	"dismiss":           {"POST", "/api/v1/notifications/{id}/dismiss", true},
	"reopen":            {"POST", "/api/v1/notifications/{id}/reopen", true},
	"clear":             {"POST", "/api/v1/notifications/clear", true},
	"silences":          {"GET", "/api/v1/silences", false},
	"silence":           {"POST", "/api/v1/silences", false},
	"end_silence":       {"DELETE", "/api/v1/silences/{id}", false},
	"set_topic":         {"PUT", "/api/v1/topics/{topic}", false},
	"format_dismiss":    {"POST", "/api/v1/format-check/dismiss", false},
	"templates":         {"GET", "/api/v1/topics/{topic}/templates", false},
	"set_template":      {"PUT", "/api/v1/topics/{topic}/templates/{name}", false},
	"delete_template":   {"DELETE", "/api/v1/topics/{topic}/templates/{name}", false},
	"preview_template":  {"POST", "/api/v1/topics/{topic}/templates/{name}/preview", false},
	"hooks":             {"GET", "/api/v1/topics/{topic}/hooks", false},
	"set_hook":          {"PUT", "/api/v1/topics/{topic}/hooks/{name}", false},
	"delete_hook":       {"DELETE", "/api/v1/topics/{topic}/hooks/{name}", false},
	"hook_deliveries":   {"GET", "/api/v1/topics/{topic}/hooks/{name}/deliveries", false},
	"tokens":            {"GET", "/api/v1/tokens", false},
	"create_token":      {"POST", "/api/v1/tokens", false},
	"revoke_token":      {"DELETE", "/api/v1/tokens/{id}", false},
	"create_gotify_app": {"POST", "/api/v1/gotify/apps", false},
	"server_env":        {"GET", "/api/v1/server", false},
	"ntfy_ignored":      {"GET", "/api/v1/ntfy/ignored", false},
}

func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
