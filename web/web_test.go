package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thejdubb02/clrd"
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
		"/":                          {">Dismiss once. Gone everywhere.</h1>", `og:image" content="https://clrd.example.com/assets/og.png`, "Uptime Kuma", "Alertmanager", "F-Droid", "github.com/thejdubb02/clrd"},
		"/docs/":                     {"<h1>Docs</h1>", `href="/docs/spec"`},
		"/docs/spec":                 {"clrd spec", `aria-current="page">clrd spec`},
		"/docs/changelog":            {"<h2 id=\"unreleased\">Unreleased</h2>", `full history on GitHub</a>`},
		"/docs/integrations/":        {`href="/docs/integrations/curl"`, `href="/docs/integrations/mcp"`},
		"/docs/integrations/grafana": {`href="/docs/integrations/alertmanager"`},
		"/app":                       {`data-view="inbox"`, "Nightly backup failed", "Urgent", "Acknowledged on pixel-9", "In sync on 2 devices", `aria-label="pixel-9: connected"`, `aria-label="tablet: offline"`, `href="/app/settings#dev-h"`, `data-act="dismiss"`},
		"/app/snoozed":               {"TLS certificate renews in 7 days", "Snoozed until"},
		"/app/cleared":               {"Cleared on pixel-9", "Resolved by gatus", `data-act="reopen"`},
		"/app/ui":                    {"<h1 id=\"h-intro\" class=\"text-2xl font-semibold tracking-tight\">Components</h1>", `href="/static/app.css?v=`, `data-variant="clear"`, `class="command-dialog"`},
		"/app/settings":              {"Linked devices", "Phone, linked to web", `data-unlink="tok-pixel"`, "pixel-9 linked by web", `data-live="linked"`, `data-unlink="tok-bot"`, "build-bot</span>\n              <div class=\"meta\">Agent</div>", `data-agent hidden`, `<option value="15m" selected>15 minutes</option>`, `name="topic" value="home"`, ">Status</th>", "Offline", "web</span> <span class=\"text-muted-foreground\">(this browser)</span>", "https://clrd.example.com/api/v1/notifications", `<option value="14" selected>14 days</option>`, `data-tkeep="gone=3;home=14;"`, `<select id="tk-gone" class="input w-auto" data-topic="gone">`, `data-approve="tok-laptop" data-name="laptop" data-own aria-label="May approve: laptop"><option value="denied" selected>Denied</option></select>`, `href="https://github.com/thejdubb02/clrd">Source code</a>`, `href="https://github.com/thejdubb02/clrd/issues">Report a problem</a>`, `data-approve="tok-pixel" data-name="pixel-9" aria-label="May approve: pixel-9"><option value="denied">Denied</option><option value="ask" selected>Ask</option><option value="allowed">Allowed</option></select>`},
		"/app?q=disk":                {"Disk 91% full on web-1"},
		"/app?topic=home":            {"Front door open", `data-topic>in home</span>`},
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
	if _, body := get(t, h, "/app/settings"); strings.Contains(body, `data-unlink="tok-web"`) || strings.Contains(body, `data-unlink="tok-laptop"`) {
		t.Error("unlink offered on this browser or a typed-in token")
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

func (readOnly) Act(*http.Request, string, string, *time.Time) error      { return ErrUnauthorized }
func (readOnly) Reply(*http.Request, string, string, string, int64) error { return ErrUnauthorized }
func (readOnly) Mark(*http.Request, string, string, string, string) error { return ErrUnauthorized }

func (readOnly) StartSilence(*http.Request, string, string, string, string) error {
	return ErrUnauthorized
}

func (readOnly) EndSilence(*http.Request, string) error { return ErrUnauthorized }

func (readOnly) FormatIgnore(*http.Request, string, string, string, bool) error {
	return ErrUnauthorized
}

func (readOnly) SaveHook(*http.Request, string, string, string, []string, *string, string, bool) error {
	return ErrUnauthorized
}

func (readOnly) DeleteHook(*http.Request, string, string) error { return ErrUnauthorized }

func (readOnly) SaveTemplate(*http.Request, Template, string, bool) error { return ErrUnauthorized }
func (readOnly) SetSenderRules(*http.Request, string, SenderRules, int64) error {
	return ErrUnauthorized
}
func (readOnly) UndoSenderChange(*http.Request, string, int64) error          { return ErrUnauthorized }
func (readOnly) DeleteTemplate(*http.Request, string, string) error           { return ErrUnauthorized }
func (readOnly) SetSubscriptions(*http.Request, *bool, map[string]bool) error { return ErrUnauthorized }

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

type none struct{ readOnly }

func (none) Snapshot(*http.Request, Query) (Snapshot, error) { return Snapshot{}, nil }

// An empty first run shows the send-a-test tip, with this instance's address.
func TestFirstRunTip(t *testing.T) {
	_, body := get(t, Handler(none{}, ""), "/app")
	if !strings.Contains(body, `aria-label="Send your first notification" data-tip>`) {
		t.Error("first run must lead with the send-a-test tip")
	}
	all := len(tips("x", "", nil))
	if strings.Count(body, " data-tip hidden") != all-1 || !strings.Contains(body, "http://example.com/api/v1/notifications") {
		t.Error("one tip shows, and it copies this instance's address")
	}
	if n := strings.Count(body, `text-muted-foreground shrink-0 [&_svg]:size-[18px]"><svg`); n != all {
		t.Errorf("every tip has its icon: %d of %d", n, all)
	}
	if strings.Contains(body, "Give each topic an icon") || !strings.Contains(body, `{&#34;topic&#34;:&#34;test&#34;`) {
		t.Error("no topics: no topic icon tip, and the test goes to the test topic")
	}
	_, body = get(t, Handler(none{}, ""), "/app?topic=builds")
	if !strings.Contains(body, `{&#34;topic&#34;:&#34;builds&#34;`) {
		t.Error("a topic view sends its test to that topic")
	}
	_, body = get(t, Handler(none{}, ""), "/app?topic=x%27%3Brm")
	if !strings.Contains(body, `{&#34;topic&#34;:&#34;test&#34;`) {
		t.Error("an invalid topic must not reach the shell command")
	}
	if len(tips("x", "", []Topic{{Name: "a"}})) != all+1 {
		t.Error("with topics, the topic icon tip is back")
	}
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
	// A reply button posts its answer and the seq the card was drawn at.
	agent := id("Agent wants approval")
	if _, body := get(t, h, "/app?topic=agents"); !strings.Contains(body, `name="reply" value="yes" data-act="reply">Approve</button>`) || strings.Contains(body, ` data-links>`) {
		t.Fatal("reply actions are not answer buttons, or show as empty links")
	}
	// Device actions (#450) are shown as text, never as a link or a button the browser runs.
	if _, body := get(t, h, "/app?topic=infra"); !strings.Contains(body, `Clean /tmp</span> runs on a phone: POST https://ops.example.com/clean?host=web-1</li>`) ||
		!strings.Contains(body, `Ping</span> runs on a phone: Android broadcast (the app&#39;s default)</li>`) || strings.Contains(body, `href="https://ops.example.com`) {
		t.Fatal("device actions missing, or rendered as links")
	}
	seq := strconv.FormatInt(f.items[agent].Seq, 10)
	if w := post(url.Values{"id": {agent}, "reply": {"yes"}, "seq": {seq}, "next": {"/app?topic=agents"}}); w.Code != 303 || f.items[agent].Reply != "yes" || f.items[agent].State != "acknowledged" {
		t.Fatalf("reply: %d, stored %q state %s", w.Code, f.items[agent].Reply, f.items[agent].State)
	}
	if _, body := get(t, h, "/app?topic=agents"); !strings.Contains(body, "Answered Approve on web") || strings.Contains(body, `name="reply"`) {
		t.Fatal("answered card still offers the buttons or hides the answer")
	}
	if w := post(url.Values{"id": {agent}, "reply": {"no"}, "seq": {seq}}); w.Code != 400 || f.items[agent].Reply != "yes" {
		t.Fatalf("second answer at a stale seq: %d, stored %q, want 400 and the first answer kept", w.Code, f.items[agent].Reply)
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

// A card shows who marked it, and a mark never hides it (card marks).
func TestMarks(t *testing.T) {
	n := Notification{ID: "m1", Topic: "triage", Title: "disk", State: "active", CreatedAt: time.Now(), Marks: []Mark{
		{By: "stew", Mark: "actioned", Note: "filed relay#512", Link: "https://github.com/x/relay/issues/512"},
		{By: "Ann", Mark: "seen"},
		{By: "eve", Mark: "actioned", Link: "javascript:alert(1)"},
	}}
	_, body := get(t, Handler(one{n: n}, ""), "/app")
	for _, want := range []string{`data-mark="actioned">stew actioned: filed relay#512 <a class="underline" href="https://github.com/x/relay/issues/512"`, `data-mark="seen">Ann seen</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, `href="javascript:`) {
		t.Error("a mark link rendered a script URL")
	}
}

type formatted struct{ one }

func (f formatted) Snapshot(r *http.Request, q Query) (Snapshot, error) {
	s, err := f.one.Snapshot(r, q)
	s.Formatting = true
	return s, err
}

// An untitled multi-line message heads its card with its first line as plain
// text and renders the rest as the body (#303).
func TestUntitledMessage(t *testing.T) {
	n := Notification{ID: "u1", Topic: "ci", Message: "\n**Build** `main` failed\nStep *test* exited 1.\n- lint ok", State: "active", CreatedAt: time.Now()}
	_, body := get(t, Handler(one{n: n}, ""), "/app")
	if !strings.Contains(body, `id="t-u1">**Build** `+"`main`"+` failed</h2>`) {
		t.Error("with formatting off, the heading is not the first line as typed")
	}
	_, body = get(t, Handler(formatted{one{n: n}}, ""), "/app")
	if !strings.Contains(body, `id="t-u1">Build main failed</h2>`) {
		t.Error("heading is not the first line as plain text")
	}
	if !strings.Contains(body, "Step <em>test</em> exited 1.") || !strings.Contains(body, "<li>lint ok</li>") {
		t.Error("body is not the rest of the message, rendered")
	}
	for _, c := range []struct{ msg, format, head, rest string }{
		{"one line", "", "one line", ""},
		{"*a* <b>\nnext", "text", "*a* <b>", "next"},
		{"---\nx", "", "---", "x"},
		{"Build failed\n    go test ./...", "", "Build failed", "    go test ./..."},
	} {
		m := Notification{Message: c.msg, Format: c.format}
		if m.Heading() != c.head || m.Body() != c.rest {
			t.Errorf("%q: got %q / %q", c.msg, m.Heading(), m.Body())
		}
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
	for path, ours := range map[string]bool{"/": true, "/app": true, "/app/cleared": true, "/docs/spec": true, "/static/app.css": true,
		"/robots.txt": true, "/alerts/json": false, "/alerts/sse": false, "/healthz": false, "/api/v1/ws": false} {
		code, body := get(t, mux, path)
		if (body == "ntfy") == ours || (ours && code != 200) {
			t.Errorf("%s routed to the wrong side (%d)", path, code)
		}
	}
}

// A page names every script and stylesheet by its content, so after an upgrade
// it never runs with an older cached file (#410).
func TestStaticVersioned(t *testing.T) {
	h := Handler(NewFake(), "")
	ref := regexp.MustCompile(`(?:src|href)="/static/([^"?]+)([^"]*)"`)
	named := map[string]bool{}
	for _, p := range []string{"/", "/docs/spec", "/app", "/app/ui", "/app/settings"} {
		_, body := get(t, h, p)
		for _, m := range ref.FindAllStringSubmatch(body, -1) {
			b, err := files.ReadFile("static/" + m[1])
			if err != nil {
				t.Fatalf("%s names a missing file %s", p, m[1])
			}
			sum := sha256.Sum256(b)
			if want := "?v=" + hex.EncodeToString(sum[:6]); m[2] != want {
				t.Errorf("%s: /static/%s%s, want %s", p, m[1], m[2], want)
			}
			named[p+" "+m[1]] = true
		}
	}
	for _, k := range []string{"/ app.css", "/app app.css", "/app inbox.js", "/app vendor/basecoat.min.js", "/app vendor/command.min.js",
		"/app/ui app.css", "/app/ui vendor/basecoat.min.js", "/app/ui vendor/command.min.js", "/app/settings pair.js"} {
		if !named[k] {
			t.Errorf("%s: not named", k)
		}
	}

	// Each GET runs once with the current hash, an old one and none.
	do := func(path, etag string) *http.Response {
		r := httptest.NewRequest("GET", path, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Result()
	}
	v := staticHash["inbox.js"]
	for path, want := range map[string]string{
		"/static/inbox.js?v=" + v:   "public, max-age=31536000, immutable",
		"/static/inbox.js?v=000000": "no-cache",
		"/static/inbox.js":          "no-cache",
	} {
		res := do(path, "")
		if res.StatusCode != 200 || res.Header.Get("Cache-Control") != want || res.Header.Get("ETag") != `"`+v+`"` {
			t.Errorf("%s: %d %q %q, want 200 %q", path, res.StatusCode, res.Header.Get("Cache-Control"), res.Header.Get("ETag"), want)
		}
	}
	if res := do("/static/inbox.js", `"`+v+`"`); res.StatusCode != 304 {
		t.Errorf("revalidate with the current ETag: %d, want 304", res.StatusCode)
	}
	if res := do("/static/inbox.js", `"000000"`); res.StatusCode != 200 {
		t.Errorf("revalidate with an old ETag: %d, want 200", res.StatusCode)
	}
	if res := do("/app/ui", ""); res.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("/app/ui Cache-Control %q, want no-cache", res.Header.Get("Cache-Control"))
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
		"Content-Security-Policy": "default-src 'self'; img-src 'self' data: https: http:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
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

// Every shared sound is offered in Settings and served (#282, SPEC 2.2).
func TestSounds(t *testing.T) {
	h := Handler(NewFake(), "")
	_, page := get(t, h, "/app/settings")
	wavs, _ := fs.Glob(clrd.Files, "assets/sounds/*.wav")
	if len(wavs) == 0 {
		t.Fatal("no sounds in assets/sounds")
	}
	for _, opt := range append([]string{"default", "none"}, wavs...) {
		name := strings.TrimSuffix(filepath.Base(opt), ".wav")
		if !strings.Contains(page, `<option value="`+name+`">`) {
			t.Errorf("settings does not offer %q", name)
		}
		if strings.HasSuffix(opt, ".wav") {
			if code, _ := get(t, h, "/assets/sounds/"+name+".wav"); code != 200 {
				t.Errorf("%s: %d", name, code)
			}
		}
	}
}

// Wrappers like Ferdium and home screens never read the SVG icon (#243).
func TestIcons(t *testing.T) {
	h := Handler(NewFake(), "")
	for p, ct := range map[string]string{"/favicon.svg": "image/svg+xml", "/favicon.ico": "image/x-icon",
		"/apple-touch-icon.png": "image/png", "/manifest.webmanifest": "application/manifest+json",
		"/assets/icon-32.png": "image/png", "/assets/icon-192.png": "image/png", "/assets/icon-512.png": "image/png"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != ct || w.Body.Len() == 0 {
			t.Errorf("%s: %d %q, want 200 %q", p, w.Code, w.Header().Get("Content-Type"), ct)
		}
	}
	var m struct {
		Icons []struct{ Src string }
	}
	_, body := get(t, h, "/manifest.webmanifest")
	if err := json.Unmarshal([]byte(body), &m); err != nil || len(m.Icons) != 2 {
		t.Fatalf("manifest: %v %+v", err, m)
	}
	for _, i := range m.Icons {
		if code, _ := get(t, h, "/"+i.Src); code != 200 {
			t.Errorf("manifest icon %s: %d", i.Src, code)
		}
	}
	links := []string{`<link rel="icon" href="/favicon.svg"`, `<link rel="icon" href="/assets/icon-32.png"`, `<link rel="icon" href="/favicon.ico"`,
		`<link rel="apple-touch-icon" href="/apple-touch-icon.png"`, `<link rel="manifest" href="/manifest.webmanifest"`}
	for _, p := range []string{"/", "/docs/", "/docs/spec", "/app", "/app/settings", "/app/ui"} {
		_, body := get(t, h, p)
		last := -1
		for _, l := range links {
			i := strings.Index(body, l)
			if i < 0 || i < last {
				t.Errorf("%s: missing or out of order %s", p, l)
			}
			last = i
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
	for _, p := range []string{"/", "/docs/", "/docs/spec", "/app", "/app/snoozed", "/app/cleared", "/app/settings", "/robots.txt", "/static/app.css", "/static/inbox.js"} {
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

// Only types the server serves inline show as an image; a BMP would download.
func TestAttachmentImage(t *testing.T) {
	for typ, want := range map[string]bool{"image/png": true, "image/webp": true, "image/bmp": false, "image/svg+xml": false, "": false} {
		if got := (Attachment{Type: typ}).Image(); got != want {
			t.Errorf("%q: Image() = %v, want %v", typ, got, want)
		}
	}
	// External files (no type) go by the name or the link's path (#199).
	for _, c := range []struct {
		a    Attachment
		want bool
	}{
		{Attachment{URL: "https://example.com/graph.PNG"}, true},
		{Attachment{URL: "https://example.com/g?id=1", Name: "graph.jpg"}, true},
		{Attachment{URL: "https://example.com/g.webp?size=2#top"}, true},
		{Attachment{URL: "https://example.com/r.pdf", Name: "r.pdf"}, false},
		{Attachment{URL: "https://example.com/x?f=a.png"}, false},
		{Attachment{URL: "https://example.com/i.svg"}, false},
		{Attachment{URL: "/api/v1/files/1", Name: "a.png", Type: "application/octet-stream"}, false},
	} {
		if got := c.a.Image(); got != c.want {
			t.Errorf("%+v: Image() = %v, want %v", c.a, got, c.want)
		}
	}
}

func TestSyncDots(t *testing.T) {
	s := Snapshot{Linked: []LinkedDevice{{Name: "cli", Kind: "token"}}}
	for i := range 7 {
		s.Linked = append(s.Linked, LinkedDevice{Name: string(rune('a' + i)), Kind: "phone", Connected: i < 2, CaughtUp: i == 0})
	}
	s.Linked = append(s.Linked, LinkedDevice{Name: "me", Kind: "browser", This: true})
	d := s.Sync()
	if len(d.Dots) != 6 || d.More != 2 || d.Live != 2 || d.Dots[0].Name != "me" || d.Dots[1].Status() != "connected" || d.Dots[2].Status() != "catching up" || d.Dots[3].Status() != "offline" {
		t.Errorf("got %+v", d)
	}
}

// The May approve menu never offers a raise on this browser or anything it
// hangs under, two hops up too, walked by id even when names repeat (#329).
func TestApproveOptions(t *testing.T) {
	s := Snapshot{MeID: "b", Linked: []LinkedDevice{
		{ID: "t", Name: "laptop", Kind: "token", Approve: "denied"},
		{ID: "p", Name: "pixel", Kind: "phone", ViaID: "t", Approve: "ask"},
		{ID: "b", Name: "web", Kind: "browser", ViaID: "p", Approve: "allowed", This: true},
		{ID: "o", Name: "laptop", Kind: "token", Approve: "denied"}, // same name as t, not in the tree
	}}
	vals := func(d LinkedDevice) (v string) {
		for _, o := range s.ApproveOptions(d) {
			v += o.Value
			if o.Selected {
				v += "*"
			}
			v += " "
		}
		return v
	}
	for i, want := range []string{"denied* ", "denied ask* ", "denied ask allowed* ", "denied* ask allowed "} {
		if got := vals(s.Linked[i]); got != want {
			t.Errorf("%s: options %q, want %q", s.Linked[i].ID, got, want)
		}
	}
	if !s.Own(s.Linked[0]) || s.Own(s.Linked[3]) {
		t.Error("Own: wrong tree")
	}
}

func TestTimeLeft(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second:                "under a minute",
		12*time.Minute + 10*time.Second: "12m",
		2*time.Hour + 5*time.Minute:     "2h 5m",
		76*time.Hour + 20*time.Second:   "3d 4h",
	} {
		if got := timeLeft(time.Now().Add(d)); got != want {
			t.Errorf("timeLeft(+%v) = %q, want %q", d, got, want)
		}
	}
}

// Topic subscriptions (SPEC 2.3): the card menu, the not-subscribed banner
// and the Settings list, all as plain form posts.
func TestSubscriptions(t *testing.T) {
	f := NewFake()
	h := Handler(f, "")
	post := func(form url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/app/subscriptions", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(w, r)
		return w
	}
	_, inbox := get(t, h, "/app")
	if !strings.Contains(inbox, `data-unsub="home"`) || !strings.Contains(inbox, `data-unsub="agents" data-approvals`) || strings.Contains(inbox, `data-unsub="home" data-approvals`) {
		t.Fatal("cards offer no Unsubscribe, or warn on the wrong topic")
	}
	if w := post(url.Values{"topic": {"home"}, "subscribe": {"0"}, "next": {"/app"}}); w.Code != 303 || w.Header().Get("Location") != "/app" || f.subscribed("home") {
		t.Fatalf("unsubscribe home: %d to %q", w.Code, w.Header().Get("Location"))
	}
	if _, b := get(t, h, "/app"); strings.Contains(b, "Front door open") || strings.Contains(b, `href="/app?topic=home"`) {
		t.Fatal("an unsubscribed topic still shows in the inbox or the topic list")
	}
	_, page := get(t, h, "/app?topic=home")
	if !strings.Contains(page, "Front door open") || !strings.Contains(page, "Not subscribed to home") || !strings.Contains(page, `data-subscribe="home"`) || strings.Contains(page, `data-unsub="home"`) {
		t.Fatal("the topic page does not browse an unsubscribed topic with its banner")
	}
	if w := post(url.Values{"topic": {"agents"}, "subscribe": {"0"}}); w.Header().Get("Location") != "/app/settings#topics" || !f.subscribed("agents") {
		t.Fatal("unsubscribing from agents did not stop at the warning first")
	}
	if post(url.Values{"topic": {"agents"}, "subscribe": {"0"}, "sure": {"1"}}); f.subscribed("agents") {
		t.Fatal("a confirmed unsubscribe from agents did not save")
	}
	_, set := get(t, h, "/app/settings")
	if !strings.Contains(set, `name="was" value="0:home"`) || !strings.Contains(set, `name="on" value="infra" checked`) || !strings.Contains(set, "data-approval-warn") {
		t.Fatal("Settings has no Topics list as drawn")
	}
	// Another device turns infra off after the list was drawn: saving the
	// list with infra untouched leaves it off, and only home changes.
	f.subs["infra"] = false
	post(url.Values{"was": {"0:home", "1:infra", "1:backups"}, "on": {"home", "infra"}, "new_topics": {"1"}, "new_topics_was": {"1"}})
	if !f.subscribed("home") || f.subscribed("infra") || f.subscribed("backups") || f.NoNewTopics {
		t.Fatalf("the list sent rows it did not change: %v, new topics off %v", f.subs, f.NoNewTopics)
	}
	f.Unfiltered = true
	if _, b := get(t, h, "/app/settings"); !strings.Contains(b, "Not available here") || strings.Contains(b, "data-subs-form") {
		t.Fatal("a token with no user is offered a list it may not change")
	}
}
