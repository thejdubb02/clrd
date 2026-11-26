package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/thejdubb02/clrd/server"
)

// fakeAPI records every call and answers like the REST API in docs/SPEC.md.
type fakeAPI struct {
	mu    sync.Mutex
	calls []string
	body  map[string]any
	raw   string // the body as sent
	auth  string
	state string
	gets  int // GETs allowed before the token is revoked (401); 0 means no limit
	ngets int
	slow  bool                // a refused GET answers only after the deadline
	subs  []chan server.Event // handed out by Subscribe in order, then a silent one
	nsub  int
	onSub func(n int) // runs under mu on the nth Subscribe
}

func (f *fakeAPI) Subscribe() (<-chan server.Event, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nsub++; f.onSub != nil {
		f.onSub(f.nsub)
	}
	if len(f.subs) == 0 {
		return make(chan server.Event), func() {}
	}
	ch := f.subs[0]
	f.subs = f.subs[1:]
	return ch, func() {}
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	f.auth = r.Header.Get("Authorization")
	f.body = nil
	b, _ := io.ReadAll(r.Body)
	f.raw = string(b)
	_ = json.Unmarshal(b, &f.body)
	if r.Header.Get("Authorization") != "Bearer clrd_good" {
		w.WriteHeader(403)
		io.WriteString(w, `{"error":"token lacks scope","code":"forbidden"}`)
		return
	}
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/notifications/") {
		if f.ngets++; f.gets > 0 && f.ngets > f.gets {
			if f.slow {
				<-r.Context().Done()
			}
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"token revoked","code":"unauthorized"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "01J", "state": f.state, "state_by": "pixel-9", "seq": 7})
		return
	}
	if r.Method == "GET" {
		io.WriteString(w, `[{"id":"01J"}]`)
		return
	}
	io.WriteString(w, `{"id":"01J","state":"active"}`)
}

func (f *fakeAPI) last() (string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1], f.body
}

func rpc(t *testing.T, h http.Handler, auth, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func callTool(t *testing.T, h http.Handler, name string, args map[string]any) (text string, isErr bool) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	code, out := rpc(t, h, "Bearer clrd_good", string(b))
	if code != 200 {
		t.Fatalf("%s: http %d", name, code)
	}
	res := out["result"].(map[string]any)
	return res["content"].([]any)[0].(map[string]any)["text"].(string), res["isError"].(bool)
}

func TestForeignOriginRejected(t *testing.T) {
	req := httptest.NewRequest("POST", "http://clrd.example/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer clrd_good")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	New(&fakeAPI{}).ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
	req.Header.Set("Origin", "http://clrd.example")
	rec = httptest.NewRecorder()
	New(&fakeAPI{}).ServeHTTP(rec, req)
	if rec.Code == 403 {
		t.Fatal("same origin rejected")
	}
}

