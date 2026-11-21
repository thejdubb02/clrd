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
