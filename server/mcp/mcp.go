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
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/thejdubb02/clrd"
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
			"serverInfo":      map[string]any{"name": "clrd", "version": clrd.Version()},
			"instructions":    "clrd notifications. send() to alert a human, wait_for_reply() to block until they ack or dismiss it, resolve() when the problem clears.",
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		tools, _ := fs.ReadFile(toolsFS, "tools.json") // embedded: cannot fail
		writeRPC(w, req.ID, map[string]any{"tools": json.RawMessage(tools)}, nil)
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		dec := json.NewDecoder(bytes.NewReader(req.Params))
		dec.UseNumber() // a big whole number keeps its exact digits on the way to the API (#355)
		err := dec.Decode(&p)
		if err == nil {
			_, err = exact(p.Arguments)
		}
		if err != nil {
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

// exact keeps a plain whole number as its digits (#355); any other number
// becomes a float64 as before, so 5.0 still reaches an int field as 5.
func exact(v any) (any, error) {
	var err error
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		if err != nil || strings.ContainsAny(string(t), ".eE") {
			return f, err
		}
	case map[string]any:
		for k, x := range t {
			if t[k], err = exact(x); err != nil {
				return nil, err
			}
		}
	case []any:
		for i, x := range t {
			if t[i], err = exact(x); err != nil {
				return nil, err
			}
		}
	}
	return v, nil
}

