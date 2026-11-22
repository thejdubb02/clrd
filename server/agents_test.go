package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func invitePage(t *testing.T, link string) (int, string) {
	t.Helper()
	res, err := http.Get(link) // a client never sends the part after #
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func devicesOf(t *testing.T, ts *httptest.Server, tok string) map[string]map[string]any {
	t.Helper()
	code, out := call(t, ts, "GET", "/api/v1/devices?all=1", tok, nil)
	if code != 200 {
		t.Fatalf("devices: %d %v", code, out)
	}
	m := map[string]map[string]any{}
	for _, d := range out["devices"].([]any) {
		d := d.(map[string]any)
		m[d["name"].(string)] = d
	}
	return m
}

// Issue #454: a person makes one invite, an agent claims it once and gets
// its own user and token, held to the invite's topics and denied approvals.
func TestAgentInvite(t *testing.T) {
	s, ts, admin := newTest(t)
	laptop := mkToken(t, s, "laptop", []string{ScopeRead, ScopePublish}, []string{"home", "work"})
	person := childOf(t, s, laptop) // the person, signed in on the web

	// Only a signed-in person or a full admin may invite: not a typed-in token.
	ask := map[string]any{"name": "bot", "topics": []string{"home"}}
	if code, _ := call(t, ts, "POST", "/api/v1/agents/invite", laptop, ask); code != 403 {
		t.Fatalf("typed-in token invited: %d", code)
	}
	// Never more than the person holds, and no patterns from a person.
	for _, topics := range [][]string{{"other"}, {"ho*"}, {}} {
		if code, _ := call(t, ts, "POST", "/api/v1/agents/invite", person, map[string]any{"name": "bot", "topics": topics}); code < 400 || code > 403 {
			t.Fatalf("invite %v: %d, want a refusal", topics, code)
		}
	}
	// Even a person with every topic names exact topics only: a pattern is an admin's.
	wide := childOf(t, s, mkToken(t, s, "desktop", []string{ScopeRead, ScopePublish}, nil))
	if code, _ := call(t, ts, "POST", "/api/v1/agents/invite", wide, map[string]any{"name": "bot", "topics": []string{"ho*"}}); code != 400 {
		t.Fatalf("person invited a pattern: %d, want 400", code)
	}
	for _, exp := range []string{"30s", "2h", "soon"} {
		if code, _ := call(t, ts, "POST", "/api/v1/agents/invite", person, map[string]any{"name": "bot", "topics": []string{"home"}, "expires_in": exp}); code != 400 {
			t.Fatalf("expires_in %s: %d, want 400", exp, code)
		}
	}
	code, iv := call(t, ts, "POST", "/api/v1/agents/invite", person, ask)
	ic, _ := iv["code"].(string)
	if code != 201 || len(ic) != 22 || iv["invite"] != "clrd://agent?code="+ic+"&server="+url.QueryEscape(ts.URL) ||
		iv["url"] != ts.URL+"/agent#"+ic { // #463: after #, so no request or proxy log carries it
		t.Fatalf("invite: %d %v", code, iv)
	}
	exp, _ := time.Parse(time.RFC3339Nano, iv["expires_at"].(string))
	if d := time.Until(exp); d < 14*time.Minute || d > inviteTTL {
		t.Fatalf("default expiry %s, want 15 minutes", d)
	}
	// A pair or link route cannot reach an invite.
	if code, _ := call(t, ts, "GET", "/api/v1/pair/"+ic, admin, nil); code != 404 {
		t.Fatalf("pair route saw an invite: %d", code)
	}

	// The https form is a plain text guide, spends nothing and holds no token.
	for range 2 {
		code, page := invitePage(t, iv["url"].(string))
		if code != 200 || !strings.Contains(page, "/api/v1/agents/claim") || !strings.Contains(page, "after #") ||
			strings.Contains(page, tokenPrefix) {
			t.Fatalf("page: %d %s", code, page)
		}
	}

	code, cl := call(t, ts, "POST", "/api/v1/agents/claim", "", map[string]string{"code": ic})
	agent, _ := cl["token"].(string)
	guide, _ := cl["guide"].(map[string]any)
	if code != 200 || !strings.HasPrefix(agent, tokenPrefix) || guide["publish"] != ts.URL+"/api/v1/notifications" ||
		guide["mcp"] != ts.URL+"/mcp" || guide["approve"] != ApproveDenied {
		t.Fatalf("claim: %d %v", code, cl)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/agents/claim", "", map[string]string{"code": ic}); code != 410 {
		t.Fatalf("second claim: %d, want 410", code)
	}

	// Held to the invite's topics.
	pub := func(tok, topic string) int {
		code, _ := call(t, ts, "POST", "/api/v1/notifications", tok, map[string]any{"topic": topic, "message": "x"})
		return code
	}
	if pub(agent, "home") != 200 || pub(agent, "work") != 403 || pub(agent, "HOME") != 403 {
		t.Fatalf("agent publish: home %d, work %d, HOME %d (#458)", pub(agent, "home"), pub(agent, "work"), pub(agent, "HOME"))
	}
	// An agent never invites another, from its token or a browser made from it.
	for _, tok := range []string{agent, childOf(t, s, agent)} {
		if code, _ := call(t, ts, "POST", "/api/v1/agents/invite", tok, map[string]any{"name": "bot2", "topics": []string{"home"}}); code != 403 {
			t.Fatalf("agent invited: %d", code)
		}
	}

	// It shows in the person's devices and the user list, not the other way round.
	d := devicesOf(t, ts, person)["bot"]
	if d == nil || d["kind"] != "agent" || d["can_unlink"] != true || d["approve"] != ApproveDenied {
		t.Fatalf("person's devices: %v", devicesOf(t, ts, person))
	}
	if mine := devicesOf(t, ts, agent); mine["laptop"] != nil || mine["bot"] == nil {
		t.Fatalf("agent's devices: %v", mine)
	}
	_, us := call(t, ts, "GET", "/api/v1/users", admin, nil)
	var u map[string]any
	for _, x := range us["users"].([]any) {
		if x.(map[string]any)["name"] == "bot" {
			u = x.(map[string]any)
		}
	}
	if u == nil || u["kind"] != "agent" || u["last_seen_at"] == nil || u["id"] != cl["user"] {
		t.Fatalf("users: %v", us)
	}

	// #187: it cannot raise its own setting, nor answer its own request.
	aid := d["id"].(string)
	for _, v := range []string{ApproveAllowed, ApproveAsk} {
		if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+aid+"/approve", agent, map[string]any{"approve": v}); code != 403 {
			t.Fatalf("agent set itself %s: %d", v, code)
		}
	}
	code, n := call(t, ts, "POST", "/api/v1/notifications", agent, map[string]any{"topic": "home", "message": "merge?",
		"actions": []map[string]any{{"label": "Allow", "reply": "allow"}}})
	if code != 200 {
		t.Fatalf("ask: %d %v", code, n)
	}
	reply := func(tok string) (int, map[string]any) {
		return call(t, ts, "POST", "/api/v1/notifications/"+n["id"].(string)+"/reply", tok, map[string]any{"reply": "allow"})
	}
	if code, out := reply(agent); code != 403 {
		t.Fatalf("agent answered its own request: %d %v", code, out)
	}
	if code, out := reply(person); code != 200 { // the person it asks may answer
		t.Fatalf("person answered: %d %v", code, out)
	}

	// The name is now taken.
	if code, _ := call(t, ts, "POST", "/api/v1/agents/invite", person, ask); code != 409 {
		t.Fatalf("invite a taken name: %d, want 409", code)
	}

	// Unlink ends its streams at once, revokes it and drops its user.
	res, lines := openStream(t, ts.URL+"/api/v1/events/sse", agent)
	defer res.Body.Close()
	if code, _ := call(t, ts, "DELETE", "/api/v1/devices/"+aid, person, nil); code != 204 {
		t.Fatalf("unlink: %d", code)
	}
	closed := time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-lines:
		case <-closed:
			t.Fatal("agent stream still open after unlink")
		}
	}
	if pub(agent, "home") != 401 {
		t.Fatal("unlinked agent still publishes")
	}
	_, us = call(t, ts, "GET", "/api/v1/users", admin, nil)
	if b, _ := json.Marshal(us); strings.Contains(string(b), `"bot"`) {
		t.Fatalf("agent user kept after unlink: %v", us)
	}
}