func TestAuthRequired(t *testing.T) {
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	New(&fakeAPI{}).ServeHTTP(rec, req)
	if rec.Code != 401 || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestBadTokenSurfacesAsToolError(t *testing.T) {
	h := New(&fakeAPI{})
	b := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ack","arguments":{"id":"01J"}}}`
	_, out := rpc(t, h, "Bearer clrd_bad", b)
	res := out["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "403") {
		t.Fatalf("want 403 tool error, got %v", res)
	}
}

func TestInitializeAndList(t *testing.T) {
	h := New(&fakeAPI{})
	_, out := rpc(t, h, "Bearer clrd_good", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if out["result"].(map[string]any)["protocolVersion"] != "2025-03-26" {
		t.Fatalf("version not echoed: %v", out)
	}
	code, _ := rpc(t, h, "Bearer clrd_good", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if code != 202 {
		t.Fatalf("notification: want 202, got %d", code)
	}
	_, out = rpc(t, h, "Bearer clrd_good", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var names []string
	for _, tl := range out["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "send,list,topics,counts,format_check,format_dismiss,settings,get,update,mark,ack,resolve,snooze,wait_for_reply,devices,device_theme,device_log,unlink,sound,subscriptions,request_approve,"+
		"dismiss,reopen,clear,silences,silence,end_silence,set_topic,templates,set_template,delete_template,preview_template,"+
		"hooks,set_hook,preview_hook,hook_allow,set_hook_allow,delete_hook,hook_deliveries,tokens,create_token,revoke_token,rename_token,rotate_token,create_agent_invite,users,create_user,set_user_grants,delete_user,set_token_user,create_gotify_app,create_pushover_app,ntfy_anonymous,set_ntfy_anonymous,ntfy_origins,set_ntfy_origins,server_env,version,ntfy_ignored,senders,stats,sender,set_sender,sender_changes,undo_sender_change,push,set_push,delete_push" {
		t.Fatalf("tools: %v", names)
	}
	send := out["result"].(map[string]any)["tools"].([]any)[0].(map[string]any)
	if props := send["inputSchema"].(map[string]any)["properties"].(map[string]any); props["format"] == nil || props["icon"] == nil || props["attachment"] == nil {
		t.Fatalf("send schema lacks format, icon or attachment: %v", props)
	}
	props := send["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"click", "expires", "reopen", "snooze_until", "tags"} {
		if props[name] == nil {
			t.Errorf("send schema lacks %s", name)
		}
	}
	_, out = rpc(t, h, "Bearer clrd_good", `{"jsonrpc":"2.0","id":3,"method":"nope"}`)
	if out["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("want method not found: %v", out)
	}
}

func TestTools(t *testing.T) {
	f := &fakeAPI{}
	h := New(f)
	cases := []struct {
		tool string
		args map[string]any
		call string
		body map[string]any
	}{
		{"send", map[string]any{"topic": "infra", "message": "Disk 91%", "title": "build", "priority": 4, "id": "disk-build"},
			"POST /api/v1/notifications", map[string]any{"topic": "infra", "message": "Disk 91%", "title": "build", "priority": float64(4), "external_id": "disk-build", "reopen": true}},
		{"send", map[string]any{"topic": "infra", "message": "*raw*", "format": "text"},
			"POST /api/v1/notifications", map[string]any{"topic": "infra", "message": "*raw*", "format": "text", "reopen": true}},
		{"send", map[string]any{"topic": "infra", "message": "m", "icon": "https://x.test/i.png", "attachment": map[string]any{"url": "https://x.test/r.pdf", "name": "r.pdf", "size": 9}},
			"POST /api/v1/notifications", map[string]any{"attachment": map[string]any{"name": "r.pdf", "url": "https://x.test/r.pdf"}, "icon": "https://x.test/i.png", "message": "m", "reopen": true, "topic": "infra"}},
		{"send", map[string]any{"topic": "infra", "message": "m", "id": "build", "reopen": false,
			"click": "https://x.test/build", "tags": []any{"build"}, "expires": "1h", "snooze_until": "2040-02-01T10:00:00Z",
			"actions": []any{map[string]any{"label": "Copy", "copy": "build"}}},
			"POST /api/v1/notifications", map[string]any{"topic": "infra", "message": "m", "external_id": "build", "reopen": false,
				"click": "https://x.test/build", "tags": []any{"build"}, "expires": "1h", "snooze_until": "2040-02-01T10:00:00Z",
				"actions": []any{map[string]any{"label": "Copy", "copy": "build"}}}},
		{"list", map[string]any{"topic": "infra"}, "GET /api/v1/notifications?state=open&topic=infra", nil},
		{"list", map[string]any{"state": "all"}, "GET /api/v1/notifications?state=all", nil},
		{"mark", map[string]any{"id": "01J", "mark": "actioned", "note": "filed relay#512"}, "POST /api/v1/notifications/01J/mark", map[string]any{"link": "", "mark": "actioned", "note": "filed relay#512"}},
		{"mark", map[string]any{"id": "01J"}, "DELETE /api/v1/notifications/01J/mark", nil},
		{"ack", map[string]any{"id": "01J"}, "POST /api/v1/notifications/01J/ack", map[string]any{"via": "mcp"}},
		{"resolve", map[string]any{"id": "01J"}, "POST /api/v1/notifications/01J/resolve", map[string]any{"via": "mcp"}},
		{"resolve", map[string]any{"topic": "infra", "external_id": "disk-build"}, "POST /infra/disk-build/resolve", map[string]any{"via": "mcp"}},
		{"snooze", map[string]any{"id": "01J", "for": "1h"}, "POST /api/v1/notifications/01J/snooze", map[string]any{"for": "1h", "via": "mcp"}},
		{"stats", map[string]any{"days": 30, "group": "topic"}, "GET /api/v1/stats?days=30&group=topic", nil},
		{"counts", map[string]any{"keep_cleared_days": 5}, "GET /api/v1/counts?keep_cleared_days=5", nil},
		{"counts", map[string]any{"keep_cleared_days": 5, "topic": "infra"}, "GET /api/v1/counts?keep_cleared_days=5&topic=infra", nil},
		{"counts", map[string]any{"keep_cleared_days": "default", "topic": "infra"}, "GET /api/v1/counts?keep_cleared_days=default&topic=infra", nil},
		{"settings", map[string]any{"topic_keep_cleared_days": map[string]any{"infra": 3, "ci": nil}}, "PUT /api/v1/settings", map[string]any{"topic_keep_cleared_days": map[string]any{"infra": float64(3), "ci": nil}}},
		{"format_check", map[string]any{"topic": "infra"}, "GET /api/v1/format-check?topic=infra", nil},
		{"format_check", map[string]any{"topic": "infra", "by": "sender"}, "GET /api/v1/format-check?by=sender&topic=infra", nil},
		{"format_dismiss", map[string]any{"topic": "infra", "rule": "no_title", "dismissed": false}, "POST /api/v1/format-check/dismiss", map[string]any{"topic": "infra", "rule": "no_title", "dismissed": false}},
		{"sound", nil, "GET /api/v1/sounds", nil},
		{"sound", map[string]any{"id": "d1", "sound": ""}, "PUT /api/v1/devices/d1", map[string]any{"sound": ""}}, // the server refuses it (#299)
		{"subscriptions", nil, "GET /api/v1/subscriptions", nil},
		{"subscriptions", map[string]any{"for": "u1", "topics": map[string]any{"chat": false}}, "PUT /api/v1/subscriptions?for=u1", map[string]any{"topics": map[string]any{"chat": false}}},
		{"request_approve", map[string]any{"approve": "allowed"}, "POST /api/v1/devices/approve-request", map[string]any{"approve": "allowed", "topic": ""}},
		{"set_sender", map[string]any{"id": "tk1", "routes": map[string]any{"*": []any{"infra"}}}, "PUT /api/v1/senders/tk1", map[string]any{"routes": map[string]any{"*": []any{"infra"}}}},
		{"undo_sender_change", map[string]any{"id": "tk1", "seq": 3}, "POST /api/v1/senders/tk1/changes/3/undo", map[string]any{}},
		{"settings", nil, "GET /api/v1/settings", nil},
		{"settings", map[string]any{"theme": "custom", "theme_colors": map[string]any{"bg": "#ffffff"}}, "PUT /api/v1/settings", map[string]any{"theme": "custom", "theme_colors": map[string]any{"bg": "#ffffff"}}},
		{"device_theme", map[string]any{"id": "d1", "theme": ""}, "PUT /api/v1/devices/d1", map[string]any{"theme": ""}},
		{"settings", map[string]any{"keep_cleared_days": 5}, "PUT /api/v1/settings", map[string]any{"keep_cleared_days": float64(5)}},
		{"push", nil, "GET /api/v1/push", nil},
		{"set_push", map[string]any{"endpoint": "https://example.test/sub", "keys": map[string]any{"p256dh": "example", "auth": "example"}},
			"PUT /api/v1/push", map[string]any{"endpoint": "https://example.test/sub", "keys": map[string]any{"p256dh": "example", "auth": "example"}}},
		{"delete_push", nil, "DELETE /api/v1/push", nil},
	}
	for _, c := range cases {
		text, isErr := callTool(t, h, c.tool, c.args)
		if isErr {
			t.Fatalf("%s: %s", c.tool, text)
		}
		call, body := f.last()
		if call != c.call {
			t.Errorf("%s: call %q, want %q", c.tool, call, c.call)
		}
		if c.body != nil {
			got, _ := json.Marshal(body)
			want, _ := json.Marshal(c.body)
			if string(got) != string(want) {
				t.Errorf("%s: body %s, want %s", c.tool, got, want)
			}
		}
		if f.auth != "Bearer clrd_good" {
			t.Errorf("%s: auth not forwarded: %q", c.tool, f.auth)
		}
	}
	if text, isErr := callTool(t, h, "send", map[string]any{"topic": "infra", "message": "m", "attachment": map[string]any{"name": "r.pdf"}}); !isErr || text != "attachment.url is required" {
		t.Errorf("attachment without url: %v %q", isErr, text)
	}
	if text, _ := callTool(t, h, "list", nil); !strings.Contains(text, `"items"`) {
		t.Errorf("list array not wrapped: %s", text)
	}
}

// #355: a whole number past 2^53 reaches the API with its exact digits.
// send passes devices through, so an agent can alert one phone (#82).
func TestSendDevices(t *testing.T) {
	f := &fakeAPI{}
	callTool(t, New(f), "send", map[string]any{"topic": "ci", "message": "m", "devices": []any{"phone"}})
	if _, b := f.last(); fmt.Sprint(b["devices"]) != "[phone]" {
		t.Fatalf("devices: %v", b)
	}
	// Not a list: passed on for the server to refuse, never dropped.
	callTool(t, New(f), "send", map[string]any{"topic": "ci", "message": "m", "devices": "phone"})
	if _, b := f.last(); b["devices"] != "phone" {
		t.Fatalf("devices string: %v", b)
	}
}

func TestBigNumberArgs(t *testing.T) {
	f := &fakeAPI{}
	h := New(f)
	big := json.Number("9007199254740993")
	callTool(t, h, "preview_template", map[string]any{"topic": "ci", "name": "github", "payload": map[string]any{"id": big}})
	if !strings.Contains(f.raw, `"id":9007199254740993`) {
		t.Fatalf("payload id lost precision: %s", f.raw)
	}
	callTool(t, h, "send", map[string]any{"topic": "ci", "message": "m", "priority": json.Number("5"), "repeat_times": json.Number("2")})
	if _, b := f.last(); b["priority"] != float64(5) || b["repeat_times"] != float64(2) {
		t.Fatalf("numbers after UseNumber: %v", b)
	}
	// Any other number goes as before: 5.0 reaches an int field as 5, 1e309 is refused.
	callTool(t, h, "settings", map[string]any{"keep_cleared_days": json.Number("5.0")})
	if !strings.Contains(f.raw, `"keep_cleared_days":5}`) {
		t.Fatalf("5.0 sent as: %s", f.raw)
	}
	code, out := rpc(t, h, "Bearer clrd_good", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send","arguments":{"topic":"ci","message":"m","priority":1e309}}}`)
	if code != 200 || out["error"] == nil {
		t.Fatalf("1e309: want invalid params, got %d %v", code, out)
	}
}

func TestMissingArgsAreToolErrors(t *testing.T) {
	h := New(&fakeAPI{})
	for tool, args := range map[string]map[string]any{
		"send": {"topic": "infra"}, "ack": {}, "mark": {"mark": "seen"}, "resolve": {"topic": "infra"}, "snooze": {"id": "01J"}, "wait_for_reply": {}, "unlink": {}, "bogus": {},
	} {
		if _, isErr := callTool(t, h, tool, args); !isErr {
			t.Errorf("%s: want isError", tool)
		}
	}
	// an id with a bad escape reaches the API escaped once, never panics building the request
	f := &fakeAPI{}
	callTool(t, New(f), "unlink", map[string]any{"id": "%zz"})
	if strings.Join(f.calls, ",") != "DELETE /api/v1/devices/%25zz" {
		t.Errorf("unlink %%zz: calls %v", f.calls)
	}
	// Every {x} in a REST tool's path is required and escaped, and never in the body.
	if text, isErr := callTool(t, New(f), "delete_hook", map[string]any{"topic": "ops"}); !isErr || text != "name is required" {
		t.Errorf("delete_hook without name: %v %q", isErr, text)
	}
	callTool(t, New(f), "set_template", map[string]any{"topic": "a/b", "name": "x", "message": "m"})
	if call, body := f.last(); call != "PUT /api/v1/topics/a%2Fb/templates/x" || fmt.Sprint(body) != "map[message:m]" {
		t.Errorf("set_template: %s %v", call, body)
	}
	callTool(t, New(f), "dismiss", map[string]any{"id": "01J"})
	if call, body := f.last(); call != "POST /api/v1/notifications/01J/dismiss" || fmt.Sprint(body) != "map[via:mcp]" {
		t.Errorf("dismiss: %s %v", call, body)
	}
}

func ev(seq int64, id, state string) server.Event {
	return server.Event{Seq: seq, Type: "state", Notification: &server.Notification{ID: id, State: state, StateBy: "pixel-9"}}
}

func TestWaitForReply(t *testing.T) {
	// The read says snoozed, but an ack landed after the call subscribed and
	// a snooze then overwrote it, before or after the read. Sampling the state
	// would miss the ack.
	evs := make(chan server.Event, 8)
	evs <- ev(4, "01K", "dismissed")    // another notification
	evs <- ev(5, "01J", "snoozed")      // still waiting
	evs <- ev(6, "01J", "acknowledged") // the reply, older than the read
	evs <- ev(7, "01J", "snoozed")
	f := &fakeAPI{state: "snoozed", subs: []chan server.Event{evs}}
	text, isErr := callTool(t, New(f), "wait_for_reply", map[string]any{"id": "01J", "timeout_s": 5})
	if isErr || !strings.Contains(text, `"reply":"acknowledged"`) || !strings.Contains(text, `"by":"pixel-9"`) {
		t.Fatalf("got %s", text)
	}

	// Snooze means "later": keep waiting, and the timeout shows the latest state.
	evs = make(chan server.Event, 1)
	evs <- ev(8, "01J", "snoozed")
	f = &fakeAPI{state: "active", subs: []chan server.Event{evs}}
	text, _ = callTool(t, New(f), "wait_for_reply", map[string]any{"id": "01J", "timeout_s": 0.05})
	if !strings.Contains(text, `"reply":"timeout"`) || !strings.Contains(text, `"state":"snoozed"`) {
		t.Fatalf("want timeout showing snoozed, got %s", text)
	}

	// Access gone by the deadline: a bare timeout, not the card (#428).
	evs = make(chan server.Event, 1)
	evs <- ev(8, "01J", "snoozed")
	f = &fakeAPI{state: "active", gets: 1, subs: []chan server.Event{evs}}
	text, _ = callTool(t, New(f), "wait_for_reply", map[string]any{"id": "01J", "timeout_s": 0.05})
	if text != `{"reply":"timeout"}` {
		t.Fatalf("lost access at the deadline got %s", text)
	}

	// A token revoked mid-wait gets an error, not the reply.
	evs = make(chan server.Event, 1)
	evs <- ev(9, "01J", "acknowledged")
	f = &fakeAPI{state: "active", gets: 1, subs: []chan server.Event{evs}}
	text, isErr = callTool(t, New(f), "wait_for_reply", map[string]any{"id": "01J", "timeout_s": 5})
	if !isErr || strings.Contains(text, "acknowledged") {
		t.Fatalf("revoked token got %s", text)
	}

	// The deadline hits during that re-read: a timeout, still without the reply.
	evs = make(chan server.Event, 1)
	evs <- ev(10, "01J", "acknowledged")
	f = &fakeAPI{state: "active", gets: 1, slow: true, subs: []chan server.Event{evs}}
	text, _ = callTool(t, New(f), "wait_for_reply", map[string]any{"id": "01J", "timeout_s": 0.05})
	if f.ngets != 2 || text != `{"reply":"timeout"}` {
		t.Fatalf("revoked token at the deadline (%d reads) got %s", f.ngets, text)
	}
}

func TestWaitForReplyRereadsAfterDrop(t *testing.T) {
	dropped := make(chan server.Event)
	close(dropped) // fell behind the stream
	f := &fakeAPI{state: "active", subs: []chan server.Event{dropped}}
	// The answer lands while the stream is dropped: only a fresh read sees it.
	f.onSub = func(n int) {
		if n == 2 {
			f.state = "dismissed"
		}
	}
	text, _ := callTool(t, New(f), "wait_for_reply", map[string]any{"id": "01J", "timeout_s": 5})
	if !strings.Contains(text, `"reply":"dismissed"`) {
		t.Fatalf("got %s", text)
	}
}

func TestProtocolVersionHeader(t *testing.T) {
	for v, want := range map[string]int{"": 200, "2025-06-18": 200, "2025-03-26": 200, "2024-11-05": 400, "1999-01-01": 400} {
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		req.Header.Set("Authorization", "Bearer clrd_good")
		if v != "" {
			req.Header.Set("MCP-Protocol-Version", v)
		}
		rec := httptest.NewRecorder()
		New(&fakeAPI{}).ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("version %q: got %d, want %d", v, rec.Code, want)
		}
	}
}
