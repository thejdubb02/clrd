package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thejdubb02/clrd/server"
)

// TestAgainstRealServer runs the tools through /mcp mounted on the real server,
// the way cmd/clrd wires it, so a drift between this package and the REST API
// fails here and not in production.
func TestAgainstRealServer(t *testing.T) {
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Mount("/mcp", New(s))
	_, raw, err := s.CreateToken(context.Background(), "agent", "allowed", []string{"read", "publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		code, out := rpc(t, s, "Bearer "+raw, string(b))
		res, _ := out["result"].(map[string]any)
		if code != 200 || res == nil || res["isError"] == true {
			t.Fatalf("%s: http %d %v", name, code, out)
		}
		var v map[string]any
		json.Unmarshal([]byte(res["content"].([]any)[0].(map[string]any)["text"].(string)), &v)
		return v
	}

	sent := call("send", map[string]any{"topic": "agents", "message": "deploy?", "id": "deploy-1"})
	id, _ := sent["id"].(string)
	if id == "" || sent["external_id"] != "deploy-1" || sent["state"] != "active" {
		t.Fatalf("send: %v", sent)
	}
	list := call("list", map[string]any{"topic": "agents"})
	if b, _ := json.Marshal(list); !strings.Contains(string(b), id) {
		t.Fatalf("list misses %s: %s", id, b)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		call("ack", map[string]any{"id": id})
	}()
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 5}); got["reply"] != "acknowledged" || got["by"] != "agent" {
		t.Fatalf("wait_for_reply: %v", got)
	}
	// Reusing an acknowledged key for a new question must not inherit the old answer.
	if again := call("send", map[string]any{"topic": "agents", "message": "deploy again?", "id": "deploy-1"}); again["id"] != id || again["state"] != "active" {
		t.Fatalf("resend: %v", again)
	}
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 0.05}); got["reply"] != "timeout" {
		t.Fatalf("wait after resend: %v", got)
	}
	if got := call("resolve", map[string]any{"topic": "agents", "external_id": "deploy-1"}); got["state"] != "resolved" {
		t.Fatalf("resolve by key: %v", got)
	}
}

