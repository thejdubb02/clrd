package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A reply action with text takes a typed answer (#153): checked, kept as
// reply_text, cleared with the answer, and under the same approve rules as a
// tap. An ask device's text rides on its confirm prompt as plain text.
func TestReplyText(t *testing.T) {
	s, ts, _ := newTest(t)
	ctx := context.Background()
	agent := mkToken(t, s, "agent", []string{ScopeRead, ScopePublish}, nil)
	phone := mkToken(t, s, "phone", []string{ScopeRead}, nil)
	acts := []any{map[string]any{"label": "Yes", "reply": "yes"}, map[string]any{"label": "Other", "reply": "other", "text": true}}
	pub := func(key string, actions any) (int, map[string]any) {
		return call(t, ts, "POST", "/api/v1/notifications", agent, map[string]any{"topic": "agents", "external_id": key, "message": "when?", "actions": actions})
	}
	for name, bad := range map[string]any{
		"on a link": []any{map[string]any{"label": "x", "url": "https://example.com", "text": true}},
		"two":       []any{map[string]any{"label": "a", "reply": "a", "text": true}, map[string]any{"label": "b", "reply": "b", "text": true}},
	} {
		if code, _ := pub("bad", bad); code != 400 {
			t.Errorf("publish %s: want 400, got %d", name, code)
		}
	}
	code, n := pub("q", acts)
	if code != 200 || n["reply_text"] != "" {
		t.Fatalf("publish: %d %v", code, n)
	}
	id := n["id"].(string)
	reply := func(tok, id, r, text string) (int, map[string]any) {
		t.Helper()
		return call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", tok, map[string]any{"reply": r, "text": text})
	}
	for _, c := range [][2]string{{"other", ""}, {"other", " \n "}, {"yes", "hi"}, {"other", strings.Repeat("é", 1001)}} {
		if code, _ := reply(phone, id, c[0], c[1]); code != 400 {
			t.Errorf("reply %q text %q: want 400, got %d", c[0], c[1][:min(len(c[1]), 8)], code)
		}
	}
	// The sender never answers its own question, text or not.
	if code, out := reply(agent, id, "other", "me"); code != 403 || out["code"] != "own_request" {
		t.Fatalf("own answer: %d %v", code, out)
	}
	_, tv, _ := s.CreateToken(ctx, "tv", ApproveDenied, []string{ScopeRead}, nil)
	if code, out := reply(tv, id, "other", "x"); code != 403 || out["code"] != "approve_denied" {
		t.Fatalf("denied device: %d %v", code, out)
	}
	long := strings.Repeat("é", 998) + "\n!" // 1000 characters, over 1000 bytes
	if code, n := reply(phone, id, "other", "  "+long+"  "); code != 200 || n["reply"] != "other" || n["reply_text"] != long || n["state"] != Acknowledged {
		t.Fatalf("answer: %d %v", code, n)
	}
	if code, _ := reply(phone, id, "yes", ""); code != 409 {
		t.Errorf("second answer: want 409, got %d", code)
	}
	// Every other device sees the text on the stored row and in the log.
	if _, n := call(t, ts, "GET", "/api/v1/notifications/"+id, agent, nil); n["reply_text"] != long {
		t.Fatalf("stored: %v", n)
	}
	_, out := call(t, ts, "GET", "/api/v1/events?since=0", phone, nil)
	evs := out["events"].([]any)
	if last := evs[len(evs)-1].(map[string]any)["notification"].(map[string]any); last["reply_text"] != long {
		t.Fatalf("event: %v", last)
	}
	// The text goes with the answer: a new question, a reopen, a snooze.
	if _, n := pub("q", acts); n["reply_text"] != "" {
		t.Fatalf("new question kept the text: %v", n)
	}
	for _, step := range []string{"reopen", "snooze"} {
		reply(phone, id, "other", "later")
		body := map[string]any{}
		if step == "snooze" {
			body["until"] = time.Now().Add(time.Hour).Format(time.RFC3339)
		}
		if _, n := call(t, ts, "POST", "/api/v1/notifications/"+id+"/"+step, phone, body); n["reply_text"] != "" || n["reply"] != "" {
			t.Fatalf("%s kept the text: %v", step, n)
		}
	}
	// Dismiss keeps it as history.
	reply(phone, id, "other", "done")
	if _, n := call(t, ts, "POST", "/api/v1/notifications/"+id+"/dismiss", phone, nil); n["reply_text"] != "done" {
		t.Fatalf("dismiss dropped the text: %v", n)
	}

	// An ask device's text waits on a prompt, shown as plain text.
	_, lp, braw := askBrowser(t, s, ts)
	_, q := pub("q2", acts)
	qid := q["id"].(string)
	code, p := reply(braw, qid, "other", "**friday**")
	if code != 202 || p["format"] != FormatText || !strings.HasSuffix(p["message"].(string), "\n\nAnswer: **friday**") {
		t.Fatalf("ask: %d %v", code, p)
	}
	if _, again := reply(braw, qid, "other", "**friday**"); again["id"] != p["id"] {
		t.Fatalf("repeat tap made another prompt: %v", again)
	}
	_, p2 := reply(braw, qid, "other", "monday")
	if p2["id"] == p["id"] {
		t.Fatal("other text returned the first prompt")
	}
	_, conf, err := s.childToken(ctx, lp, lp.ID, "phone", true)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := reply(conf, p2["id"].(string), "confirm", ""); code != 200 {
		t.Fatalf("confirm: %d", code)
	}
	if _, n := call(t, ts, "GET", "/api/v1/notifications/"+qid, agent, nil); n["reply_text"] != "monday" || n["state_by"] != "browser" {
		t.Fatalf("confirmed text: %v", n)
	}
	if code, _ := reply(conf, p["id"].(string), "confirm", ""); code != 409 {
		t.Errorf("second prompt confirmed: want 409, got %d", code)
	}
}
