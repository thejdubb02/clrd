package server

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"sync/atomic"
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

// TestApproveAsk: an ask device's tap counts only once an allowed device
// outside the sender's tree confirms it (#187).
func TestApproveAsk(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	agent := mkToken(t, s, "agent", []string{ScopeRead, ScopePublish}, nil)
	ask := func(body map[string]any) map[string]any {
		t.Helper()
		body["topic"], body["actions"] = "agents", []map[string]any{{"label": "Allow", "reply": "allow"}, {"label": "Deny", "reply": "deny"}}
		code, n := call(t, ts, "POST", "/api/v1/notifications", agent, body)
		if code != 200 {
			t.Fatalf("send: %d %v", code, n)
		}
		return n
	}
	reply := func(tok, id, r string) (int, map[string]any) {
		t.Helper()
		return call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", tok, map[string]any{"reply": r})
	}
	get := func(id string) map[string]any {
		t.Helper()
		_, n := call(t, ts, "GET", "/api/v1/notifications/"+id, agent, nil)
		return n
	}
	// The ask device: a browser under an allowed laptop, lowered to ask.
	_, lp, braw := askBrowser(t, s, ts)
	s.fetch.allow = func(netip.Addr, string) bool { return true }
	var status atomic.Int64
	status.Store(200)
	url, hits := receiver(t, &status)
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/agents/hooks/h", admin, map[string]any{"url": url, "events": []string{"action"}}); code != 200 {
		t.Fatalf("hook: %d", code)
	}

	q := ask(map[string]any{"message": "merge?"})["id"].(string)
	code, p := reply(braw, q, "allow")
	if code != 202 || p["id"] == q || p["topic"] != "agents" {
		t.Fatalf("ask tap: %d %v", code, p)
	}
	if n := get(q); n["reply"] != "" || n["state"] != Active {
		t.Fatalf("ask tap answered: %v", n)
	}
	pid := p["id"].(string)
	if _, again := reply(braw, q, "allow"); again["id"] != pid {
		t.Fatalf("repeat tap made another prompt: %v", again)
	}
	// Neither the sender's tree nor an ask device confirms.
	if code, out := reply(agent, pid, "confirm"); code != 403 || out["code"] != "own_request" {
		t.Fatalf("sender confirmed its own request: %d %v", code, out)
	}
	_, ask2, err := s.CreateToken(ctx, "tablet", ApproveAsk, []string{ScopeRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := reply(ask2, pid, "confirm"); code != 403 || out["code"] != "approve_denied" {
		t.Fatalf("ask device confirmed: %d %v", code, out)
	}
	// A phone linked from the same laptop as the browser, as on a real
	// install, confirms: the browser's answer counts.
	_, phone, err := s.childToken(ctx, lp, lp.ID, "phone", true)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := reply(phone, pid, "confirm"); code != 200 || out["state"] != Acknowledged {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if n := get(q); n["reply"] != "allow" || n["state"] != Acknowledged || n["state_by"] != "browser" {
		t.Fatalf("confirmed answer: %v", n)
	}
	// The topic's action hook hears the answer once, never the tap on the prompt.
	if err := s.deliverHooks(ctx); err != nil {
		t.Fatal(err)
	}
	if got := hits(); len(got) != 1 || got[0].body["notification"].(map[string]any)["id"] != q {
		t.Fatalf("action hooks: %v", got)
	}

	// A question changed after the tap cannot be confirmed.
	q2 := ask(map[string]any{"message": "deploy?", "external_id": "d"})["id"].(string)
	_, p2 := reply(braw, q2, "allow")
	ask(map[string]any{"message": "deploy to prod?", "external_id": "d"})
	if code, _ := reply(phone, p2["id"].(string), "confirm"); code != 409 || get(q2)["reply"] != "" {
		t.Fatalf("confirmed a changed question: %d", code)
	}
	// A tap on the changed question gets a fresh prompt, not the stale one.
	_, p3 := reply(braw, q2, "allow")
	if p3["id"] == p2["id"] {
		t.Fatal("tap on a changed question returned the stale prompt")
	}
	// The sender cannot reject it either, and a rejected tap stays rejected.
	p3id := p3["id"].(string)
	if code, out := reply(agent, p3id, "reject"); code != 403 || out["code"] != "own_request" {
		t.Fatalf("sender rejected: %d %v", code, out)
	}
	if code, _ := reply(phone, p3id, "reject"); code != 200 {
		t.Fatalf("reject: %d", code)
	}
	call(t, ts, "POST", "/api/v1/notifications/"+p3id+"/reopen", phone, nil)
	reply(phone, p3id, "confirm")
	if n := get(q2); n["reply"] != "" {
		t.Fatalf("reopened rejected prompt applied the tap: %v", n)
	}
	if _, p4 := reply(braw, q2, "allow"); p4["id"] == p3id {
		t.Fatal("repeat tap returned the rejected prompt")
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
	if code, _ := call(t, ts, "POST", "/api/v1/tokens", dadmin, map[string]any{"name": "x", "scopes": []string{"read"}, "approve": ApproveAllowed}); code != 400 {
		t.Fatalf("denied admin asked for an allowed token: %d", code)
	}
	if code, tk := call(t, ts, "POST", "/api/v1/tokens", dadmin, map[string]any{"name": "x", "scopes": []string{"read"}}); code != 200 || tk["approve"] != ApproveDenied {
		t.Fatalf("new token default: %d %v", code, tk)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/tokens", dadmin, map[string]any{"name": "x", "scopes": []string{"read"}, "approve": ApproveAsk}); code != 400 {
		t.Fatalf("denied admin asked for an ask token: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/tokens", admin, map[string]any{"name": "y", "scopes": []string{"read"}, "approve": "maybe"}); code != 400 {
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
	for _, v := range []string{ApproveAllowed, ApproveAsk} {
		if code := set(braw, p.ID, v); code != 403 || setting(top) != ApproveDenied {
			t.Fatalf("browser raised its parent to %s: %d", v, code)
		}
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

// TestApproveRequest: a device asks to raise its own setting, and it changes
// only when an allowed device that could make the change itself confirms
// the prompt (#187).
func TestApproveRequest(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	mk := func(name string) (*Token, string) {
		t.Helper()
		tok, raw, err := s.CreateToken(ctx, name, ApproveDenied, []string{ScopeRead, ScopePublish}, []string{"agents"})
		if err != nil {
			t.Fatal(err)
		}
		return tok, raw
	}
	ask := func(raw, topic, v string) (int, map[string]any) {
		t.Helper()
		return call(t, ts, "POST", "/api/v1/devices/approve-request", raw, map[string]any{"topic": topic, "approve": v})
	}
	reply := func(raw, id, r string) (int, map[string]any) {
		t.Helper()
		return call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", raw, map[string]any{"reply": r})
	}
	setting := func(id string) string {
		t.Helper()
		v, err := approveOf(ctx, s.db, id)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	agent, araw := mk("agent")
	code, p := ask(araw, "", ApproveAllowed)
	if code != 202 || p["topic"] != "agents" || p["title"] != "Confirm agent may approve: allowed" {
		t.Fatalf("request: %d %v", code, p)
	}
	pid := p["id"].(string)
	if code, again := ask(araw, "agents", ApproveAllowed); code != 202 || again["id"] != pid {
		t.Fatalf("repeat request made a new prompt: %d %v", code, again)
	}
	if code, _ := ask(araw, "agents", ApproveDenied); code != 400 {
		t.Fatalf("a request that is not a raise: %d", code)
	}
	if code, _ := ask(araw, "other", ApproveAllowed); code != 403 {
		t.Fatalf("request on a topic it cannot publish to: %d", code)
	}

	// Who cannot confirm: the requester, an ask device, a token limited to
	// some topics, a device that cannot see the requester.
	if code, out := reply(araw, pid, "confirm"); code != 403 || out["code"] != "approve_denied" {
		t.Fatalf("requester confirmed itself: %d %v", code, out)
	}
	laptop, _, braw := askBrowser(t, s, ts)
	if code, out := reply(braw, pid, "confirm"); code != 403 || out["code"] != "approve_denied" {
		t.Fatalf("ask device confirmed: %d %v", code, out)
	}
	limited := mkToken(t, s, "limited", []string{ScopeRead, ScopeAdmin}, []string{"agents"})
	if code, out := reply(limited, pid, "confirm"); code != 403 || out["code"] != "forbidden" {
		t.Fatalf("limited token confirmed: %d %v", code, out)
	}
	if code, out := reply(laptop, pid, "confirm"); code != 409 {
		t.Fatalf("device that cannot see the requester confirmed: %d %v", code, out)
	}
	if setting(agent.ID) != ApproveDenied {
		t.Fatalf("setting changed by a refused confirm: %s", setting(agent.ID))
	}
	if code, out := reply(admin, pid, "confirm"); code != 200 || setting(agent.ID) != ApproveAllowed {
		t.Fatalf("admin confirm: %d %v %s", code, out, setting(agent.ID))
	}
	// A confirmed request is spent: lowered again, reopen and confirm do not re-raise.
	call(t, ts, "PUT", "/api/v1/devices/"+agent.ID+"/approve", admin, map[string]any{"approve": ApproveDenied})
	if code, _ := call(t, ts, "POST", "/api/v1/notifications/"+pid+"/reopen", admin, nil); code != 200 {
		t.Fatalf("reopen: %d", code)
	}
	if reply(admin, pid, "confirm"); setting(agent.ID) != ApproveDenied {
		t.Fatalf("confirmed request applied twice: %s", setting(agent.ID))
	}

	// A stale request: the setting changed since it asked.
	stale, sraw := mk("stale")
	_, p = ask(sraw, "agents", ApproveAllowed)
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+stale.ID+"/approve", admin, map[string]any{"approve": ApproveAsk}); code != 200 {
		t.Fatalf("set ask: %d", code)
	}
	if code, again := ask(sraw, "agents", ApproveAllowed); code != 202 || again["id"] == p["id"] {
		t.Fatalf("request from a new setting reused the stale prompt: %d %v", code, again)
	}
	if code, _ := reply(admin, p["id"].(string), "confirm"); code != 409 || setting(stale.ID) != ApproveAsk {
		t.Fatalf("stale request applied: %d %s", code, setting(stale.ID))
	}

	// A device made from the requester and raised by the user still cannot
	// raise the token it hangs under.
	par, praw, err := s.CreateToken(ctx, "parent", ApproveDenied, []string{ScopeRead, ScopePublish}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, p = ask(praw, "agents", ApproveAllowed)
	kid, kraw, err := s.childToken(ctx, par, par.ID, "kid", false)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+kid.ID+"/approve", admin, map[string]any{"approve": ApproveAllowed}); code != 200 {
		t.Fatalf("raise kid: %d", code)
	}
	if code, out := reply(kraw, p["id"].(string), "confirm"); code != 403 || setting(par.ID) != ApproveDenied {
		t.Fatalf("kid raised its parent: %d %v", code, out)
	}
	if code, other := ask(praw, "ops", ApproveAllowed); code != 202 || other["id"] == p["id"] || other["topic"] != "ops" {
		t.Fatalf("request on another topic reused the prompt: %d %v", code, other)
	}

	// Reject, reopen, confirm: the reject sticks.
	rej, rraw := mk("rejected")
	_, p = ask(rraw, "agents", ApproveAllowed)
	rid := p["id"].(string)
	if code, _ := reply(admin, rid, "reject"); code != 200 {
		t.Fatalf("reject: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications/"+rid+"/reopen", admin, nil); code != 200 {
		t.Fatalf("reopen: %d", code)
	}
	if reply(admin, rid, "confirm"); setting(rej.ID) != ApproveDenied {
		t.Fatalf("confirm after reject applied: %s", setting(rej.ID))
	}
	if code, again := ask(rraw, "agents", ApproveAllowed); code != 202 || again["id"] == rid {
		t.Fatalf("request after reject reused the prompt: %d %v", code, again)
	}
}

// askBrowser makes a laptop (allowed) and a browser under it lowered to ask.
// It returns the laptop's raw token and token, and the browser's raw token.
func askBrowser(t *testing.T, s *Server, ts *httptest.Server) (string, *Token, string) {
	t.Helper()
	ctx := context.Background()
	laptop := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	lp := s.lookupToken(ctx, laptop)
	b, braw, err := s.childToken(ctx, lp, lp.ID, "browser", false)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+b.ID+"/approve", laptop, map[string]any{"approve": ApproveAsk}); code != 200 {
		t.Fatalf("lower to ask: %d", code)
	}
	return laptop, lp, braw
}

// TestMintNeverAboveMaker tries every way to make a token or device, from an
// ask admin and a denied admin, and checks nothing made can approve more than
// its maker or confirm a prompt (#187). An allowed admin's new token starts
// denied too. MCP create_token forwards to the same POST /api/v1/tokens.
func TestMintNeverAboveMaker(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	mint := func(tok, approve string) (int, map[string]any) {
		body := map[string]any{"name": "script", "scopes": []string{ScopeAdmin}}
		if approve != "" {
			body["approve"] = approve
		}
		return call(t, ts, "POST", "/api/v1/tokens", tok, body)
	}
	for _, v := range []string{ApproveAllowed, ApproveAsk} {
		if code, _ := mint(admin, v); code != 400 {
			t.Fatalf("allowed admin minted %s: %d", v, code)
		}
	}
	code, tk := mint(admin, "")
	if code != 200 || tk["approve"] != ApproveDenied {
		t.Fatalf("allowed admin's new token: %d %v", code, tk)
	}
	// Raising it is the same change as for any device.
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+tk["id"].(string)+"/approve", admin, map[string]any{"approve": ApproveAllowed}); code != 200 {
		t.Fatalf("raise the new token: %d", code)
	}

	// A prompt for the children to try to confirm.
	_, araw, err := s.CreateToken(ctx, "agent", ApproveDenied, []string{ScopeRead, ScopePublish}, []string{"agents"})
	if err != nil {
		t.Fatal(err)
	}
	code, p := call(t, ts, "POST", "/api/v1/devices/approve-request", araw, map[string]any{"topic": "agents", "approve": ApproveAllowed})
	if code != 202 {
		t.Fatalf("approve request: %d %v", code, p)
	}
	pid := p["id"].(string)

	for _, level := range []string{ApproveAsk, ApproveDenied} {
		_, maker, err := s.CreateToken(ctx, "maker-"+level, level, []string{ScopeAdmin}, nil)
		if err != nil {
			t.Fatal(err)
		}
		kids := map[string]string{}
		for _, v := range []string{ApproveAllowed, ApproveAsk} {
			if code, _ := mint(maker, v); code != 400 {
				t.Fatalf("%s maker minted %s: %d", level, v, code)
			}
		}
		_, tk := mint(maker, "")
		if tk["approve"] != ApproveDenied {
			t.Fatalf("%s maker's new token: %v", level, tk)
		}
		kids["api"], _ = tk["token"].(string)
		if _, _, c := signIn(t, ts, `{"token":"`+maker+`"}`, firefoxLinux, nil); c != nil {
			kids["sign-in"] = c.Value
		}
		pc := browser(t)
		_, pr, _ := pairSend(t, pc, "POST", ts.URL+"/api/v1/pair", "", nil)
		pc0 := pr["code"].(string)
		pairSend(t, pc, "POST", ts.URL+"/api/v1/pair/claim", "", map[string]string{"code": pc0})
		pairSend(t, pc, "POST", ts.URL+"/api/v1/pair/"+pc0+"/approve", maker, nil)
		pairSend(t, pc, "POST", ts.URL+"/api/v1/pair/claim", "", map[string]string{"code": pc0})
		u, _ := url.Parse(ts.URL)
		for _, c := range pc.Jar.Cookies(u) {
			if s.lookupToken(ctx, c.Value) != nil {
				kids["pairing"] = c.Value
			}
		}
		kids["phone link"] = linkPhone(t, ts, maker)

		for how, raw := range kids {
			got := s.lookupToken(ctx, raw)
			if got == nil {
				t.Fatalf("%s maker, %s: no token", level, how)
			}
			if got.Approve != ApproveDenied && got.Approve != level {
				t.Fatalf("%s maker, %s: child is %s", level, how, got.Approve)
			}
			if code, out := call(t, ts, "POST", "/api/v1/notifications/"+pid+"/reply", raw, map[string]any{"reply": "confirm"}); code != 403 {
				t.Fatalf("%s maker, %s: confirmed a prompt: %d %v", level, how, code, out)
			}
		}
		if len(kids) != 4 {
			t.Fatalf("%s maker: made %d of 4 kinds: %v", level, len(kids), kids)
		}
	}
}
