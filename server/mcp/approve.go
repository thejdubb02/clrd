package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
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
	if v, err := strconv.ParseFloat(q.Get("timeout"), 64); err == nil && v > 0 && v < ApproveWait.Seconds() {
		d = time.Duration(v * float64(time.Second)) // never past ApproveWait, or the agent gives up first
	}
	tool := in.ToolName
	if tool == "" {
		tool = "a tool"
	}
	title := "Allow " + tool + "?"
	if in.Cwd != "" {
		title += " (" + path.Base(in.Cwd) + ")"
	}
	msg := summary(in.ToolInput)
	if len([]rune(msg)) > maxShown {
		// Allow would approve text the phone never showed: send it back to the agent's own prompt.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		json.NewEncoder(w).Encode(map[string]string{"error": "too long to show on a phone, answer on screen", "code": "too_large"})
		return
	}
	sent, err := h.api(r, "POST", "/api/v1/notifications", map[string]any{
		"topic": topic, "title": title, "message": msg, "priority": 4,
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
		reason = "No answer on clrd within " + strconv.Itoa(int(d.Seconds())) + " s."
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

// maxShown is the longest summary clrd will ask about. Anything longer is never
// cut, since Allow would approve the hidden part too.
const maxShown = 4000

// summary is what the phone shows: a file edit's path and change, else the
// command, else a url or description, then every other field as a
// "key: value" line, else the raw input. Allow approves what it shows, so an
// edit shows the text it writes (#366) and no field is left out (#441).
func summary(raw json.RawMessage) string {
	var in map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber() // 1e400 stays 1e400 instead of failing the decode
	if d.Decode(&in) != nil {
		in = nil // a half-read input falls back to the raw text below
	}
	if p, b := edit(in); p != "" {
		return p + others(in, editKeys...) + "\n" + b
	}
	for _, k := range []string{"command", "file_path", "url", "description"} {
		if v, ok := in[k].(string); ok && v != "" {
			rest := others(in, k)
			if rest != "" && breaks(v) { // a line of v must not pass for a field
				v = strconv.Quote(v)
			}
			return v + rest
		}
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return "(no details)"
}

// others lists in's fields except skip, sorted, one "key: value" line each.
// A value with a line break, or one that is not a string, is shown as JSON,
// so it can never pass for a line of its own.
func others(in map[string]any, skip ...string) string {
	keys := make([]string, 0, len(in))
	for k, v := range in {
		if !slices.Contains(skip, k) && v != nil && v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		key := k
		if breaks(k) || strings.Contains(k, ":") {
			key = strconv.Quote(k)
		}
		fmt.Fprintf(&b, "\n%s: %s", key, oneLine(in[k]))
	}
	return b.String()
}

// editKeys are the fields edit already shows.
var editKeys = []string{"file_path", "notebook_path", "old_string", "new_string", "replace_all", "edits", "content", "new_source", "cell_id", "edit_mode", "cell_type"}

// breaks reports whether s would start a new line on screen.
func breaks(s string) bool { return strings.ContainsAny(s, "\r\n\u0085\u2028\u2029") }

func oneLine(v any) string {
	if s, ok := v.(string); ok {
		if breaks(s) {
			return strconv.Quote(s)
		}
		return s
	}
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false) // show && and < as typed
	_ = e.Encode(v)
	// Go escapes U+2028 and U+2029 already, not U+0085
	return strings.ReplaceAll(strings.TrimSpace(b.String()), "\u0085", `\u0085`)
}

// edit renders Claude Code's Edit, MultiEdit, Write and NotebookEdit inputs
// as the path and a diff-like body, "" for any other input.
func edit(in map[string]any) (string, string) {
	p, _ := in["file_path"].(string)
	nb, _ := in["notebook_path"].(string)
	text := func(k string) (string, bool) { v, ok := in[k].(string); return v, ok }
	lines := func(mark, s string) string { return mark + strings.ReplaceAll(s, "\n", "\n"+mark) + "\n" }
	change := func(e map[string]any) string {
		o, _ := e["old_string"].(string)
		n, _ := e["new_string"].(string)
		s := lines("- ", o) + lines("+ ", n)
		if all, _ := e["replace_all"].(bool); all {
			s = "(every match)\n" + s
		}
		return s
	}
	var b string
	if _, ok := in["new_string"]; ok {
		b = strings.TrimSuffix(change(in), "\n")
	} else if es, ok := in["edits"].([]any); ok {
		for _, e := range es {
			m, _ := e.(map[string]any)
			b += change(m)
		}
		b = strings.TrimSuffix(b, "\n")
	} else if c, ok := text("content"); ok {
		b = c
	} else if nb != "" { // which cell and what is done to it, then the new source
		p = nb
		mode, _ := text("edit_mode")
		if mode == "" {
			mode = "replace"
		}
		cell, _ := text("cell_id")
		kind, _ := text("cell_type")
		b = "(" + strings.Join(strings.Fields(mode+" cell "+cell+" "+kind), " ") + ")"
		if src, ok := text("new_source"); ok && mode != "delete" {
			b += "\n" + src
		}
	} else {
		return "", ""
	}
	if b == "" {
		b = "(empty)"
	}
	if p == "" {
		return "", ""
	}
	if breaks(p) { // a newline in the path must not pass for the change
		p = strconv.Quote(p)
	}
	return p, b
}
