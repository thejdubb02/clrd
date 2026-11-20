package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	_, raw, err := s.CreateToken(context.Background(), "agent", []string{"read", "publish"}, nil)
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

// Methods that never reach the API still need a real token.
func TestTokenCheckedUpFront(t *testing.T) {
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, pub, err := s.CreateToken(context.Background(), "sender", []string{"publish"}, nil)
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
	_, raw, err := s.CreateToken(context.Background(), "agent", []string{"read", "publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	phone, _, err := s.CreateToken(context.Background(), "pixel-9", []string{"read"}, nil)
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
			map[string]any{"label": "Yes", "reply": "yes"}, map[string]any{"label": "No", "reply": "no"}}})
	id, _ := sent["id"].(string)
	if acts, _ := sent["actions"].([]any); len(acts) != 2 {
		t.Fatalf("send dropped the buttons: %v", sent)
	}
	if att, _ := sent["attachment"].(map[string]any); sent["format"] != "text" || sent["icon"] != "https://x.test/i.png" || att["url"] != "https://x.test/r.pdf" || att["name"] != "r.pdf" {
		t.Fatalf("send dropped format, icon or attachment: %v", sent)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Reply(context.Background(), phone, id, "yes", 0)
	}()
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 5}); got["reply"] != "acknowledged" || got["answer"] != "yes" || got["by"] != "pixel-9" {
		t.Fatalf("wait_for_reply: %v", got)
	}
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 5}); got["answer"] != "yes" {
		t.Fatalf("second wait_for_reply: %v", got)
	}
	// Once the sender resolves it, by is the sender, so no answer rides along.
	call("resolve", map[string]any{"topic": "agents", "external_id": "deploy-2"})
	if got := call("wait_for_reply", map[string]any{"id": id, "timeout_s": 5}); got["reply"] != "resolved" || got["answer"] != nil {
		t.Fatalf("wait after resolve: %v", got)
	}
}
