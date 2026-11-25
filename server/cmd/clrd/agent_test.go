package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/thejdubb02/clrd/server"
)

// clrd agent invite (#454) asks the running server and prints both forms.
func TestAgentInviteCmd(t *testing.T) {
	_, ts, admin := realServer(t)
	t.Setenv("CLRD_URL", ts.URL)
	t.Setenv("CLRD_TOKEN", admin)
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := agentCmd(server.Options{}, []string{"invite", "--name", "ci", "--topics", "ops"})
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	lines := strings.Fields(string(out))
	if err != nil || len(lines) != 2 || !strings.HasPrefix(lines[0], "clrd://agent?") || !strings.HasPrefix(lines[1], ts.URL+"/agent#") {
		t.Fatalf("agent invite: %v %q", err, out)
	}
	t.Setenv("CLRD_TOKEN", "clrd_wrong")
	if err := agentCmd(server.Options{}, []string{"invite", "--name", "ci2", "--topics", "ops"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("bad token: %v", err)
	}
}
