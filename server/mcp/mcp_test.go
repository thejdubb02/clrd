package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI records every call and answers like the REST API in docs/SPEC.md.
type fakeAPI struct {
	mu    sync.Mutex
	calls []string
	body  map[string]any
	auth  string
	state string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	f.auth = r.Header.Get("Authorization")
	f.body = nil
	_ = json.NewDecoder(r.Body).Decode(&f.body)
	if r.Header.Get("Authorization") != "Bearer clrd_good" {
		w.WriteHeader(403)
		io.WriteString(w, `{"error":"token lacks scope","code":"forbidden"}`)
		return
	}
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/notifications/") {
		json.NewEncoder(w).Encode(map[string]any{"id": "01J", "state": f.state, "state_by": "pixel-9"})
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
	_, out := rpc(t, h, "Bearer x", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if out["result"].(map[string]any)["protocolVersion"] != "2025-03-26" {
		t.Fatalf("version not echoed: %v", out)
	}
	code, _ := rpc(t, h, "Bearer x", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if code != 202 {
		t.Fatalf("notification: want 202, got %d", code)
	}
	_, out = rpc(t, h, "Bearer x", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var names []string
	for _, tl := range out["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "send,list,ack,resolve,snooze,wait_for_reply" {
		t.Fatalf("tools: %v", names)
	}
	_, out = rpc(t, h, "Bearer x", `{"jsonrpc":"2.0","id":3,"method":"nope"}`)
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
			"POST /api/v1/notifications", map[string]any{"topic": "infra", "message": "Disk 91%", "title": "build", "priority": float64(4), "external_id": "disk-build"}},
		{"list", map[string]any{"topic": "infra"}, "GET /api/v1/notifications?state=open&topic=infra", nil},
		{"list", map[string]any{"state": "all"}, "GET /api/v1/notifications?state=all", nil},
		{"ack", map[string]any{"id": "01J"}, "POST /api/v1/notifications/01J/ack", nil},
		{"resolve", map[string]any{"id": "01J"}, "POST /api/v1/notifications/01J/resolve", nil},
		{"resolve", map[string]any{"topic": "infra", "external_id": "disk-build"}, "POST /infra/disk-build/resolve", nil},
		{"snooze", map[string]any{"id": "01J", "for": "1h"}, "POST /api/v1/notifications/01J/snooze", map[string]any{"for": "1h"}},
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
	if text, _ := callTool(t, h, "list", nil); !strings.Contains(text, `"items"`) {
		t.Errorf("list array not wrapped: %s", text)
	}
}

func TestMissingArgsAreToolErrors(t *testing.T) {
	h := New(&fakeAPI{})
	for tool, args := range map[string]map[string]any{
		"send": {"topic": "infra"}, "ack": {}, "resolve": {"topic": "infra"}, "snooze": {"id": "01J"}, "wait_for_reply": {}, "bogus": {},
	} {
		if _, isErr := callTool(t, h, tool, args); !isErr {
			t.Errorf("%s: want isError", tool)
		}
	}
}

func TestWaitForReply(t *testing.T) {
	PollEvery = 10 * time.Millisecond
	f := &fakeAPI{state: "active"}
	h := New(f)
	go func() {
		time.Sleep(50 * time.Millisecond)
		f.mu.Lock()
		f.state = "acknowledged"
		f.mu.Unlock()
	}()
	text, isErr := callTool(t, h, "wait_for_reply", map[string]any{"id": "01J", "timeout_s": 5})
	if isErr || !strings.Contains(text, `"reply":"acknowledged"`) || !strings.Contains(text, `"by":"pixel-9"`) {
		t.Fatalf("got %s", text)
	}
	f.state = "active"
	text, _ = callTool(t, h, "wait_for_reply", map[string]any{"id": "01J", "timeout_s": 0.05})
	if !strings.Contains(text, `"reply":"timeout"`) {
		t.Fatalf("want timeout, got %s", text)
	}
}
