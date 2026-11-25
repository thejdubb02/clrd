package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const installA = "install-aaaaaaaaaaaaaaaa"

// browserIn signs a browser in with typed, sending install, and returns the
// new row's id and cookie.
func browserIn(t *testing.T, s *Server, ts *httptest.Server, typed, install string) (string, *http.Cookie) {
	t.Helper()
	code, _, c := signIn(t, ts, `{"token":"`+typed+`","install":"`+install+`"}`, firefoxLinux, nil)
	if code != 200 || c == nil {
		t.Fatalf("sign in: %d", code)
	}
	tok := s.lookupToken(context.Background(), c.Value)
	if tok == nil {
		t.Fatal("no token behind the cookie")
	}
	return tok.ID, c
}

// age makes id look last used n days ago.
func age(t *testing.T, s *Server, id string, n int) {
	t.Helper()
	at := ms(time.Now().Add(-days(n)))
	if _, err := s.db.Exec(`UPDATE tokens SET last_seen_at = ?, created_at = ? WHERE id = ?`, at, at, id); err != nil {
		t.Fatal(err)
	}
}

func exists(s *Server, id string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE id = ?`, id).Scan(&n)
	return n == 1
}

func column(t *testing.T, s *Server, col, id string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT `+col+` FROM tokens WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// #765 with the #187 guard rails: a browser that signs in again replaces its
// old row, keeps that row's lower approve setting and what hung under it.
func TestRelinkReplacesOwnRow(t *testing.T) {
	s, ts, _ := newTest(t)
	ctx := context.Background()
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	oldID, oldC := browserIn(t, s, ts, typed, installA)
	if _, err := s.db.Exec(`UPDATE tokens SET approve = 'denied' WHERE id = ?`, oldID); err != nil {
		t.Fatal(err)
	}
	old := s.lookupToken(ctx, oldC.Value)
	kid, _, err := s.childToken(ctx, old, oldID, "kid", false)
	if err != nil {
		t.Fatal(err)
	}
	newID, _ := browserIn(t, s, ts, typed, installA)
	if exists(s, oldID) {
		t.Fatal("old row of the same install stayed")
	}
	if withCookie(t, ts, "GET", "/api/v1/topics", oldC) != 401 {
		t.Fatal("old cookie still works")
	}
	if a := column(t, s, "approve", newID); a != "denied" {
		t.Fatalf("approve = %q, want the old row's lower denied", a)
	}
	var parent string
	s.db.QueryRow(`SELECT parent_id FROM sessions WHERE token_id = ?`, kid.ID).Scan(&parent)
	if parent != newID {
		t.Fatalf("child hangs under %q, want the new row", parent)
	}
	if d := column(t, s, "detail", newID); d != "Firefox 120 on Linux" {
		t.Fatalf("detail = %q", d)
	}
}

// An old row raised above its token never lifts the new row past the token.
func TestRelinkNeverRaisesApprove(t *testing.T) {
	s, ts, _ := newTest(t)
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	oldID, _ := browserIn(t, s, ts, typed, installA)
	tid := s.lookupToken(context.Background(), typed).ID
	s.db.Exec(`UPDATE tokens SET approve = 'ask' WHERE id = ?`, tid)
	s.db.Exec(`UPDATE tokens SET approve = 'allowed' WHERE id = ?`, oldID)
	newID, _ := browserIn(t, s, ts, typed, installA)
	if a := column(t, s, "approve", newID); a != "ask" {
		t.Fatalf("approve = %q, want the token's ask", a)
	}
}

// Never by name, never across tokens, never a browser for a phone.
func TestRelinkOnlySameInstallParentKind(t *testing.T) {
	s, ts, _ := newTest(t)
	ctx := context.Background()
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	other := mkToken(t, s, "desk", []string{ScopeRead}, nil)
	a, _ := browserIn(t, s, ts, typed, installA)
	b, _ := browserIn(t, s, ts, typed, "install-bbbbbbbbbbbbbbbb") // same name, other install
	c, _ := browserIn(t, s, ts, other, installA)                   // same install, other token
	// A phone of the same install under the same token.
	lc := linkStart(t, ts, typed)
	body := map[string]any{"code": lc, "secret": phoneSecret, "name": "Pixel", "install": installA, "detail": "Pixel 8, Android 15"}
	call(t, ts, "POST", "/api/v1/link/claim", "", body)
	call(t, ts, "POST", "/api/v1/link/"+lc+"/approve", typed, nil)
	if code, out := call(t, ts, "POST", "/api/v1/link/claim", "", body); code != 200 {
		t.Fatalf("claim: %d %v", code, out)
	}
	for _, id := range []string{a, b, c} {
		if !exists(s, id) {
			t.Fatalf("row %s went", id)
		}
	}
	var detail string
	s.db.QueryRowContext(ctx, `SELECT detail FROM tokens t JOIN sessions s ON s.token_id = t.id WHERE s.phone = 1`).Scan(&detail)
	if detail != "Pixel 8, Android 15" {
		t.Fatalf("phone detail = %q", detail)
	}
}

func TestUnlinkIdleBrowsers(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	tid := s.lookupToken(ctx, typed).ID
	idle, _ := browserIn(t, s, ts, typed, "")
	recent, _ := browserIn(t, s, ts, typed, "")
	pushed, _ := browserIn(t, s, ts, typed, "")
	parent, pc := browserIn(t, s, ts, typed, "")
	streaming, sc := browserIn(t, s, ts, typed, "")
	phone := s.lookupToken(ctx, linkPhone(t, ts, typed)).ID
	if _, _, err := s.childToken(ctx, s.lookupToken(ctx, pc.Value), parent, "kid", false); err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`INSERT INTO push_subs (endpoint, token_id, p256dh, auth, created_at) VALUES ('https://push.example/1', ?, 'k', 'a', 0)`, pushed)
	for _, id := range []string{idle, pushed, parent, streaming, phone, tid} {
		age(t, s, id, 40)
	}
	age(t, s, recent, 10)
	res, _ := openStream(t, ts.URL+"/api/v1/events/sse", sc.Value)
	defer res.Body.Close()
	age(t, s, streaming, 40) // opening the stream marked it seen
	for i := 0; i < 100; i++ {
		if live, _, _ := s.hub.status(streaming); live {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Set to 0, nothing goes.
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"unlink_idle_browsers_days": 0}); code != 200 {
		t.Fatalf("set 0: %d", code)
	}
	if err := s.unlinkIdle(ctx); err != nil || !exists(s, idle) {
		t.Fatalf("0 swept: %v", err)
	}
	call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"unlink_idle_browsers_days": 30})
	if err := s.unlinkIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(s, idle) {
		t.Fatal("idle browser stayed")
	}
	for name, id := range map[string]string{"recent": recent, "pushed": pushed, "parent": parent, "streaming": streaming, "phone": phone, "typed": tid} {
		if !exists(s, id) {
			t.Fatalf("%s row was swept", name)
		}
	}
	_, out := call(t, ts, "GET", "/api/v1/devices/log", admin, nil)
	if !strings.Contains(fmt.Sprint(out), "unused for 30 days") {
		t.Fatalf("no log line: %v", out)
	}
}

func TestTidyAndRename(t *testing.T) {
	s, ts, _ := newTest(t)
	ctx := context.Background()
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	tid := s.lookupToken(ctx, typed).ID
	a, _ := browserIn(t, s, ts, typed, "")
	me, mc := browserIn(t, s, ts, typed, "")
	age(t, s, a, 10)
	age(t, s, me, 10)
	age(t, s, tid, 10)
	_, out := call(t, ts, "POST", "/api/v1/devices/tidy", mc.Value, map[string]any{"days": 7, "dry_run": true})
	if out["count"] != float64(1) || !exists(s, a) {
		t.Fatalf("dry run: %v", out)
	}
	_, out = call(t, ts, "POST", "/api/v1/devices/tidy", mc.Value, map[string]any{"days": 7})
	if out["count"] != float64(1) || exists(s, a) || !exists(s, me) || !exists(s, tid) {
		t.Fatalf("tidy: %v", out)
	}

	if code, out := call(t, ts, "PUT", "/api/v1/devices/"+me, mc.Value, map[string]any{"name": "Work laptop"}); code != 200 || out["name"] != "Work laptop" {
		t.Fatalf("rename browser: %d %v", code, out)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+tid, mc.Value, map[string]any{"name": "x"}); code != 403 {
		t.Fatalf("rename typed token: %d, want 403", code)
	}
}

func TestUnlinkIdleSetting(t *testing.T) {
	s, ts, admin := newTest(t)
	_, st := call(t, ts, "GET", "/api/v1/settings", admin, nil)
	if st["unlink_idle_browsers_days"] != float64(30) {
		t.Fatalf("default = %v", st["unlink_idle_browsers_days"])
	}
	reader := mkToken(t, s, "ops", []string{ScopeRead}, nil)
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", reader, map[string]any{"unlink_idle_browsers_days": 1}); code != 403 {
		t.Fatalf("non-admin: %d, want 403", code)
	}
}
