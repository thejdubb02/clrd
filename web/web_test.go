package web

import (
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		"/":                          {"<h1>Dismiss once. Gone everywhere.</h1>", `og:image" content="https://clrd.example.com/assets/og.png`, "Uptime Kuma", "Alertmanager", "F-Droid", "github.com/thejdubb02/clrd"},
		"/docs/":                     {"<h1>Docs</h1>", `href="/docs/spec"`},
		"/docs/spec":                 {"clrd spec", `aria-current="page">clrd spec`},
		"/docs/integrations/":        {`href="/docs/integrations/curl"`, `href="/docs/integrations/mcp"`},
		"/docs/integrations/grafana": {`href="/docs/integrations/alertmanager"`},
		"/app":                       {`data-view="inbox"`, "Nightly backup failed", "Urgent", "Acknowledged on pixel-9", "In sync on 3 devices", `data-act="dismiss"`},
		"/app/snoozed":               {"TLS certificate renews in 7 days", "Snoozed until"},
		"/app/cleared":               {"Cleared on pixel-9", "Resolved by gatus", `data-act="reopen"`},
		"/app/settings":              {"Devices in sync", "pixel-9", "https://clrd.example.com/api/v1/notifications"},
		"/app?q=disk":                {"Disk 91% full on web-1"},
		"/app?topic=home":            {"Front door open", `<span class="in-topic">in home</span>`},
		"/robots.txt":                {"User-agent: GPTBot", "User-agent: ClaudeBot", "User-agent: PerplexityBot", "User-agent: OAI-SearchBot", "Sitemap: https://clrd.example.com/sitemap.xml"},
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
	if _, body := get(t, h, "/app?topic=home"); strings.Contains(body, "Nightly backup") || strings.Contains(body, "Disk 91%") {
		t.Error("topic filter shows other topics")
	}
	if code, _ := get(t, h, "/docs/SPEC.MD"); code != 200 {
		t.Errorf("/docs/SPEC.MD: %d, want 200", code)
	}
	if code, _ := get(t, h, "/docs/nope"); code != 404 {
		t.Errorf("/docs/nope: %d, want 404", code)
	}
}

// Every internal link on every page resolves, relative ones from the page they
// are on, and every #fragment names an id on the target page.
func TestLinks(t *testing.T) {
	h := Handler(NewFake(), "")
	href := regexp.MustCompile(`(?:href|src)="([^"]*)"`)
	seen := map[string]bool{}
	pages := []string{"/", "/docs/", "/app", "/app/snoozed", "/app/cleared", "/app/settings"}
	for _, d := range docList {
		pages = append(pages, "/docs/"+d.Key)
	}
	for _, page := range pages {
		_, body := get(t, h, page)
		for _, m := range href.FindAllStringSubmatch(body, -1) {
			ref, err := url.Parse(html.UnescapeString(m[1]))
			// A doc's link into the repo goes to GitHub: the path must exist here, and must not be a doc.
			if repo, ok := strings.CutPrefix(ref.Path, "/thejdubb02/clrd/tree/main/"); ok && ref.Host == "github.com" {
				if _, err := os.Stat("../" + repo); err != nil || strings.HasPrefix(repo, "docs/") {
					t.Errorf("%s links to %s: not a repo path outside docs/", page, ref)
				}
				continue
			}
			if err != nil || ref.Host != "" || ref.Scheme != "" || ref.String() == "/assets/og.png" { // og.png is checked in TestAssets
				continue
			}
			u := (&url.URL{Path: page}).ResolveReference(ref)
			if seen[u.String()] {
				continue
			}
			seen[u.String()] = true
			code, target := get(t, h, u.RequestURI())
			if code != 200 {
				t.Errorf("%s links to %s: %d", page, u, code)
			}
			if u.Fragment != "" && !strings.Contains(target, `id="`+u.Fragment+`"`) {
				t.Errorf("%s links to %s: no element with that id", page, u)
			}
		}
	}
	if len(seen) < 25 {
		t.Errorf("only %d links checked", len(seen))
	}
}

// readOnly is for page tests that never act.
type readOnly struct{}

func (readOnly) Act(*http.Request, string, string, *time.Time) error { return ErrUnauthorized }

type deny struct{ readOnly }

func (deny) Snapshot(*http.Request, Query) (Snapshot, error) { return Snapshot{}, ErrUnauthorized }

type leaky struct{ readOnly }

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
	if _, body := get(t, Handler(deny{}, ""), "/app?topic=home&auth=clrd_old"); !strings.Contains(body, `value="/app?topic=home"`) || strings.Contains(body, `role="alert"`) {
		t.Error("next must keep the filters and drop auth, with no error shown")
	}
	if _, body := get(t, Handler(deny{}, ""), "/app?error=token&topic=home&name=work+%22laptop%22"); !strings.Contains(body, `placeholder="work laptop" value="work &#34;laptop&#34;">`) || !strings.Contains(body, `value="/app?topic=home"`) {
		t.Errorf("failed sign-in: the typed device name is not kept, or leaks into next")
	}
	if _, body := get(t, Handler(deny{}, ""), "/app?name=evil"); strings.Contains(body, `value="evil"`) {
		t.Errorf("a name in a plain link prefills the sign-in form")
	}
	if _, body := get(t, Handler(deny{}, ""), "/app?error=token&topic=home"); !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `value="/app?topic=home"`) {
		t.Error("a bad token must show the error, and next must drop it")
	}
}