// num reads a number argument: a json.Number from a tool call, a float64
// from an in-process caller such as approve.
func num(a map[string]any, k string) (float64, bool) {
	switch v := a[k].(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	}
	return 0, false
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
		for _, k := range []string{"title", "format", "icon", "color"} {
			if v := str(a, k); v != "" {
				body[k] = v
			}
		}
		if v, ok := num(a, "priority"); ok {
			body["priority"] = int(v)
		}
		if v := str(a, "repeat"); v != "" {
			body["repeat"] = v
		}
		if v, ok := a["live"].(bool); ok && v {
			body["live"] = true
		}
		if v, ok := num(a, "repeat_times"); ok {
			body["repeat_times"] = int(v)
		}
		if v := str(a, "id"); v != "" {
			body["external_id"] = v // send's id is the sender's key, not the server ULID
		}
		if v, ok := a["actions"].([]any); ok {
			body["actions"] = v
		}
		if v, ok := a["devices"]; ok {
			body["devices"] = v // anything but a list of strings is the server's 400, never a send to everyone
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
	case "stats":
		return h.api(r, "GET", "/api/v1/stats?"+query(a, "days"), nil)
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
	case "subscriptions":
		path, body := "/api/v1/subscriptions", map[string]any{}
		if f := str(a, "for"); f != "" {
			path += "?for=" + url.QueryEscape(f)
		}
		for _, k := range []string{"topics", "new_topics"} {
			if v, ok := a[k]; ok {
				body[k] = v
			}
		}
		if len(body) == 0 {
			return h.api(r, "GET", path, nil)
		}
		return h.api(r, "PUT", path, body)
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
	case "mark":
		if err := need("id"); err != nil {
			return nil, err
		}
		if str(a, "mark") == "" {
			return h.api(r, "DELETE", "/api/v1/notifications/"+id+"/mark", nil)
		}
		return h.api(r, "POST", "/api/v1/notifications/"+id+"/mark",
			map[string]any{"mark": str(a, "mark"), "note": str(a, "note"), "link": str(a, "link")})
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
	if v, ok := num(a, "timeout_s"); ok && v > 0 {
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

// self is the caller's token id: its own row in the device list.
func (h *Handler) self(r *http.Request) string {
	out, _ := h.api(r, "GET", "/api/v1/devices?all=1", nil)
	ds, _ := out["devices"].([]any)
	for _, d := range ds {
		if m, _ := d.(map[string]any); m["this"] == true {
			return str(m, "id")
		}
	}
	return ""
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
		return answered(map[string]any{"reply": st, "by": n["state_by"], "notification": n}, st, str(n, "reply"), str(n, "reply_text")), nil
	}
	var last any = n
	var me string // the caller's token id, read at its first local clear event
	for {
		select {
		case <-r.Context().Done():
			// Read again before showing it: the stream is not filtered, and the
			// grant for this topic may have gone mid-wait (#428).
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			if _, err := h.api(r.WithContext(ctx), "GET", "/api/v1/notifications/"+id, nil); err != nil {
				return map[string]any{"reply": "timeout"}, nil
			}
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
			if e.Only != "" { // one device cleared it for itself: a reply only when that is the caller (#777)
				if me == "" {
					me = h.self(r)
				}
				if e.Only != me {
					continue
				}
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
			return answered(map[string]any{"reply": x.State, "by": x.StateBy, "notification": x}, x.State, x.Reply, x.ReplyText), nil
		}
	}
}

// answered adds which reply action was tapped. Only while the tap's own
// acknowledged state stands, so "by" is the device that tapped it. text is
// what was typed with a text reply action.
func answered(out map[string]any, state, reply, text string) map[string]any {
	if reply != "" && state == server.Acknowledged {
		out["answer"] = reply
		if text != "" {
			out["text"] = text
		}
	}
	return out
}

// restTools are tools that are one REST call: {x} in the path comes from the
// argument x, the other arguments are the JSON body.
var restTools = map[string]struct {
	method, path string
	via          bool // a state change: log it as made by an agent
}{
	"dismiss":             {"POST", "/api/v1/notifications/{id}/dismiss", true},
	"reopen":              {"POST", "/api/v1/notifications/{id}/reopen", true},
	"clear":               {"POST", "/api/v1/notifications/clear", true},
	"silences":            {"GET", "/api/v1/silences", false},
	"silence":             {"POST", "/api/v1/silences", false},
	"end_silence":         {"DELETE", "/api/v1/silences/{id}", false},
	"set_topic":           {"PUT", "/api/v1/topics/{topic}", false},
	"format_dismiss":      {"POST", "/api/v1/format-check/dismiss", false},
	"templates":           {"GET", "/api/v1/topics/{topic}/templates", false},
	"set_template":        {"PUT", "/api/v1/topics/{topic}/templates/{name}", false},
	"delete_template":     {"DELETE", "/api/v1/topics/{topic}/templates/{name}", false},
	"preview_template":    {"POST", "/api/v1/topics/{topic}/templates/{name}/preview", false},
	"hooks":               {"GET", "/api/v1/topics/{topic}/hooks", false},
	"set_hook":            {"PUT", "/api/v1/topics/{topic}/hooks/{name}", false},
	"delete_hook":         {"DELETE", "/api/v1/topics/{topic}/hooks/{name}", false},
	"hook_deliveries":     {"GET", "/api/v1/topics/{topic}/hooks/{name}/deliveries", false},
	"preview_hook":        {"POST", "/api/v1/topics/{topic}/hooks/preview", false},
	"hook_allow":          {"GET", "/api/v1/hooks/allow", false},
	"set_hook_allow":      {"PUT", "/api/v1/hooks/allow", false},
	"tokens":              {"GET", "/api/v1/tokens", false},
	"create_token":        {"POST", "/api/v1/tokens", false},
	"revoke_token":        {"DELETE", "/api/v1/tokens/{id}", false},
	"rename_token":        {"PUT", "/api/v1/tokens/{id}", false},
	"rotate_token":        {"POST", "/api/v1/tokens/{id}/rotate", false},
	"create_agent_invite": {"POST", "/api/v1/agents/invite", false},
	"users":               {"GET", "/api/v1/users", false},
	"create_user":         {"POST", "/api/v1/users", false},
	"set_user_grants":     {"PUT", "/api/v1/users/{id}", false},
	"delete_user":         {"DELETE", "/api/v1/users/{id}", false},
	"set_token_user":      {"PUT", "/api/v1/tokens/{id}/user", false},
	"create_gotify_app":   {"POST", "/api/v1/gotify/apps", false},
	"create_pushover_app": {"POST", "/api/v1/pushover/apps", false},
	"ntfy_anonymous":      {"GET", "/api/v1/ntfy/anonymous", false},
	"set_ntfy_anonymous":  {"PUT", "/api/v1/ntfy/anonymous", false},
	"ntfy_origins":        {"GET", "/api/v1/ntfy/origins", false},
	"set_ntfy_origins":    {"PUT", "/api/v1/ntfy/origins", false},
	"server_env":          {"GET", "/api/v1/server", false},
	"version":             {"GET", "/api/v1/version", false},
	"ntfy_ignored":        {"GET", "/api/v1/ntfy/ignored", false},
	"senders":             {"GET", "/api/v1/senders", false},
	"sender":              {"GET", "/api/v1/senders/{id}", false},
	"set_sender":          {"PUT", "/api/v1/senders/{id}", false},
	"sender_changes":      {"GET", "/api/v1/senders/{id}/changes", false},
	"undo_sender_change":  {"POST", "/api/v1/senders/{id}/changes/{seq}/undo", false},
	"push":                {"GET", "/api/v1/push", false},
	"set_push":            {"PUT", "/api/v1/push", false},
	"delete_push":         {"DELETE", "/api/v1/push", false},
}
