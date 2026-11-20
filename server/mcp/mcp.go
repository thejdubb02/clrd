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
var MaxWait = 300 * time.Second

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
	case "ack":
		if err := need("id"); err != nil {
			return nil, err
		}
		return h.api(r, "POST", "/api/v1/notifications/"+id+"/ack", nil)
	case "resolve":
		if str(a, "topic") != "" && str(a, "external_id") != "" {
			return h.api(r, "POST", "/"+url.PathEscape(str(a, "topic"))+"/"+url.PathEscape(str(a, "external_id"))+"/resolve", nil)
		}
		if err := need("id"); err != nil {
			return nil, fmt.Errorf("id, or topic and external_id, is required")
		}
		return h.api(r, "POST", "/api/v1/notifications/"+id+"/resolve", nil)
	case "snooze":
		if err := need("id", "for"); err != nil {
			return nil, err
		}
		return h.api(r, "POST", "/api/v1/notifications/"+id+"/snooze", map[string]any{"for": str(a, "for")})
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
			"topic":      p("string", "Topic, e.g. infra"),
			"message":    p("string", "Body text"),
			"title":      p("string", "Optional title"),
			"format":     map[string]any{"type": "string", "enum": []string{"markdown", "text"}, "description": "markdown (default) or text, which is shown exactly as sent"},
			"icon":       p("string", "Optional http(s) image URL shown with the notification; the topic's icon when omitted"),
			"attachment": schema(map[string]any{"url": p("string", "http(s) URL of the file"), "name": p("string", "Optional file name")}, "url"),
			"priority":   map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "description": "1..5, 5 = urgent"},
			"id":         p("string", "Optional stable key (external_id), e.g. disk-build. Use it later with resolve(topic, external_id)."),
			"actions": map[string]any{"type": "array", "maxItems": 3,
				"description": "Optional answer buttons, e.g. [{\"label\":\"Yes\",\"reply\":\"yes\"},{\"label\":\"No\",\"reply\":\"no\"}]. wait_for_reply returns the tapped one as answer.",
				"items":       schema(map[string]any{"label": p("string", "Button text"), "reply": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,64}$", "description": "Value returned as answer, unique"}}, "label", "reply")},
		}, "topic", "message")},
	{"name": "list", "description": "List notifications.",
		"inputSchema": schema(map[string]any{
			"topic": p("string", "Filter by topic"),
			"state": p("string", "open (default), all, active, acknowledged, snoozed, dismissed or resolved"),
		})},
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
		"inputSchema": schema(map[string]any{"id": p("string", "Server id from send"), "timeout_s": map[string]any{"type": "number", "description": "Seconds to wait, default 60, max 300"}}, "id")},
}