type one struct {
	readOnly
	n Notification
}

func (o one) Snapshot(*http.Request, Query) (Snapshot, error) {
	return Snapshot{Items: []Notification{o.n}}, nil
}

// The card buttons are a form, so Ack, Snooze and Clear work without JavaScript.
func TestActForm(t *testing.T) {
	f := NewFake()
	h := Handler(f, "")
	id := func(title string) string {
		for _, n := range f.items {
			if n.Title == title {
				return n.ID
			}
		}
		t.Fatalf("no %q", title)
		return ""
	}
	door := id("Front door open for 10 minutes")
	if _, body := get(t, h, "/app?topic=home"); !strings.Contains(body, `action="/app/act"`) || !strings.Contains(body, `name="next" value="/app?topic=home"`) ||
		!strings.Contains(body, `name="snooze" value="4h"`) || !strings.Contains(body, `name="act" value="dismiss"`) {
		t.Fatal("cards have no action form that returns to this page")
	}
	post := func(form url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/app/act", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(w, r)
		return w
	}
	w := post(url.Values{"id": {door}, "snooze": {"4h"}, "next": {"/app?topic=home"}})
	if n := f.items[door]; w.Code != 303 || w.Header().Get("Location") != "/app?topic=home" || n.State != "snoozed" ||
		n.SnoozeUntil == nil || n.SnoozeUntil.Sub(time.Now()) < 239*time.Minute || n.SnoozeUntil.Sub(time.Now()) > 4*time.Hour {
		t.Fatalf("snooze 4h: %d to %q, state %s until %v", w.Code, w.Header().Get("Location"), n.State, n.SnoozeUntil)
	}
	for _, next := range []string{"//evil.example/app", "https://evil.example/app", "", "/app/../\\evil.example", "/apple", "/app/..%2f..%2f"} {
		if w := post(url.Values{"id": {door}, "act": {"dismiss"}, "next": {next}}); w.Code != 303 || w.Header().Get("Location") != "/app" {
			t.Errorf("next %q: %d to %q, want /app", next, w.Code, w.Header().Get("Location"))
		}
	}
	if f.items[door].State != "dismissed" {
		t.Fatal("Clear did not dismiss")
	}
	for _, form := range []url.Values{{"id": {door}, "act": {"delete"}}, {"id": {door}, "snooze": {"-1h"}}, {"id": {door}, "snooze": {"tonight"}}, {"id": {"nope"}, "act": {"ack"}}} {
		if w := post(form); w.Code != 400 {
			t.Errorf("%v: %d, want 400", form, w.Code)
		}
	}
	if w := post(url.Values{"id": {door}, "act": {"ack"}, "pad": {strings.Repeat("x", 80<<10)}}); w.Code != 400 {
		t.Errorf("oversized form: %d, want 400", w.Code)
	}
	h = Handler(deny{}, "")
	if w := post(url.Values{"id": {"x"}, "act": {"ack"}, "next": {"/app/cleared"}}); w.Code != 303 || w.Header().Get("Location") != "/app/cleared" {
		t.Errorf("signed out: %d to %q, want back to the page (it shows sign-in)", w.Code, w.Header().Get("Location"))
	}
}

// Notification fields come from anyone with a publish token.
func TestHostileNotification(t *testing.T) {
	n := Notification{ID: `x" onmouseover="alert(1)`, Topic: "a<b", Title: `<img src=x onerror=alert(1)>`, Message: "<script>alert(1)</script>",
		Click: "javascript:alert(1)", Actions: []Action{{Label: "go", URL: "javascript:alert(2)"}}, State: "active", CreatedAt: time.Now()}
	_, body := get(t, Handler(one{n: n}, ""), "/app")
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
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
	}
	// The CSP forbids inline script and style, so none may be on a page.
	inline := regexp.MustCompile(`<script>|<script [^>]*>[^<]|<style|\sstyle="|\son[a-z]+="`)
	for _, p := range []string{"/", "/docs/", "/docs/spec", "/app", "/app/snoozed", "/app/cleared", "/app/settings"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		for k, v := range want {
			if got := w.Header().Get(k); got != v {
				t.Errorf("%s: %s is %q, want %q", p, k, got, v)
			}
		}
		if m := inline.FindString(w.Body.String()); m != "" {
			t.Errorf("%s: inline %q is blocked by the CSP", p, m)
		}
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
		if err == nil && !d.IsDir() && strings.Contains(".go .html .css .js .mjs .md .svg", filepath.Ext(p)) {
			b, _ := os.ReadFile(p)
			check(p, string(b))
		}
		return nil
	})
}
