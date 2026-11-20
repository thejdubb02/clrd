package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fresh install needs no CLRD_DB and no root: the default lives under the
// user's data folder, unless a server already uses the old /data path (#242).
func TestDefaultDB(t *testing.T) {
	dir := t.TempDir()
	old := legacyDB
	legacyDB = filepath.Join(dir, "legacy", "clrd.db")
	t.Cleanup(func() { legacyDB = old })

	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
	if got, want := DefaultDB(), filepath.Join(dir, "xdg", "clrd", "clrd.db"); got != want {
		t.Fatalf("with XDG_DATA_HOME: %q, want %q", got, want)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", filepath.Join(dir, "home"))
	if got, want := DefaultDB(), filepath.Join(dir, "home", ".local", "share", "clrd", "clrd.db"); got != want {
		t.Fatalf("with HOME: %q, want %q", got, want)
	}
	os.MkdirAll(filepath.Dir(legacyDB), 0o700)
	os.WriteFile(legacyDB, nil, 0o600)
	if got := DefaultDB(); got != legacyDB {
		t.Fatalf("with the old file present: %q, want %q", got, legacyDB)
	}
}

// With CLRD_BASE_URL unset, a pairing link uses the address the browser came
// in on (scheme from the browser's Origin, which a TLS proxy would hide), so a
// phone on the LAN can open it; set, the setting wins.
func TestPairLinkFollowsHost(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"", "https://192.168.1.50:8080/pair?c="},
		{"https://clrd.example.com", "https://clrd.example.com/pair?c="},
	} {
		s, err := New(Options{DB: filepath.Join(t.TempDir(), "clrd.db"), BaseURL: tc.base,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "http://192.168.1.50:8080/api/v1/pair", nil)
		req.Header.Set("Origin", "https://192.168.1.50:8080")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		s.Close()
		var out struct{ URL string }
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != 201 || !strings.HasPrefix(out.URL, tc.want) {
			t.Fatalf("base %q: %d url %q, want it to start %s", tc.base, rec.Code, out.URL, tc.want)
		}
	}
}

// #254: a GET page (pair, link) has no Origin, so behind a TLS proxy the
// scheme comes from X-Forwarded-Proto, but only from a trusted proxy.
func TestLinkSchemeBehindProxy(t *testing.T) {
	s, err := New(Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct{ peer, proto, want string }{
		{"10.0.0.2:5000", "https", "https://clrd.example.com"},
		{"10.0.0.2:5000", "", "http://clrd.example.com"},
		{"10.0.0.2:5000", "http, https", "https://clrd.example.com"}, // the client's own value comes first

		{"203.0.113.9:5000", "https", "http://clrd.example.com"}, // not a proxy: ignored
	} {
		req := httptest.NewRequest("GET", "http://clrd.example.com/pair?c=x", nil)
		req.RemoteAddr = tc.peer
		if tc.proto != "" {
			req.Header.Set("X-Forwarded-Proto", tc.proto)
		}
		if got := s.baseFor(req); got != tc.want {
			t.Errorf("peer %s proto %q: %q, want %q", tc.peer, tc.proto, got, tc.want)
		}
	}
}

// A request with no token is told how to get one.
func TestNoTokenSaysWhatToDo(t *testing.T) {
	_, ts, _ := newTest(t)
	code, out := call(t, ts, "GET", "/api/v1/notifications", "", nil)
	if msg, _ := out["error"].(string); code != 401 || !strings.Contains(msg, "clrd token create") {
		t.Fatalf("%d %v", code, out)
	}
}
