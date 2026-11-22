package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFile(t *testing.T) {
	s, ts, admin := newTest(t)
	apiTok := mkToken(t, s, "made on the API", []string{ScopeRead}, nil)
	f := filepath.Join(t.TempDir(), "clrd.json")
	s.opt.Config = f
	apply := func(body string) error {
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return s.applyConfig(context.Background())
	}
	phone := "clrd_phonephonephonephone"
	t.Setenv("CLRD_TEST_LAPTOP", "clrd_laptoplaptoplaptop")
	full := `{"users": [{"name": "justin", "grants": [{"topic": "home", "scopes": ["read"]}]}],
	  "tokens": [{"name": "phone", "secret": "` + phone + `", "scopes": ["read", "publish"], "user": "justin", "approve": "allowed"},
	    {"name": "laptop", "secret": "$CLRD_TEST_LAPTOP", "scopes": ["read"]},
	    {"name": "taken over", "secret": "` + apiTok + `", "scopes": ["read", "publish"]}]}`
	if err := apply(full); err != nil {
		t.Fatal(err)
	}
	if err := apply(full); err != nil { // a second start changes nothing
		t.Fatal(err)
	}
	// The phone follows its user's grants: home only, read only.
	if c, _ := call(t, ts, "GET", "/api/v1/notifications?topic=home", phone, nil); c != 200 {
		t.Fatalf("phone reads home: %d", c)
	}
	if c, _ := call(t, ts, "POST", "/api/v1/notifications", phone, map[string]any{"topic": "home", "title": "x"}); c != 403 {
		t.Fatalf("phone publishes past its user's grants: %d", c)
	}
	if c, _ := call(t, ts, "GET", "/api/v1/notifications", "clrd_laptoplaptoplaptop", nil); c != 200 {
		t.Fatalf("laptop with its secret from the environment: %d", c)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE hash = ? AND name = 'taken over' AND scopes = 'read,publish'`, hashToken(apiTok)).Scan(&n)
	if n != 1 {
		t.Fatal("a token named by its secret is not taken over")
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM people`).Scan(&n)
	if n != 1 {
		t.Fatalf("people after two starts: %d", n)
	}

	// Dropped from the file: the file's rows go, a token made on the API stays.
	other := mkToken(t, s, "other", []string{ScopeRead}, nil)
	if err := apply(`{"tokens": [{"name": "laptop", "secret": "$CLRD_TEST_LAPTOP", "scopes": ["read"]}]}`); err != nil {
		t.Fatal(err)
	}
	for tok, want := range map[string]int{phone: 401, apiTok: 401, other: 200, "clrd_laptoplaptoplaptop": 200, admin: 200} {
		if c, _ := call(t, ts, "GET", "/api/v1/notifications", tok, nil); c != want {
			t.Errorf("%.12s after the drop: %d, want %d", tok, c, want)
		}
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM people`).Scan(&n)
	if n != 0 {
		t.Fatalf("a user dropped from the file stays: %d", n)
	}

	// A browser signed in with the laptop keeps its access while the file
	// leaves the laptop alone, and is signed out when the file changes it.
	laptop := `{"tokens": [{"name": "laptop", "secret": "$CLRD_TEST_LAPTOP", "scopes": [%s]}]}`
	browser := mkToken(t, s, "browser", []string{ScopeRead}, nil)
	s.db.Exec(`INSERT INTO sessions (token_id, parent_id) SELECT b.id, l.id FROM tokens b, tokens l WHERE b.hash = ? AND l.hash = ?`,
		hashToken(browser), hashToken("clrd_laptoplaptoplaptop"))
	for _, step := range []struct {
		scopes string
		want   int
	}{{`"read"`, 200}, {`"read", "publish"`, 401}} {
		if err := apply(fmt.Sprintf(laptop, step.scopes)); err != nil {
			t.Fatal(err)
		}
		if c, _ := call(t, ts, "GET", "/api/v1/notifications", browser, nil); c != step.want {
			t.Fatalf("browser after the laptop got %s: %d, want %d", step.scopes, c, step.want)
		}
	}

	for body, want := range map[string]string{
		`null`:  "one JSON object",
		`{} {}`: "one JSON object",
		`{"users": [{"name": "a", "grants": [{"topik": "x"}]}]}`:                                "unknown field",
		`{"tokens": [{"name": "x", "secret": "xxxxxxxxxxxxxxxxxxxxxxxx", "scopes": ["read"]}]}`: "start with clrd_",
		`{"tokens": [{"name": "x", "secret": "short", "scopes": ["read"]}]}`:                    "at least 20",
		`{"tokens": [{"name": "x", "secret": "$CLRD_TEST_UNSET", "scopes": ["read"]}]}`:         "at least 20",
		`{"tokens": [{"name": "x", "secret": "clrd_xxxxxxxxxxxxxxxxxxxx", "scope": ["read"]}]}`: "unknown field",
		`{"tokens": [{"name": "x", "secret": "clrd_xxxxxxxxxxxxxxxxxxxx", "user": "nobody"}]}`:  "not in users",
		`{"users": [{"name": "a", "grants": [{"topic": "x", "scopes": ["admin"]}]}]}`:           "grant scope",
		`{"users": [{"name": "a"}, {"name": "a"}]}`:                                             "twice",
	} {
		if err := apply(body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
	if c, _ := call(t, ts, "GET", "/api/v1/notifications", "clrd_laptoplaptoplaptop", nil); c != 200 {
		t.Fatalf("a bad file changed the server: %d", c)
	}
}

// #440: the file manages a person made on the API but never deletes it, and
// a dropped user that a token made on the API still points at is kept, so
// the start goes on.
func TestConfigKeepsAPIPeople(t *testing.T) {
	s, ts, admin := newTest(t)
	if c, out := call(t, ts, "POST", "/api/v1/users", admin, map[string]any{"name": "ann", "grants": []map[string]any{{"topic": "work", "scopes": []string{"read"}}}}); c != 200 {
		t.Fatalf("make ann: %d %v", c, out)
	}
	f := filepath.Join(t.TempDir(), "clrd.json")
	s.opt.Config = f
	apply := func(body string) error {
		os.WriteFile(f, []byte(body), 0o600)
		return s.applyConfig(context.Background())
	}
	if err := apply(`{"users": [{"name": "ann", "grants": [{"topic": "home", "scopes": ["read"]}]}, {"name": "bob", "grants": [{"topic": "home", "scopes": ["read"]}]}]}`); err != nil {
		t.Fatal(err)
	}
	var grants string
	s.db.QueryRow(`SELECT grants FROM people WHERE name = 'ann'`).Scan(&grants)
	if !strings.Contains(grants, "home") {
		t.Fatalf("the file does not manage ann: %s", grants)
	}
	tok := mkToken(t, s, "bob's phone", []string{ScopeRead}, nil) // made on the API, for the file's bob
	s.db.Exec(`UPDATE tokens SET user_id = (SELECT id FROM people WHERE name = 'bob') WHERE hash = ?`, hashToken(tok))

	if err := apply(`{}`); err != nil {
		t.Fatalf("drop ann and bob: %v", err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM people WHERE name IN ('ann', 'bob') AND config = 0`).Scan(&n)
	if n != 2 {
		t.Fatalf("people kept and given back to the API: %d, want 2", n)
	}
	if c, _ := call(t, ts, "GET", "/api/v1/notifications?topic=home", tok, nil); c != 200 {
		t.Fatalf("bob's phone after the drop: %d", c)
	}
}

// #458: a file user never takes over an agent of the same name.
func TestConfigSparesAgent(t *testing.T) {
	s, ts, admin := newTest(t)
	_, iv := call(t, ts, "POST", "/api/v1/agents/invite", admin, map[string]any{"name": "bot", "topics": []string{"home"}})
	_, cl := call(t, ts, "POST", "/api/v1/agents/claim", "", map[string]string{"code": iv["code"].(string)})
	agent, _ := cl["token"].(string)
	f := filepath.Join(t.TempDir(), "clrd.json")
	s.opt.Config = f
	os.WriteFile(f, []byte(`{"users": [{"name": "bot", "grants": [{"topic": "*", "scopes": ["read", "publish"]}]}]}`), 0o600)
	if err := s.applyConfig(context.Background()); err == nil || !strings.Contains(err.Error(), "an agent has this name") {
		t.Fatalf("file took over an agent: %v", err)
	}
	if c, _ := call(t, ts, "POST", "/api/v1/notifications", agent, map[string]any{"topic": "work", "message": "x"}); c != 403 {
		t.Fatalf("agent got the file's grants: %d", c)
	}
}
