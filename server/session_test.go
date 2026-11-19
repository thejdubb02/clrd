package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const firefoxLinux = "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0"

// signIn posts a JSON sign-in and returns the status, the device it made and
// the session cookie.
func signIn(t *testing.T, ts *httptest.Server, body, ua string, old *http.Cookie) (int, map[string]any, *http.Cookie) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/session", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	if old != nil {
		req.AddCookie(old)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	var c *http.Cookie
	for _, k := range res.Cookies() {
		if k.Name == sessionCookie {
			c = k
		}
	}
	return res.StatusCode, out, c
}

func withCookie(t *testing.T, ts *httptest.Server, method, path string, c *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, nil)
	req.AddCookie(c)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func tokenIDs(t *testing.T, ts *httptest.Server, admin string) map[string]string {
	t.Helper()
	_, toks := call(t, ts, "GET", "/api/v1/tokens", admin, nil)
	ids := map[string]string{}
	for _, tk := range toks["tokens"].([]any) {
		m := tk.(map[string]any)
		ids[m["id"].(string)] = m["name"].(string)
	}
	return ids
}

// Issue #30: every browser that signs in is its own named device.
func TestSignInMintsDevice(t *testing.T) {
	s, ts, admin := newTest(t)
	typed := mkToken(t, s, "laptop", []string{ScopeRead, ScopePublish}, []string{"home"})
	parent := s.lookupToken(context.Background(), typed)

	code, a, ca := signIn(t, ts, `{"token":"`+typed+`"}`, firefoxLinux, nil)
	if code != 200 || ca == nil || ca.Value == typed {
		t.Fatalf("sign-in: %d, cookie %v", code, ca)
	}
	if a["name"] != "Firefox on Linux" || a["id"] == parent.ID {
		t.Fatalf("device: %v", a)
	}
	if got := s.lookupToken(context.Background(), ca.Value); got == nil ||
		!slices.Equal(got.Scopes, parent.Scopes) || !slices.Equal(got.Topics, parent.Topics) {
		t.Fatalf("session token %+v, parent %+v", got, parent)
	}
	_, b, cb := signIn(t, ts, `{"token":"`+typed+`","name":" work\n pc "}`, firefoxLinux, nil)
	if b["name"] != "work pc" || b["id"] == a["id"] {
		t.Fatalf("second device: %v", b)
	}
	ids := tokenIDs(t, ts, admin)
	if ids[a["id"].(string)] != "Firefox on Linux" || ids[b["id"].(string)] != "work pc" {
		t.Fatalf("device list: %v", ids)
	}
	// The browser acts as itself.
	_, n := call(t, ts, "POST", "/api/v1/notifications", typed, map[string]any{"topic": "home", "message": "hi"})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/notifications/"+n["id"].(string)+"/dismiss", nil)
	req.AddCookie(cb)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("dismiss: %v %v", err, res)
	}
	if _, got := call(t, ts, "GET", "/api/v1/notifications/"+n["id"].(string), typed, nil); got["state_by"] != "work pc" {
		t.Fatalf("state_by: %v", got["state_by"])
	}

	// Signing in again on browser A replaces its session.
	_, a2, ca2 := signIn(t, ts, `{"token":"`+typed+`"}`, firefoxLinux, ca)
	if _, ok := tokenIDs(t, ts, admin)[a["id"].(string)]; ok || withCookie(t, ts, "GET", "/api/v1/topics", ca) != 401 {
		t.Fatal("old session survives a new sign-in")
	}
	// Sign-out revokes the session token, and only that.
	if c := withCookie(t, ts, "DELETE", "/api/v1/session", ca2); c != 204 {
		t.Fatalf("sign-out: %d", c)
	}
	if _, ok := tokenIDs(t, ts, admin)[a2["id"].(string)]; ok || withCookie(t, ts, "GET", "/api/v1/topics", ca2) != 401 {
		t.Fatal("session token survives sign-out")
	}
	if c, _ := call(t, ts, "GET", "/api/v1/topics", typed, nil); c != 200 {
		t.Fatalf("typed token after sign-out: %d", c)
	}
	if withCookie(t, ts, "GET", "/api/v1/topics", cb) != 200 {
		t.Fatal("sign-out on A signed B out")
	}
}

// A cookie that holds a real token (set before #30, or by setup) must not
// revoke that token on sign-out.
func TestSignOutKeepsRealToken(t *testing.T) {
	_, ts, admin := newTest(t)
	if c := withCookie(t, ts, "DELETE", "/api/v1/session", &http.Cookie{Name: sessionCookie, Value: admin}); c != 204 {
		t.Fatalf("sign-out: %d", c)
	}
	if c, _ := call(t, ts, "GET", "/api/v1/tokens", admin, nil); c != 200 {
		t.Fatalf("admin token after legacy sign-out: %d", c)
	}
}

// Revoking the token typed in also revokes the browsers signed in with it,
// and closes their streams.
func TestRevokeParentEndsSessions(t *testing.T) {
	s, ts, admin := newTest(t)
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	_, _, c := signIn(t, ts, `{"token":"`+typed+`"}`, firefoxLinux, nil)
	ws := dialWS(t, ts.URL, c.Value, 0)
	ws.until(func(m msg) bool { return m.Type == "ready" })

	call(t, ts, "DELETE", "/api/v1/tokens/"+s.lookupToken(context.Background(), typed).ID, admin, nil)
	if withCookie(t, ts, "GET", "/api/v1/topics", c) != 401 {
		t.Fatal("session outlives its parent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := ws.c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
		t.Fatalf("session stream after parent revoke: %v", err)
	}
	if code, _ := call(t, ts, "DELETE", "/api/v1/tokens/nope", admin, nil); code != 404 {
		t.Fatalf("unknown token: %d", code)
	}
}

func TestDeviceName(t *testing.T) {
	for _, c := range [][2]string{
		{firefoxLinux, "Firefox on Linux"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36", "Chrome on Android"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15", "Safari on macOS"},
		{"Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/120.0 Safari/537.36 Edg/120.0", "Edge on Windows"},
		{"curl/8.0", "Web browser"},
	} {
		if got := deviceName("", c[0]); got != c[1] {
			t.Errorf("%q: got %q, want %q", c[0], got, c[1])
		}
	}
	if got := deviceName(strings.Repeat("é", 100), ""); len([]rune(got)) != 64 {
		t.Errorf("long name not capped: %d", len([]rune(got)))
	}
}