// TestSetupToolsAgainstRealServer runs every setup tool against the real REST
// API, so a wrong path or body shape fails here.
func TestSetupToolsAgainstRealServer(t *testing.T) {
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Mount("/mcp", New(s))
	_, admin, err := s.CreateToken(context.Background(), "agent", "allowed", []string{"read", "publish", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, pub, err := s.CreateToken(context.Background(), "sender", "allowed", []string{"read", "publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tool := func(token, name string, args map[string]any) (string, bool) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		code, out := rpc(t, s, "Bearer "+token, string(b))
		res, _ := out["result"].(map[string]any)
		if code != 200 || res == nil {
			t.Fatalf("%s: http %d %v", name, code, out)
		}
		return res["content"].([]any)[0].(map[string]any)["text"].(string), res["isError"] == true
	}
	ok := func(name string, args map[string]any, want ...string) map[string]any {
		t.Helper()
		text, isErr := tool(admin, name, args)
		if isErr {
			t.Fatalf("%s: %s", name, text)
		}
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Fatalf("%s: %s lacks %s", name, text, w)
			}
		}
		var v map[string]any
		json.Unmarshal([]byte(text), &v)
		return v
	}

	a := ok("send", map[string]any{"topic": "agents", "message": "a"})
	ok("dismiss", map[string]any{"id": a["id"]}, `"state":"dismissed"`)
	ok("reopen", map[string]any{"id": a["id"]}, `"state":"active"`)
	ok("send", map[string]any{"topic": "agents", "message": "b"})
	ok("clear", map[string]any{"topic": "agents"}, `"cleared":2`)
	ok("send", map[string]any{"topic": "agents", "message": "c", "live": true}, `"live":true`) // stores nothing (#400)

	sl := ok("silence", map[string]any{"topic": "agents", "for": "2h", "comment": "deploy"}, `"comment":"deploy"`)
	ok("silences", nil, sl["id"].(string))
	ok("end_silence", map[string]any{"id": sl["id"]})
	if text := fmt.Sprint(ok("silences", nil)); strings.Contains(text, sl["id"].(string)) {
		t.Fatalf("silence not ended: %s", text)
	}

	ok("set_topic", map[string]any{"topic": "backups", "checkin": "25h"}, `"checkin":"25h"`)
	ok("set_topic", map[string]any{"topic": "home", "device_actions": true}, `"device_actions":true`) // #401
	ok("send", map[string]any{"topic": "home", "message": "lights", "actions": []any{map[string]any{"label": "Off",
		"http": map[string]any{"url": "https://example.com/off"}}}}, `"method":"POST"`)

	ok("set_template", map[string]any{"topic": "ci", "name": "build", "title": "{{.repo}}", "id": "{{.run}}"}, `"id":"{{.run}}"`)
	ok("templates", map[string]any{"topic": "ci"}, `"title":"{{.repo}}"`, `"builtin"`)
	ok("delete_template", map[string]any{"topic": "ci", "name": "build"}, `"deleted":true`)

	ok("set_hook", map[string]any{"topic": "ops", "name": "audit", "url": "https://example.com/h", "events": []string{"acknowledged"}, "secret": "s"}, `"has_secret":true`)
	ok("hooks", map[string]any{"topic": "ops"}, `"name":"audit"`)
	ok("preview_hook", map[string]any{"topic": "ops", "body": `{"t": {{json .notification.topic}}}`}, `"body":"{\"t\": \"ops\"}"`)
	ok("set_hook_allow", map[string]any{"networks": []string{"192.168.1.7/24"}, "ports": []int{8080}}, `"networks":["192.168.1.0/24"]`, `"ports":[8080]`)
	ok("hook_allow", map[string]any{}, `"networks":["192.168.1.0/24"]`)
	ok("hook_deliveries", map[string]any{"topic": "ops", "name": "audit"}, `"deliveries"`)
	ok("delete_hook", map[string]any{"topic": "ops", "name": "audit"}, `"deleted":true`)

	cam := ok("create_token", map[string]any{"name": "cam", "scopes": []string{"read", "publish"},
		"grants": []any{map[string]any{"topic": "home-*", "scopes": []string{"publish"}}}}, `"topic":"home-*"`, `"token":"clrd_`, `"approve":"denied"`)
	ok("tokens", nil, `"name":"cam"`)
	// No admin agent makes a token that may approve through MCP (#187).
	_, dadmin, err := s.CreateToken(context.Background(), "denied agent", "denied", []string{"read", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if text, isErr := tool(dadmin, "create_token", map[string]any{"name": "me too", "scopes": []string{"read"}, "approve": "allowed"}); !isErr || !strings.Contains(text, "400") {
		t.Fatalf("denied admin made an allowed token: %v %s", isErr, text)
	}
	ok("devices", nil, `"approve":"allowed"`)
	ok("revoke_token", map[string]any{"id": cam["id"]})
	if text, isErr := tool(cam["token"].(string), "topics", nil); !isErr || !strings.Contains(text, "401") {
		t.Fatalf("revoked token still works: %v %s", isErr, text)
	}

	ok("server_env", nil, `"CLRD_RETENTION":"720h0m0s"`)
	ok("ntfy_ignored", nil, `"ignored"`)

	// Errors say what to fix: a missing path argument, and the API's own message.
	if text, isErr := tool(admin, "set_hook", map[string]any{"topic": "ops", "url": "https://example.com/h"}); !isErr || text != "name is required" {
		t.Fatalf("set_hook without name: %v %q", isErr, text)
	}
	if text, isErr := tool(admin, "set_hook", map[string]any{"topic": "ops", "name": "x", "url": "ftp://x"}); !isErr || !strings.Contains(text, "url must be an http(s) URL") {
		t.Fatalf("set_hook bad url: %v %q", isErr, text)
	}
	// Admin tools stay admin: the API refuses a token without the scope.
	for _, name := range []string{"tokens", "server_env", "hooks"} {
		if text, isErr := tool(pub, name, map[string]any{"topic": "ops"}); !isErr || !strings.Contains(text, "403") {
			t.Fatalf("%s as non-admin: %v %q", name, isErr, text)
		}
	}
}

// TestClientSession drives /mcp over real HTTP the way an MCP client does:
// initialize, the initialized notification, tools/list, then tool calls with
// the negotiated MCP-Protocol-Version header on every request.
func TestClientSession(t *testing.T) {
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Mount("/mcp", New(s))
	ts := httptest.NewServer(s)
	defer ts.Close()
	_, agent, err := s.CreateToken(context.Background(), "agent", "allowed", []string{"read", "publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.CreateToken(context.Background(), "pixel-9", "allowed", []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	post := func(token string, msg map[string]any) (int, map[string]any) {
		t.Helper()
		msg["jsonrpc"] = "2.0"
		b, _ := json.Marshal(msg)
		req, _ := http.NewRequest("POST", ts.URL+"/mcp", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if version != "" {
			req.Header.Set("MCP-Protocol-Version", version)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	tool := func(token, name string, args map[string]any) (map[string]any, string) {
		t.Helper()
		code, out := post(token, map[string]any{"id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		res, _ := out["result"].(map[string]any)
		if code != 200 || res == nil {
			t.Fatalf("%s: http %d %v", name, code, out)
		}
		text := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if res["isError"] == true {
			return nil, text
		}
		var v map[string]any
		json.Unmarshal([]byte(text), &v)
		return v, ""
	}

	code, out := post(agent, map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}})
	res, _ := out["result"].(map[string]any)
	if code != 200 || res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %d %v", code, out)
	}
	version = "2025-06-18"
	if code, _ := post(agent, map[string]any{"method": "notifications/initialized"}); code != 202 {
		t.Fatalf("initialized: %d", code)
	}
	_, out = post(agent, map[string]any{"id": 2, "method": "tools/list"})
	listed := map[string]bool{}
	for _, tl := range out["result"].(map[string]any)["tools"].([]any) {
		listed[tl.(map[string]any)["name"].(string)] = true
	}
	for _, n := range []string{"send", "list", "topics", "get", "update", "ack", "resolve", "snooze", "wait_for_reply"} {
		if !listed[n] {
			t.Errorf("tools/list lacks %s", n)
		}
	}

	sent, _ := tool(agent, "send", map[string]any{"topic": "build", "message": "step 1 of 3", "title": "Release"})
	id, _ := sent["id"].(string)
	tool(agent, "ack", map[string]any{"id": id})
	if got, msg := tool(agent, "update", map[string]any{"id": id, "message": "step 2 of 3"}); msg != "" || got["message"] != "step 2 of 3" || got["state"] != "acknowledged" || got["title"] != "Release" {
		t.Fatalf("update must change only the message: %v %s", got, msg)
	}
	if got, _ := tool(agent, "get", map[string]any{"id": id}); got["id"] != id || got["message"] != "step 2 of 3" || got["state"] != "acknowledged" {
		t.Fatalf("get: %v", got)
	}
	if got, _ := tool(agent, "topics", nil); !strings.HasPrefix(fmt.Sprint(got["topics"]), "[map[approvals:false icon: last_at:") || !strings.HasSuffix(fmt.Sprint(got["topics"]), " open:1 subscribed:true topic:build]]") {
		t.Fatalf("topics: %v", got)
	}
	// The format check's fix prompt points at this server, not example.com (#308).
	tool(agent, "send", map[string]any{"topic": "untitled", "message": "x"})
	if got, _ := tool(agent, "format_check", map[string]any{"topic": "untitled"}); !strings.Contains(fmt.Sprint(got["topics"]), ts.URL+"/api/v1/notifications") {
		t.Fatalf("format_check prompt: %v", got)
	}
	if _, msg := tool(reader, "update", map[string]any{"id": id, "message": "hijacked"}); !strings.Contains(msg, "403") {
		t.Fatalf("a read-only token edited a message: %q", msg)
	}
	if _, msg := tool(agent, "update", map[string]any{"id": id}); msg != "message is required" {
		t.Fatalf("update without a message: %q", msg)
	}
	tool(agent, "resolve", map[string]any{"id": id})
	if _, msg := tool(agent, "update", map[string]any{"id": id, "message": "too late"}); !strings.Contains(msg, "409") {
		t.Fatalf("update after resolve: %q", msg)
	}
	// A question with buttons keeps its text: a tap answers what was shown.
	ask, _ := tool(agent, "send", map[string]any{"topic": "build", "message": "Allow rm?", "actions": []any{map[string]any{"label": "Allow", "reply": "allow"}}})
	if _, msg := tool(agent, "update", map[string]any{"id": ask["id"], "message": "Allow ls?"}); !strings.Contains(msg, "answer buttons") {
		t.Fatalf("update rewrote a prompt under its buttons: %q", msg)
	}
	// A link button is not an answer, so the progress line stays editable.
	link, _ := tool(agent, "send", map[string]any{"topic": "build", "message": "step 1", "actions": []any{map[string]any{"label": "Logs", "url": "https://x.test/l"}}})
	if got, msg := tool(agent, "update", map[string]any{"id": link["id"], "message": "step 2"}); msg != "" || got["message"] != "step 2" {
		t.Fatalf("update with a link button: %v %q", got, msg)
	}
	if _, msg := tool(agent, "get", map[string]any{"id": "nope"}); !strings.Contains(msg, "404") {
		t.Fatalf("get unknown id: %q", msg)
	}
}

// Methods that never reach the API still need a real token.
func TestTokenCheckedUpFront(t *testing.T) {
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, pub, err := s.CreateToken(context.Background(), "sender", "allowed", []string{"publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(s)
	for _, m := range []string{"initialize", "ping", "tools/list"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + m + `"}`
		if code, _ := rpc(t, h, "Bearer clrd_made_up", body); code != 401 {
			t.Errorf("%s with a made-up token: want 401, got %d", m, code)
		}
		if code, out := rpc(t, h, "Bearer "+pub, body); code != 200 || out["result"] == nil {
			t.Errorf("%s with a publish-only token: got %d %v", m, code, out)
		}
	}
	note := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	if code, _ := rpc(t, h, "Bearer clrd_made_up", note); code != 401 {
		t.Errorf("notification with a made-up token: want 401, got %d", code)
	}
	if code, _ := rpc(t, h, "Bearer "+pub, note); code != 202 {
		t.Errorf("notification with a publish-only token: want 202, got %d", code)
	}
}

// A tapped reply button reaches the agent as answer, live and on a later call.
func TestReplyButtons(t *testing.T) {
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, raw, err := s.CreateToken(context.Background(), "agent", "allowed", []string{"read", "publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	phone, _, err := s.CreateToken(context.Background(), "pixel-9", "allowed", []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(s)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		code, out := rpc(t, h, "Bearer "+raw, string(b))
		res, _ := out["result"].(map[string]any)
		if code != 200 || res == nil || res["isError"] == true {
			t.Fatalf("%s: http %d %v", name, code, out)
		}
		var v map[string]any
		json.Unmarshal([]byte(res["content"].([]any)[0].(map[string]any)["text"].(string)), &v)
		return v
	}
	sent := call("send", map[string]any{"topic": "agents", "message": "deploy?", "id": "deploy-2", "format": "text", "icon": "https://x.test/i.png",
		"attachment": map[string]any{"url": "https://x.test/r.pdf", "name": "r.pdf"}, "actions": []any{
			map[string]any{"label": "Yes", "reply": "yes"}, map[string]any{"label": "Other", "reply": "other", "text": true}}})
	id, _ := sent["id"].(string)
	if acts, _ := sent["actions"].([]any); len(acts) != 2 {
		t.Fatalf("send dropped the buttons: %v", sent)
	}
	if att, _ := sent["attachment"].(map[string]any); sent["format"] != "text" || sent["icon"] != "https://x.test/i.png" || att["url"] != "https://x.test/r.pdf" || att["name"] != "r.pdf" {
		t.Fatalf("send dropped format, icon or attachment: %v", sent)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Reply(context.Background(), phone, id, "other", "next week", "app", 0)
	}()
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 5}); got["reply"] != "acknowledged" || got["answer"] != "other" || got["text"] != "next week" || got["by"] != "pixel-9" {
		t.Fatalf("wait_for_reply: %v", got)
	}
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 5}); got["answer"] != "other" || got["text"] != "next week" {
		t.Fatalf("second wait_for_reply: %v", got)
	}
	// Once the sender resolves it, by is the sender, so no answer rides along.
	call("resolve", map[string]any{"topic": "agents", "external_id": "deploy-2"})
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 5}); got["reply"] != "resolved" || got["answer"] != nil || got["text"] != nil {
		t.Fatalf("wait after resolve: %v", got)
	}
}

// /mcp/approve answers a permission hook from a tap: Allow allows, anything
// else denies, and a timeout denies and clears the phones.
func TestApproveSummary(t *testing.T) {
	for in, want := range map[string]string{
		`{"command":"rm -rf build","description":"clean"}`:                                                          "rm -rf build\ndescription: clean",
		`{"command":"rm *","workdir":"/srv","timeout":5,"sandbox":null,"env":{"A":"1 && b"}}`:                       "rm *\nenv: {\"A\":\"1 && b\"}\ntimeout: 5\nworkdir: /srv", // #441: Allow approves every field
		`{"description":"read","scope":"all\nworkdir: /safe"}`:                                                      "read\nscope: \"all\\nworkdir: /safe\"",                    // a line break cannot fake a field
		`{"command":"ls\nworkdir: /safe","workdir":"/srv"}`:                                                         "\"ls\\nworkdir: /safe\"\nworkdir: /srv",
		`{"description":"x","a: b":"c","n":"1\u2028m\u0085"}`:                                                       "x\n\"a: b\": c\nn: \"1\\u2028m\\u0085\"", // a colon or Unicode line break cannot fake a field
		`{"command":"x","big":1e400}`:                                                                               "x\nbig: 1e400",
		`{"file_path":"/a/x.go","content":"y","scope":"all"}`:                                                       "/a/x.go\nscope: all\ny",
		`{"file_path":"/a/x.go","old_string":"a := 1\nb := 2","new_string":"a := 3"}`:                               "/a/x.go\n- a := 1\n- b := 2\n+ a := 3",
		`{"file_path":"/a/x.go","old_string":"a","new_string":"","replace_all":true}`:                               "/a/x.go\n(every match)\n- a\n+ ",
		`{"file_path":"/a/x.go","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d"}]}`: "/a/x.go\n- a\n+ b\n- c\n+ d",
		`{"file_path":"/a/new.sh","content":"#!/bin/sh\nrm -rf ~\n"}`:                                               "/a/new.sh\n#!/bin/sh\nrm -rf ~\n",
		`{"notebook_path":"/a/n.ipynb","new_source":"print(1)"}`:                                                    "/a/n.ipynb\n(replace cell)\nprint(1)",
		`{"notebook_path":"/a/n.ipynb","cell_id":"c7","edit_mode":"delete","new_source":"x"}`:                       "/a/n.ipynb\n(delete cell c7)",
		`{"notebook_path":"/a/n.ipynb","cell_id":"c7","edit_mode":"insert","cell_type":"code","new_source":"x"}`:    "/a/n.ipynb\n(insert cell c7 code)\nx",
		`{"file_path":"/a/x\n- fake","content":"y"}`:                                                                `"/a/x\n- fake"` + "\ny",
		`{"file_path":"/a/blank","content":"  "}`:                                                                   "/a/blank\n  ",
		`{"file_path":"/a/empty","content":""}`:                                                                     "/a/empty\n(empty)",
		`{"file_path":"/a/x.go"}`:                                                                                   "/a/x.go",
		`{"query":"clrd"}`:                                                                                          `{"query":"clrd"}`,
	} {
		if got := summary(json.RawMessage(in)); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestApproveHook(t *testing.T) {
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, raw, err := s.CreateToken(context.Background(), "laptop", "allowed", []string{"read", "publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	phone, _, err := s.CreateToken(context.Background(), "pixel-9", "allowed", []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(s)
	body := `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"},"cwd":"/home/me/app"}`
	ask := func(auth, query string, tap string) (int, map[string]any) {
		t.Helper()
		if tap != "" {
			go func() {
				for i := 0; i < 200; i++ {
					time.Sleep(10 * time.Millisecond)
					open, _ := s.List(context.Background(), phone, server.ListQuery{Topics: []string{"agents"}, State: "active"})
					if len(open) > 0 {
						s.Reply(context.Background(), phone, open[0].ID, tap, "", "app", 0)
						return
					}
				}
			}()
		}
		req := httptest.NewRequest("POST", "/mcp/approve"+query, strings.NewReader(body))
		req.Header.Set("Authorization", auth)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		hso, _ := out["hookSpecificOutput"].(map[string]any)
		dec, _ := hso["decision"].(map[string]any)
		return rec.Code, dec
	}
	if code, dec := ask("Bearer "+raw, "", "allow"); code != 200 || dec["behavior"] != "allow" {
		t.Fatalf("allow tap: %d %v", code, dec)
	}
	if code, dec := ask("Bearer "+raw, "", "deny"); code != 200 || dec["behavior"] != "deny" || !strings.Contains(fmt.Sprint(dec["message"]), "pixel-9") {
		t.Fatalf("deny tap: %d %v", code, dec)
	}
	t0 := time.Now()
	if code, dec := ask("Bearer "+raw, "?timeout=0.1", ""); code != 200 || dec["behavior"] != "deny" || time.Since(t0) > 10*time.Second {
		t.Fatalf("timeout: %d %v after %v", code, dec, time.Since(t0))
	}
	if open, _ := s.List(context.Background(), phone, server.ListQuery{Topics: []string{"agents"}, State: "active"}); len(open) != 0 {
		t.Fatalf("timed-out ask still rings: %v", open)
	}
	all, _ := s.List(context.Background(), phone, server.ListQuery{Topics: []string{"agents"}, State: "all"})
	if len(all) != 3 || all[0].Message != "rm -rf build" || !strings.Contains(all[0].Title, "Bash") || !strings.Contains(all[0].Title, "(app)") {
		t.Fatalf("notifications: %+v", all)
	}
	if code, dec := ask("Bearer clrd_wrong", "", ""); code != 502 || dec != nil {
		t.Fatalf("bad token must fail, not answer: %d %v", code, dec)
	}
	body = `{"tool_name":"Bash","tool_input":{"command":"echo ok; ` + strings.Repeat("x", 4000) + `"}}`
	if code, dec := ask("Bearer "+raw, "", "allow"); code != 413 || dec != nil {
		t.Fatalf("too long to show must go back to the agent: %d %v", code, dec)
	}
}

// TestWaitLocalClear: the caller's own local clear (#767) is its reply,
// whether it came before the wait or during it, as get shows it; another
// device's never is (#777).
func TestWaitLocalClear(t *testing.T) {
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	agent, raw, err := s.CreateToken(ctx, "agent", "allowed", []string{"read", "publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	phone, _, err := s.CreateToken(ctx, "pixel-9", "allowed", []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []*server.Token{agent, phone} {
		if _, err := s.SetClears(ctx, tok, tok.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	h := New(s)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		code, out := rpc(t, h, "Bearer "+raw, string(b))
		res, _ := out["result"].(map[string]any)
		if code != 200 || res == nil || res["isError"] == true {
			t.Fatalf("%s: http %d %v", name, code, out)
		}
		var v map[string]any
		json.Unmarshal([]byte(res["content"].([]any)[0].(map[string]any)["text"].(string)), &v)
		return v
	}
	id, _ := call("send", map[string]any{"topic": "agents", "message": "deploy?"})["id"].(string)
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Act(ctx, phone, id, "dismiss", "app", nil)
		time.Sleep(50 * time.Millisecond)
		s.Act(ctx, agent, id, "dismiss", "app", nil)
	}()
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 5}); got["reply"] != "dismissed" || got["by"] != "agent" {
		t.Fatalf("wait during own local clear: %v", got)
	}
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 5}); got["reply"] != "dismissed" {
		t.Fatalf("wait after own local clear: %v", got)
	}
	// Shown again at once: the clear still counts, from its event.
	id3, _ := call("send", map[string]any{"topic": "agents", "message": "undo?"})["id"].(string)
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Act(ctx, agent, id3, "dismiss", "app", nil)
		s.Act(ctx, agent, id3, "reopen", "app", nil)
	}()
	if got := call("wait_for_reply", map[string]any{"id": id3, "timeout_s": 5}); got["reply"] != "dismissed" {
		t.Fatalf("wait over own clear and reopen: %v", got)
	}
	id2, _ := call("send", map[string]any{"topic": "agents", "message": "again?"})["id"].(string)
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Act(ctx, phone, id2, "dismiss", "app", nil)
	}()
	if got := call("wait_for_reply", map[string]any{"id": id2, "timeout_s": 0.3}); got["reply"] != "timeout" {
		t.Fatalf("another device's local clear ended the wait: %v", got)
	}
}
