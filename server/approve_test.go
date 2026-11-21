package server

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestApproveReply: only an allowed device may answer an approval request,
// and never one from its own token tree (#187).
func TestApproveReply(t *testing.T) {
	s, ts, _ := newTest(t)
	ctx := context.Background()
	agent := mkToken(t, s, "agent", []string{ScopeRead, ScopePublish}, nil)
	ask := func(tok string) string {
		t.Helper()
		code, n := call(t, ts, "POST", "/api/v1/notifications", tok, map[string]any{"topic": "agents", "message": "merge?",
			"actions": []map[string]any{{"label": "Allow", "reply": "allow"}}})
		if code != 200 {
			t.Fatalf("send: %d %v", code, n)
		}
		return n["id"].(string)
	}
	reply := func(tok, id string) (int, string) {
		t.Helper()
		code, out := call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", tok, map[string]any{"reply": "allow"})
		c, _ := out["code"].(string)
		return code, c
	}
	session := func(raw string) string { // a browser signed in with raw
		t.Helper()
		p := s.lookupToken(ctx, raw)
		_, b, err := s.childToken(ctx, p, p.ID, "browser", false)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// The sender itself, and a browser made from it, cannot answer.
	id := ask(agent)
	if code, c := reply(agent, id); code != 403 || c != "own_request" {
		t.Fatalf("sender answered its own request: %d %s", code, c)
	}
	if code, c := reply(session(agent), id); code != 403 || c != "own_request" {
		t.Fatalf("sender's browser answered its request: %d %s", code, c)
	}
	// Nor does its parent answer one sent from such a browser, even once
	// that browser signed out.
	b := session(agent)
	fromB := ask(b)
	if code, _ := call(t, ts, "DELETE", "/api/v1/session", b, nil); code != 204 {
		t.Fatalf("sign out: %d", code)
	}
	if code, c := reply(agent, fromB); code != 403 || c != "own_request" {
		t.Fatalf("parent answered its signed-out browser's request: %d %s", code, c)
	}

	// A denied phone is refused, and so is a browser made from it.
	_, denied, err := s.CreateToken(ctx, "pixel", "", []string{ScopeRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, c := reply(denied, id); code != 403 || c != "approve_denied" {
		t.Fatalf("denied token answered: %d %s", code, c)
	}
	if code, c := reply(session(denied), id); code != 403 || c != "approve_denied" {
		t.Fatalf("browser of a denied token answered: %d %s", code, c)
	}

	// An allowed device that did not send it may.
	phone := mkToken(t, s, "phone", []string{ScopeRead}, nil)
	if code, c := reply(phone, id); code != 200 {
		t.Fatalf("allowed phone: %d %s", code, c)
	}
}

// TestSetApprove: only an allowed device changes the setting, and no device
// can raise its own or the token it hangs under, by any route (#187).
func TestSetApprove(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	idOf := func(raw string) string { return s.lookupToken(ctx, raw).ID }
	set := func(tok, id, v string) int {
		t.Helper()
		code, _ := call(t, ts, "PUT", "/api/v1/devices/"+id+"/approve", tok, map[string]any{"approve": v})
		return code
	}
	setting := func(raw string) string { return s.lookupToken(ctx, raw).Approve }

	// A denied admin can raise nothing: not itself, not a new token.
	_, dadmin, err := s.CreateToken(ctx, "agent", ApproveDenied, []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := set(dadmin, idOf(dadmin), ApproveAllowed); code != 403 || setting(dadmin) != ApproveDenied {
		t.Fatalf("denied admin raised itself: %d", code)
	}
	other := mkToken(t, s, "other", []string{ScopeRead}, nil)
	if code := set(dadmin, idOf(other), ApproveDenied); code != 403 || setting(other) != ApproveAllowed {
		t.Fatalf("denied admin changed another token: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/tokens", dadmin, map[string]any{"name": "x", "scopes": []string{"read"}, "approve": ApproveAllowed}); code != 403 {
		t.Fatalf("denied admin made an allowed token: %d", code)
	}
	if code, tk := call(t, ts, "POST", "/api/v1/tokens", dadmin, map[string]any{"name": "x", "scopes": []string{"read"}}); code != 200 || tk["approve"] != ApproveDenied {
		t.Fatalf("new token default: %d %v", code, tk)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/tokens", admin, map[string]any{"name": "y", "scopes": []string{"read"}, "approve": "ask"}); code != 400 {
		t.Fatalf("unknown setting: %d", code)
	}

	// A browser of an allowed token: it may lower the token above it, but
	// never raise it again, and that token, now denied, cannot either.
	top := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	p := s.lookupToken(ctx, top)
	b, braw, err := s.childToken(ctx, p, p.ID, "browser", false)
	if err != nil || b.Approve != ApproveAllowed {
		t.Fatalf("browser of an allowed token: %v %+v", err, b)
	}
	if code := set(braw, p.ID, ApproveDenied); code != 200 || setting(top) != ApproveDenied {
		t.Fatalf("lower the parent: %d", code)
	}
	if code := set(braw, p.ID, ApproveAllowed); code != 403 || setting(top) != ApproveDenied {
		t.Fatalf("browser raised its parent: %d", code)
	}
	if code := set(top, p.ID, ApproveAllowed); code != 403 {
		t.Fatalf("denied token raised itself: %d", code)
	}
	// A browser made from it now starts denied, and cannot raise itself.
	_, b2, err := s.childToken(ctx, p, p.ID, "browser 2", false)
	if err != nil || setting(b2) != ApproveDenied {
		t.Fatalf("browser of a denied token: %v %s", err, setting(b2))
	}
	if code := set(b2, idOf(b2), ApproveAllowed); code != 403 || setting(b2) != ApproveDenied {
		t.Fatalf("denied browser raised itself: %d", code)
	}
	// The allowed browser may raise its sibling, and lower itself.
	if code := set(braw, idOf(b2), ApproveAllowed); code != 200 || setting(b2) != ApproveAllowed {
		t.Fatalf("raise a sibling: %d", code)
	}
	if code := set(braw, b.ID, ApproveDenied); code != 200 || setting(braw) != ApproveDenied {
		t.Fatalf("lower itself: %d", code)
	}
	// The device list shows the setting.
	_, out := call(t, ts, "GET", "/api/v1/devices?all=1", b2, nil)
	got := map[string]any{}
	for _, d := range out["devices"].([]any) {
		got[d.(map[string]any)["id"].(string)] = d.(map[string]any)["approve"]
	}
	if got[p.ID] != ApproveDenied || got[idOf(b2)] != ApproveAllowed {
		t.Fatalf("devices: %v", got)
	}
}

// TestApproveMigration: on upgrade, linked browsers and phones keep working
// (allowed) and plain tokens start denied, once only (#187).
func TestApproveMigration(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "clrd.db")
	open := func() *Server {
		t.Helper()
		s, err := New(Options{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	tok, _, err := s.CreateToken(ctx, "laptop", ApproveAllowed, []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.childToken(ctx, tok, tok.ID, "browser", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE tokens DROP COLUMN approve`); err != nil { // as before #187
		t.Fatal(err)
	}
	s.Close()

	get := func(s *Server, id string) string {
		v, err := approveOf(ctx, s.db, id)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	s = open()
	if get(s, tok.ID) != ApproveDenied || get(s, b.ID) != ApproveAllowed {
		t.Fatalf("upgrade: token %s, browser %s", get(s, tok.ID), get(s, b.ID))
	}
	s.db.Exec(`UPDATE tokens SET approve = 'denied' WHERE id = ?`, b.ID) // the user denies the browser
	s.Close()
	s = open()
	defer s.Close()
	if get(s, b.ID) != ApproveDenied {
		t.Fatal("a restart allowed a browser the user denied")
	}
}
