package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func copyActionTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, err := New(Options{DB: filepath.Join(t.TempDir(), "clrd.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, mkToken(t, s, "admin", []string{ScopeAdmin}, nil)
}

func TestNtfyCopyActions(t *testing.T) {
	s, admin := copyActionTestServer(t)
	for _, tc := range []struct {
		name, path, body, header, label, value string
		clear                                  bool
	}{
		{"short", "/short", "x", "copy, Copy code, 123456", "Copy code", "123456", false},
		{"named", "/named", "x", "copy, Copy code, value=' a,b ', clear=true", "Copy code", " a,b ", true},
		{"json", "/", `{"topic":"json","message":"x","actions":[{"action":"copy","label":"Copy code","value":"123456","clear":false},{"action":"copy","label":"Empty"},{"action":"copy","label":"Big","value":"` + strings.Repeat("x", maxCopyText+1) + `"}]}`, "", "Copy code", "123456", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.SetBasicAuth("", admin)
			if tc.header != "" {
				req.Header.Set("Actions", tc.header)
			}
			res := httptest.NewRecorder()
			s.ServeHTTP(res, req)
			if res.Code != 200 {
				t.Fatalf("publish: %d %s", res.Code, res.Body)
			}
			check := func(out string) {
				t.Helper()
				m := ntfyDecode(t, out)
				if len(m.Actions) != 1 {
					t.Fatalf("want one action: %s", out)
				}
				a := m.Actions[0]
				if a.Action != "copy" || a.Label != tc.label || a.Value != tc.value || a.Clear != tc.clear || a.URL != "" {
					t.Fatalf("wrong copy action: %s", out)
				}
			}
			check(res.Body.String())

			req = httptest.NewRequest("GET", "/"+tc.name+"/json?poll=1", nil)
			req.SetBasicAuth("", admin)
			res = httptest.NewRecorder()
			s.ServeHTTP(res, req)
			if res.Code != 200 || len(lines(res.Body.String())) != 1 {
				t.Fatalf("poll: %d %s", res.Code, res.Body)
			}
			check(res.Body.String())
		})
	}
}

func TestNativeCopyAction(t *testing.T) {
	s, admin := copyActionTestServer(t)
	publish := func(action string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/notifications",
			strings.NewReader(`{"topic":"copy","message":"x","actions":[`+action+`]}`))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		s.ServeHTTP(res, req)
		return res
	}
	res := publish(`{"label":"Copy","copy":"x"}`)
	if res.Code != 200 {
		t.Fatalf("publish copy: %d %s", res.Code, res.Body)
	}
	var out Notification
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil || len(out.Actions) != 1 || out.Actions[0].Copy != "x" {
		t.Fatalf("missing copy action: %s", res.Body)
	}
	if res := publish(`{"label":"Copy","copy":"x","url":"https://example.com"}`); res.Code != 400 {
		t.Fatalf("publish copy with url: %d %s", res.Code, res.Body)
	}
	if res := publish(`{"label":"Copy","copy":"` + strings.Repeat("x", 4097) + `"}`); res.Code != 400 {
		t.Fatalf("publish oversized copy: %d %s", res.Code, res.Body)
	}
}
