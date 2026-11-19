package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	b, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(b)
}

func TestPages(t *testing.T) {
	h := Handler(NewFake(), "https://clrd.example.com")
	for path, want := range map[string][]string{
		"/":               {"<h1>Dismiss once. Gone everywhere.</h1>", `og:image" content="https://clrd.example.com/assets/og.png`, "Uptime Kuma", "Alertmanager", "F-Droid", "github.com/thejdubb02/clrd"},
		"/docs/":          {"<h1>Docs</h1>", `href="/docs/spec"`},
		"/docs/spec":      {"clrd spec", `aria-current="page">clrd spec`},
		"/app":            {`data-view="inbox"`, "Nightly backup failed", "Urgent", "Acknowledged on pixel-9", "In sync on 3 devices", `data-act="dismiss"`},
		"/app/snoozed":    {"TLS certificate renews in 7 days", "Snoozed until"},
		"/app/cleared":    {"Cleared on pixel-9", "Resolved by gatus", `data-act="reopen"`},
		"/app/settings":   {"Devices in sync", "pixel-9", "https://clrd.example.com/api/v1/notifications"},
		"/app?q=disk":     {"Disk 91% full on web-1"},
		"/app?topic=home": {"Front door open", `<span class="in-topic">in home</span>`},
		"/robots.txt":     {"User-agent: GPTBot", "User-agent: ClaudeBot", "User-agent: PerplexityBot", "User-agent: OAI-SearchBot", "Sitemap: https://clrd.example.com/sitemap.xml"},
	} {
		code, body := get(t, h, path)
		if code != 200 {
			t.Errorf("%s: status %d", path, code)
		}
		for _, s := range want {
			if !strings.Contains(body, s) {
				t.Errorf("%s: missing %q", path, s)
			}
		}
	}
	if _, body := get(t, h, "/app?q=disk"); strings.Contains(body, "Nightly backup") {
		t.Error("search did not filter")
	}
	if code, _ := get(t, h, "/docs/nope"); code != 404 {
		t.Errorf("/docs/nope: %d, want 404", code)
	}
}

// Every internal link on every page resolves.
func TestLinks(t *testing.T) {
	h := Handler(NewFake(), "")
	href := regexp.MustCompile(`(?:href|src)="(/[^"]*)"`)
	seen := map[string]bool{}
	for _, page := range []string{"/", "/docs/", "/docs/spec", "/app", "/app/snoozed", "/app/cleared", "/app/settings"} {
		_, body := get(t, h, page)
		for _, m := range href.FindAllStringSubmatch(body, -1) {
			u := strings.ReplaceAll(strings.SplitN(m[1], "#", 2)[0], "&amp;", "&")
			if u == "" || u == "/assets/og.png" || seen[u] { // og.png is checked in TestAssets
				continue
			}
			seen[u] = true
			if code, _ := get(t, h, u); code != 200 {
				t.Errorf("%s links to %s: %d", page, u, code)
			}
		}
	}
	if len(seen) < 15 {
		t.Errorf("only %d links checked", len(seen))
	}
}

type deny struct{}

func (deny) Snapshot(*http.Request, Query) (Snapshot, error) { return Snapshot{}, ErrUnauthorized }

type leaky struct{}

func (leaky) Snapshot(*http.Request, Query) (Snapshot, error) {
	return Snapshot{Items: []Notification{{Title: "secret-title"}}}, ErrUnauthorized
}

func TestUnauthorized(t *testing.T) {
	for _, api := range []API{deny{}, leaky{}} {
		code, body := get(t, Handler(api, ""), "/app")
		if code != 401 || strings.Contains(body, "secret-title") || strings.Contains(body, "data-inbox") || !strings.Contains(body, `action="/api/v1/session"`) {
			t.Errorf("%T: status %d, page leaked data, rendered the inbox or has no sign-in form", api, code)
		}
	}
	if _, body := get(t, Handler(deny{}, ""), "/app?topic=home&auth=clrd_old"); !strings.Contains(body, `value="/app?topic=home"`) {
		t.Error("next must keep the filters and drop auth")
	}
}

type one struct{ n Notification }

func (o one) Snapshot(*http.Request, Query) (Snapshot, error) {
	return Snapshot{Items: []Notification{o.n}}, nil
}

// Notification fields come from anyone with a publish token.
func TestHostileNotification(t *testing.T) {
	n := Notification{ID: `x" onmouseover="alert(1)`, Topic: "a<b", Title: `<img src=x onerror=alert(1)>`, Message: "<script>alert(1)</script>",
		Click: "javascript:alert(1)", Actions: []Action{{Label: "go", URL: "javascript:alert(2)"}}, State: "active", CreatedAt: time.Now()}
	_, body := get(t, Handler(one{n}, ""), "/app")
	for _, bad := range []string{"<img src=x", "<script>alert", `href="javascript:`, `" onmouseover="`} {
		if strings.Contains(body, bad) {
			t.Errorf("rendered %q unescaped", bad)
		}
	}
	if !strings.Contains(body, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Error("title not shown escaped")
	}
}

// Handler sits beside the ntfy routes (docs/SPEC.md section 5): each side
// keeps its paths, and ours answer 200.
func TestMountBesideNtfy(t *testing.T) {
	mux := http.NewServeMux()
	ntfy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ntfy") })
	for _, p := range []string{"POST /{topic}", "PUT /{topic}", "POST /{$}", "GET /{topic}/json", "GET /{topic}/sse", "GET /{topic}/ws",
		"GET /{topic}/raw", "GET /{topic}/publish", "POST /{topic}/{id}/resolve", "POST /{topic}/{id}/ack", "GET /api/v1/ws", "GET /healthz"} {
		mux.Handle(p, ntfy)
	}
	mux.Handle("GET /", Handler(NewFake(), ""))
	for path, ours := range map[string]bool{"/": true, "/app": true, "/app/cleared": true, "/docs/spec": true, "/static/style.css": true,
		"/robots.txt": true, "/alerts/json": false, "/alerts/sse": false, "/healthz": false, "/api/v1/ws": false} {
		code, body := get(t, mux, path)
		if (body == "ntfy") == ours || (ours && code != 200) {
			t.Errorf("%s routed to the wrong side (%d)", path, code)
		}
	}
}

func TestAssets(t *testing.T) {
	h := Handler(NewFake(), "")
	for _, p := range []string{"/assets/logo.svg", "/assets/mark.svg", "/assets/og.png", "/assets/fonts/Geist-Variable.woff2", "/assets/fonts/GeistMono-Variable.woff2", "/assets/fonts/OFL.txt", "/favicon.svg"} {
		if code, _ := get(t, h, p); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP header: %q", csp)
	}
}

// The repo rule: no em dash, en dash or ellipsis character in anything we ship.
func TestNoBannedCharacters(t *testing.T) {
	check := func(name, s string) {
		for _, r := range []string{"\u2014", "\u2013", "\u2026"} {
			if strings.Contains(s, r) {
				t.Errorf("%s contains %U", name, []rune(r)[0])
			}
		}
	}
	h := Handler(NewFake(), "")
	for _, p := range []string{"/", "/docs/", "/docs/spec", "/app", "/app/snoozed", "/app/cleared", "/app/settings", "/robots.txt", "/static/style.css", "/static/inbox.js"} {
		_, body := get(t, h, p)
		check(p, body)
	}
	filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(".go .html .css .js .md .svg", filepath.Ext(p)) {
			b, _ := os.ReadFile(p)
			check(p, string(b))
		}
		return nil
	})
}
