package server

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Test secrets the phone makes up: any 32+ characters.
var phoneSecret, otherSecret = strings.Repeat("p", 32), strings.Repeat("o", 32)

func linkClaim(t *testing.T, base, code, secret string) (int, map[string]any) {
	t.Helper()
	st, out, _ := pairSend(t, browser(t), "POST", base+"/api/v1/link/claim", "", map[string]string{"code": code, "secret": secret, "name": " Pixel\n9 "})
	return st, out
}

func TestLinkPhone(t *testing.T) {
	s, ts, admin := newTest(t)
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	pc := browser(t)
	if code, _, raw := pairSend(t, pc, "POST", ts.URL+"/api/v1/session", "", map[string]string{"token": typed}); code != 200 {
		t.Fatalf("sign in: %d %s", code, raw)
	}
	as := func(method, path string) (int, map[string]any) { // the signed-in browser, by its cookie
		code, out, _ := pairSend(t, pc, method, ts.URL+path, "", nil)
		return code, out
	}

	if code, _, _ := pairSend(t, browser(t), "POST", ts.URL+"/api/v1/link", "", nil); code != 401 {
		t.Fatalf("create without a token: %d, want 401", code)
	}
	code, p := as("POST", "/api/v1/link")
	lc, _ := p["code"].(string)
	if code != 201 || len(lc) != 22 || !strings.HasSuffix(p["url"].(string), "/link?c="+lc) ||
		!strings.HasPrefix(p["qr"].(string), "data:image/png;base64,") {
		t.Fatalf("create: %d %v", code, p)
	}
	if code, info := as("GET", "/api/v1/link/"+lc); code != 200 || info["state"] != "waiting" || info["name"] != "" {
		t.Fatalf("info before scan: %d %v", code, info)
	}
	if code, _ := as("POST", "/api/v1/link/"+lc+"/approve"); code != 409 {
		t.Fatalf("approve before claim: %d, want 409", code)
	}
	if code, _ := linkClaim(t, ts.URL, lc, "short"); code != 400 {
		t.Fatalf("claim with a short secret: %d, want 400", code)
	}
	if code, _ := linkClaim(t, ts.URL, lc, phoneSecret); code != 202 {
		t.Fatalf("claim before approve: %d, want 202", code)
	}
	if code, _ := linkClaim(t, ts.URL, lc, otherSecret); code != 410 {
		t.Fatalf("claim with another secret: %d, want 410", code)
	}
	if code, info := as("GET", "/api/v1/link/"+lc); code != 200 || info["state"] != "claimed" || info["name"] != "Pixel 9" {
		t.Fatalf("info after scan: %d %v", code, info)
	}
	// Only the browser that made the code sees or decides it, even an admin gets 404.
	for _, path := range []string{"GET /api/v1/link/" + lc, "POST /api/v1/link/" + lc + "/approve", "POST /api/v1/link/" + lc + "/deny"} {
		m, u, _ := strings.Cut(path, " ")
		if code, _, _ := pairSend(t, browser(t), m, ts.URL+u, admin, nil); code != 404 {
			t.Fatalf("%s by another token: %d, want 404", path, code)
		}
	}
	// A link code is not a pair code: Flow B's routes, open to any token, cannot reach it.
	for _, path := range []string{"GET /api/v1/pair/" + lc, "POST /api/v1/pair/" + lc + "/approve", "POST /api/v1/pair/" + lc + "/deny"} {
		m, u, _ := strings.Cut(path, " ")
		if code, _, _ := pairSend(t, browser(t), m, ts.URL+u, admin, nil); code != 404 {
			t.Fatalf("%s: %d, want 404", path, code)
		}
	}
	if code, out := as("POST", "/api/v1/link/"+lc+"/approve"); code != 200 || out["name"] != "Pixel 9" {
		t.Fatalf("approve: %d %v", code, out)
	}
	// After approve, a wrong secret still gets nothing and does not spend the code.
	if code, out := linkClaim(t, ts.URL, lc, otherSecret); code != 410 || out["token"] != nil {
		t.Fatalf("claim with another secret after approve: %d %v", code, out)
	}
	code, out := linkClaim(t, ts.URL, lc, phoneSecret)
	tok, _ := out["token"].(string)
	if code != 200 || out["state"] != "approved" || out["name"] != "Pixel 9" || !strings.HasPrefix(tok, "clrd_") {
		t.Fatalf("claim after approve: %d %v", code, out)
	}
	if code, _ := linkClaim(t, ts.URL, lc, phoneSecret); code != 410 {
		t.Fatalf("claim reused: %d, want 410", code)
	}
	// The phone has the browser's access and no more.
	if code, _ := call(t, ts, "GET", "/api/v1/notifications", tok, nil); code != 200 {
		t.Fatalf("list as the phone: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", tok, map[string]string{"topic": "x", "title": "y"}); code != 403 {
		t.Fatalf("publish as a read-only phone: %d, want 403", code)
	}
	if ids := tokenIDs(t, ts, admin); !strings.Contains(strings.Join(mapValues(ids), ","), "Pixel 9") {
		t.Fatalf("device list: %v", ids)
	}
	// Signing the browser out keeps the phone; revoking the typed token ends both.
	u, _ := url.Parse(ts.URL)
	var session string
	for _, c := range pc.Jar.Cookies(u) {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}
	if code, _ := as("DELETE", "/api/v1/session"); code != 204 || session == "" {
		t.Fatalf("sign out: %d, session %q", code, session)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/notifications", session, nil); code != 401 {
		t.Fatalf("browser token after sign-out: %d, want 401", code)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/notifications", tok, nil); code != 200 {
		t.Fatalf("phone after the browser signed out: %d, want 200", code)
	}
	root := s.lookupToken(context.Background(), typed)
	if err := s.DeleteToken(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/notifications", tok, nil); code != 401 {
		t.Fatalf("phone after its root token is revoked: %d, want 401", code)
	}
}

func mapValues(m map[string]string) []string {
	out := []string{}
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func linkStart(t *testing.T, ts *httptest.Server, tok string) string {
	t.Helper()
	_, p := call(t, ts, "POST", "/api/v1/link", tok, nil)
	return p["code"].(string)
}

func TestLinkDenyExpiryAndPage(t *testing.T) {
	s, ts, admin := newTest(t)
	start := func() string { return linkStart(t, ts, admin) }

	c := start()
	_, _, page := pairSend(t, browser(t), "GET", ts.URL+"/link?c="+c, "", nil)
	if !strings.Contains(page, `href="clrd://link?code=`+c+`&amp;server=`) || !strings.Contains(page, "Link a phone") {
		t.Fatalf("link page: %s", page)
	}
	if code, _ := linkClaim(t, ts.URL, c, phoneSecret); code != 202 {
		t.Fatalf("claim: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/link/"+c+"/deny", admin, nil); code != 204 {
		t.Fatalf("deny: %d", code)
	}
	if code, _ := linkClaim(t, ts.URL, c, phoneSecret); code != 410 {
		t.Fatalf("claim after deny: %d, want 410", code)
	}

	c = start()
	if code, _ := linkClaim(t, ts.URL, c, phoneSecret); code != 202 {
		t.Fatalf("claim: %d", code)
	}
	s.pairMu.Lock()
	s.pairs[c].created = time.Now().Add(-pairTTL - time.Second)
	s.pairMu.Unlock()
	if code, _ := call(t, ts, "POST", "/api/v1/link/"+c+"/approve", admin, nil); code != 404 {
		t.Fatalf("approve expired: %d, want 404", code)
	}
	if code, _ := linkClaim(t, ts.URL, c, phoneSecret); code != 410 {
		t.Fatalf("claim expired: %d, want 410", code)
	}
	_, _, page = pairSend(t, browser(t), "GET", ts.URL+"/link?c="+c, "", nil)
	if !strings.Contains(page, "expired") || strings.Contains(page, "clrd://") {
		t.Fatalf("expired link page: %s", page)
	}

	// Made with a typed token, not a browser session: the phone hangs off that token.
	creator := mkToken(t, s, "script", []string{ScopeRead}, nil)
	c = linkStart(t, ts, creator)
	linkClaim(t, ts.URL, c, phoneSecret)
	call(t, ts, "POST", "/api/v1/link/"+c+"/approve", creator, nil)
	_, out := linkClaim(t, ts.URL, c, phoneSecret)
	tok, _ := out["token"].(string)
	if code, _ := call(t, ts, "GET", "/api/v1/notifications", tok, nil); code != 200 {
		t.Fatalf("phone of a typed token: %d", code)
	}
	if err := s.DeleteToken(context.Background(), s.lookupToken(context.Background(), creator).ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/notifications", tok, nil); code != 401 {
		t.Fatalf("phone after its creator is revoked: %d, want 401", code)
	}

	// A pair code cannot be claimed as a link.
	_, p, _ := pairSend(t, browser(t), "POST", ts.URL+"/api/v1/pair", "", nil)
	if code, _ := linkClaim(t, ts.URL, p["code"].(string), phoneSecret); code != 410 {
		t.Fatalf("link claim of a pair code: %d, want 410", code)
	}
}