// An admin token may invite (agents provision agents), with patterns.
func TestAgentInviteAdmin(t *testing.T) {
	_, ts, admin := newTest(t)
	code, iv := call(t, ts, "POST", "/api/v1/agents/invite", admin, map[string]any{"name": "ci", "topics": []string{"ops-*"}, "expires_in": "1m"})
	if code != 201 {
		t.Fatalf("admin invite: %d %v", code, iv)
	}
	_, cl := call(t, ts, "POST", "/api/v1/agents/claim", "", map[string]string{"code": iv["code"].(string)})
	agent, _ := cl["token"].(string)
	pub := func(topic string) int {
		code, _ := call(t, ts, "POST", "/api/v1/notifications", agent, map[string]any{"topic": topic, "message": "x"})
		return code
	}
	if pub("ops-deploy") != 200 || pub("home") != 403 {
		t.Fatalf("admin-made agent: ops-deploy %d, home %d", pub("ops-deploy"), pub("home"))
	}
	if code, _ := call(t, ts, "GET", "/api/v1/users", agent, nil); code != 403 {
		t.Fatalf("agent got admin: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/agents/claim", "", map[string]string{"code": "nope"}); code != 410 {
		t.Fatalf("unknown code: %d, want 410", code)
	}
}

// Review of #454: a publish-only maker gives a publish-only agent, and
// revoking the maker kills its invites not yet claimed.
func TestAgentInviteLimits(t *testing.T) {
	s, ts, _ := newTest(t)
	pubber := childOf(t, s, mkToken(t, s, "pubber", []string{ScopePublish}, []string{"alerts"}))
	_, iv := call(t, ts, "POST", "/api/v1/agents/invite", pubber, map[string]any{"name": "p", "topics": []string{"alerts"}})
	_, cl := call(t, ts, "POST", "/api/v1/agents/claim", "", map[string]string{"code": iv["code"].(string)})
	agent, _ := cl["token"].(string)
	if code, _ := call(t, ts, "GET", "/api/v1/devices/log", agent, nil); code != 403 {
		t.Fatalf("publish-only agent read the device log: %d", code)
	}

	ot, ops, _ := s.CreateToken(context.Background(), "ops", "denied", []string{ScopeAdmin}, nil)
	_, iv = call(t, ts, "POST", "/api/v1/agents/invite", ops, map[string]any{"name": "q", "topics": []string{"alerts"}})
	if err := s.DeleteToken(context.Background(), ot.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/agents/claim", "", map[string]string{"code": iv["code"].(string)}); code != 410 {
		t.Fatalf("claim after the maker was revoked: %d, want 410", code)
	}
}
