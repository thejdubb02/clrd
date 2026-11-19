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
	PollEvery = 10 * time.Millisecond
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
