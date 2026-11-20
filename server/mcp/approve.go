package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// ApproveWait is how long /mcp/approve waits for a tap by default: just under
// the 600 s that Claude Code and Codex give a permission hook, so clrd answers
// deny before the agent gives up on it.
var ApproveWait = 590 * time.Second

// hookInput is the part of a Claude Code or Codex PermissionRequest hook body
// that clrd shows on the phone.
type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Cwd       string          `json:"cwd"`
}

// approve answers a coding agent's permission prompt from a phone. It takes
// the hook's JSON body as sent, publishes a notification with Allow and Deny
// buttons, waits for a tap, and answers in the hook's own output format.
// Anything but an Allow tap is a deny, including a timeout. If the send itself
// fails it answers with that error status, and the agent falls back to its own
// prompt on screen.
func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	var in hookInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, `{"error":"body must be the hook's JSON input","code":"bad_request"}`, http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	topic := q.Get("topic")
	if topic == "" {
		topic = "agents"
	}
	d := ApproveWait
	if v, err := strconv.ParseFloat(q.Get("timeout"), 64); err == nil && v > 0 {
		d = time.Duration(v * float64(time.Second))
	}
	tool := in.ToolName
	if tool == "" {
		tool = "a tool"
	}
	title := "Allow " + tool + "?"
	if in.Cwd != "" {
		title += " (" + path.Base(in.Cwd) + ")"
	}
	sent, err := h.api(r, "POST", "/api/v1/notifications", map[string]any{
		"topic": topic, "title": title, "message": summary(in.ToolInput), "priority": 4,
		"actions": []map[string]any{{"label": "Allow", "reply": "allow"}, {"label": "Deny", "reply": "deny"}},
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "code": "send_failed"})
		return
	}
	id := url.PathEscape(str(sent, "id"))
	res, err := h.wait(r, id, map[string]any{"timeout_s": d.Seconds()})
	if r.Context().Err() != nil { // the agent hung up: clear the phones and stop
		h.api(r.WithContext(context.WithoutCancel(r.Context())), "POST", "/api/v1/notifications/"+id+"/resolve", nil)
		return
	}
	reason := "Denied from clrd."
	m, _ := res.(map[string]any)
	switch {
	case err != nil:
		reason = "clrd could not wait for an answer: " + err.Error()
	case str(m, "answer") == "allow":
		hookAnswer(w, "allow", "")
		return
	case str(m, "reply") == "timeout":
		reason = fmt.Sprintf("No answer on clrd within %d s.", int(d.Seconds()))
	case str(m, "by") != "":
		reason = "Denied from clrd by " + str(m, "by") + "."
	}
	if str(m, "reply") == "timeout" || err != nil { // still ringing: clear it everywhere
		h.api(r, "POST", "/api/v1/notifications/"+id+"/resolve", nil)
	}
	hookAnswer(w, "deny", reason)
}

// hookAnswer writes the PermissionRequest output both Claude Code and Codex read.
func hookAnswer(w http.ResponseWriter, behavior, message string) {
	dec := map[string]any{"behavior": behavior}
	if message != "" {
		dec["message"] = message
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest", "decision": dec}})
}

// summary is what the phone shows: the command, else a description, else the
// raw input, cut to fit a notification.
func summary(raw json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	s := ""
	for _, k := range []string{"command", "file_path", "url", "description"} {
		if v, ok := in[k].(string); ok && v != "" {
			s = v
			break
		}
	}
	if s == "" {
		s = strings.TrimSpace(string(raw))
	}
	if s == "" {
		s = "(no details)"
	}
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500]) + "..."
	}
	return s
}
