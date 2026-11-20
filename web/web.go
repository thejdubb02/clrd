// Package web serves the clrd landing page, the docs and the inbox. Every page
// is rendered on the server; static/inbox.js only adds live updates.
//
// The server mounts Handler as its GET fallback and implements API. See
// README.md in this directory for the seam.
package web

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thejdubb02/clrd"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

//go:embed templates static
var files embed.FS

// Notification mirrors docs/SPEC.md section 2.
type Notification struct {
	ID          string      `json:"id"`
	Topic       string      `json:"topic"`
	ExternalID  string      `json:"external_id,omitempty"`
	Title       string      `json:"title"`
	Message     string      `json:"message"`
	Priority    int         `json:"priority"`
	Tags        []string    `json:"tags"`
	Click       string      `json:"click,omitempty"`
	Icon        string      `json:"icon,omitempty"`   // http(s) image URL
	Format      string      `json:"format,omitempty"` // "markdown" (also when empty) or "text"
	Attachment  *Attachment `json:"attachment,omitempty"`
	Actions     []Action    `json:"actions,omitempty"`
	State       string      `json:"state"`
	StateBy     string      `json:"state_by"`
	StateByID   string      `json:"state_by_id"`
	Reply       string      `json:"reply"` // the reply action tapped, "" until someone answers
	SnoozeUntil *time.Time  `json:"snooze_until"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Seq         int64       `json:"seq"`
}

// Action is a link (URL) or an answer button (Reply), never both.
type Action struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Reply string `json:"reply,omitempty"`
}

// Attachment is a file on a notification: uploaded (/api/v1/files/<id>, with
// Type and Size) or external (an http(s) URL, Type "").
type Attachment struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// Image reports whether the card can show the file inline: the types the
// server serves inline (server/files.go); anything else it sends as a download.
func (a Attachment) Image() bool {
	switch a.Type {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// Links are the actions that open a URL.
func (n Notification) Links() (out []Action) {
	for _, a := range n.Actions {
		if a.URL != "" {
			out = append(out, a)
		}
	}
	return out
}

// Replies are the answer buttons still open: none once someone has answered.
func (n Notification) Replies() (out []Action) {
	for _, a := range n.Actions {
		if a.Reply != "" && n.Reply == "" {
			out = append(out, a)
		}
	}
	return out
}

// Answer is the label of the reply that was tapped.
func (n Notification) Answer() string {
	for _, a := range n.Actions {
		if a.Reply == n.Reply {
			return a.Label
		}
	}
	return n.Reply
}

// Heading is the title, or the message when a sender gave no title (common
// with ntfy-style publishes).
func (n Notification) Heading() string {
	if n.Title != "" {
		return n.Title
	}
	return n.Message
}

// Body is the message unless it was already used as the heading.
func (n Notification) Body() string {
	if n.Title == "" {
		return ""
	}
	return n.Message
}

type Topic struct {
	Name string `json:"name"`
	Open int    `json:"open"`
}

type Device struct {
	ID       string    `json:"id"` // token id; matches Snapshot.MeID on the browser viewing the page
	Name     string    `json:"name"`
	LastSeen time.Time `json:"last_seen"`
}

// Query selects the cards for one inbox view.
type Query struct {
	States []string // e.g. active, acknowledged
	Topic  string   // empty = all topics
	Q      string   // free-text search, empty = none
}

// Snapshot is everything one inbox page needs. Seq must be read before (or in
// the same transaction as) Items, so no event can fall between the page and
// the WebSocket replay that starts from Seq.
type Snapshot struct {
	Seq     int64
	Epoch   string // the database Seq belongs to; a different one means start over
	Items   []Notification
	Counts  map[string]int // open and recent rows by state
	Topics  []Topic
	Devices []Device // seen in the last 10 minutes
	Me      string   // device name of the signed-in token, the state_by its own actions get
	MeID    string   // its token id, the state_by_id its own actions get (names can repeat)
	// Snooze times on each card's menu (Go durations) and the one a tap uses.
	SnoozeIntervals []string
	SnoozeDefault   string
	// Display settings for message bodies (docs/SPEC.md section 2.1).
	Formatting, Autolink, Favicons, LinkPreviews bool
	TopicPreviews                                map[string]bool // overrides LinkPreviews for one topic
}

// API is what the server provides. The request is passed so the server can
// read its own session cookie. Return ErrUnauthorized when there is no valid
// session; the page then shows nothing but a sign-in notice.
type API interface {
	Snapshot(r *http.Request, q Query) (Snapshot, error)
	// Act changes one notification for the signed-in browser: ack, snooze
	// (until is set), dismiss or reopen.
	Act(r *http.Request, id, action string, until *time.Time) error
	// Reply answers with one of the notification's reply actions, as shown at seq.
	Reply(r *http.Request, id, reply string, seq int64) error
}

var ErrUnauthorized = errors.New("web: unauthorized")

// Views of the inbox and the states each one shows.
var views = map[string][]string{
	"inbox":   {"active", "acknowledged"},
	"snoozed": {"snoozed"},
	"cleared": {"dismissed", "resolved"},
}

var tpl = template.Must(template.New("").Funcs(template.FuncMap{
	"iso":   func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"clock": func(t time.Time) string { return t.UTC().Format("Jan 2, 15:04 UTC") },
	"card": func(n Notification, view, here string, snap Snapshot) any {
		return struct {
			N             Notification
			View, Here    string
			Snooze        []string
			SnoozeDefault string
			Text          template.HTML
			Preview       bool
		}{n, view, here, snap.SnoozeIntervals, snap.SnoozeDefault, renderBody(n.Body(), n.Format, snap), snap.PreviewsOn(n.Topic)}
	},
	"snoozeLabel": snoozeLabel,
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
}).ParseFS(files, "templates/*.html"))

// PreviewsOn says whether a topic's cards show link previews, as the server
// decides for GET /api/v1/preview.
func (s Snapshot) PreviewsOn(topic string) bool {
	if v, ok := s.TopicPreviews[topic]; ok {
		return v
	}
	return s.LinkPreviews
}

// TopicPreview is a topic's override for the Settings select: "", "on" or "off".
func (s Snapshot) TopicPreview(topic string) string {
	v, ok := s.TopicPreviews[topic]
	switch {
	case !ok:
		return ""
	case v:
		return "on"
	}
	return "off"
}

// snoozeLabel reads a Go duration as people say it: "1 hour", "2 days", "90 min".
// badge is the open count a browser tab shows before the page name.
func badge(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("(%d) ", n)
}

func snoozeLabel(v string) string {
	d, err := time.ParseDuration(v)
	if err != nil || d%time.Minute != 0 {
		return v
	}
	n, unit := int(d/time.Minute), "min"
	switch {
	case d%(24*time.Hour) == 0:
		n, unit = int(d/(24*time.Hour)), "day"
	case d%time.Hour == 0:
		n, unit = int(d/time.Hour), "hour"
	default:
		return fmt.Sprintf("%d min", n)
	}
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", n, unit)
}

type meta struct {
	Title, Desc, Path, Base string
}

func (m meta) URL() string { return m.Base + m.Path }

type doc struct {
	Key, Title string
	HTML       template.HTML
}

// docs/*.md (and subdirectories) rendered once at start, keyed by lowercase
// path without .md: docs/SPEC.md is served at /docs/spec, and a folder's
// README.md at the folder, docs/integrations/README.md at /docs/integrations.
var docs, docList = loadDocs()

// A relative link in a doc: no scheme, not absolute, not just a fragment.
var relLink = regexp.MustCompile(`href="([A-Za-z0-9_.][A-Za-z0-9_./-]*)(#[^"]*)?"`)

func docKey(p string) string {
	p = strings.ToLower(p)
	if dir, file := path.Split(p); file == "readme" && dir != "" {
		return strings.TrimSuffix(dir, "/")
	}
	return p
}

func loadDocs() (map[string]doc, []doc) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM), // no Typographer: it would add dashes and ellipses
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	m := map[string]doc{}
	err := fs.WalkDir(clrd.Files, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".md" {
			return err
		}
		src, err := fs.ReadFile(clrd.Files, p)
		if err != nil {
			return err
		}
		var b bytes.Buffer
		if err := md.Convert(src, &b); err != nil {
			return err
		}
		// Relative links resolve from this file's folder, as on GitHub: another
		// doc becomes its /docs/ page, anything else in the repo its GitHub page.
		// Code blocks and tables scroll sideways on a phone, so they take
		// keyboard focus (axe: scrollable-region-focusable).
		out := strings.NewReplacer("<pre>", `<pre tabindex="0">`, "<table>", `<table tabindex="0">`).Replace(b.String())
		html := relLink.ReplaceAllStringFunc(out, func(s string) string {
			g := relLink.FindStringSubmatch(s)
			t := path.Join(path.Dir(p), g[1])
			switch {
			case t == ".." || strings.HasPrefix(t, "../"):
				return s
			case strings.HasPrefix(t, "docs/") && strings.HasSuffix(t, ".md"):
				return `href="/docs/` + docKey(strings.TrimSuffix(strings.TrimPrefix(t, "docs/"), ".md")) + g[2] + `"`
			}
			return `href="https://github.com/thejdubb02/clrd/tree/main/` + t + g[2] + `"`
		})
		key := docKey(strings.TrimSuffix(strings.TrimPrefix(p, "docs/"), ".md"))
		title := key
		if line, _, _ := strings.Cut(string(src), "\n"); strings.HasPrefix(line, "# ") {
			title = strings.TrimPrefix(line, "# ")
		}
		m[key] = doc{key, title, template.HTML(html)}
		return nil
	})
	if err != nil {
		panic(err)
	}
	list := make([]doc, 0, len(m))
	for _, d := range m {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return m, list
}

// Handler serves every web route. baseURL (CLRD_BASE_URL) makes canonical and
// Open Graph URLs absolute; empty falls back to the request host.
func Handler(api API, baseURL string) http.Handler {
	baseURL = strings.TrimRight(baseURL, "/")
	base := func(r *http.Request) string {
		if baseURL != "" {
			return baseURL
		}
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
	page := func(w http.ResponseWriter, name string, status int, data any) {
		var b bytes.Buffer
		if err := tpl.ExecuteTemplate(&b, name, data); err != nil {
			log.Printf("web: render %s: %v", name, err)
			http.Error(w, "render failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		w.Write(b.Bytes())
	}
	static, _ := fs.Sub(files, "static")
	assets, _ := fs.Sub(clrd.Files, "assets")
	cached := func(prefix string, fsys fs.FS) http.Handler {
		h := http.StripPrefix(prefix, http.FileServerFS(fsys))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			h.ServeHTTP(w, r)
		})
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", cached("/static/", static))
	mux.Handle("GET /assets/", cached("/assets/", assets))
	mux.HandleFunc("GET /favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFileFS(w, r, clrd.Files, "assets/mark.svg")
	})
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		tpl.ExecuteTemplate(w, "robots.txt", base(r))
	})
	mux.HandleFunc("GET /sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		tpl.ExecuteTemplate(w, "sitemap.xml", struct {
			Base string
			Docs []doc
		}{base(r), docList})
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		page(w, "landing", 200, meta{
			Title: "clrd: dismiss once, gone everywhere",
			Desc:  "clrd is a self-hosted notification server. Send an alert with one HTTP call; clear it on any device and it clears on all of them. ntfy compatible, open source.",
			Path:  "/", Base: base(r),
		})
	})
	mux.HandleFunc("GET /docs/{key...}", func(w http.ResponseWriter, r *http.Request) {
		key := docKey(strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(r.PathValue("key")), "/"), ".md"))
		data := struct {
			meta
			Doc  doc
			Docs []doc
		}{meta: meta{Title: "Docs: clrd", Desc: "Documentation for clrd, the self-hosted notification server.", Path: "/docs/", Base: base(r)}, Docs: docList}
		if key != "" {
			d, ok := docs[key]
			if !ok {
				data.Title, data.Path = "Not found: clrd docs", r.URL.Path
				page(w, "docs", http.StatusNotFound, data)
				return
			}
			data.Doc, data.Title, data.Path = d, d.Title+": clrd docs", "/docs/"+key
		}
		page(w, "docs", 200, data)
	})

	app := func(view string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			states := views[view]
			if states == nil { // settings: no list, the rest of the snapshot still counts
				states = []string{}
			}
			q := Query{States: states, Topic: r.URL.Query().Get("topic"), Q: strings.TrimSpace(r.URL.Query().Get("q"))}
			snap, err := api.Snapshot(r, q)
			if errors.Is(err, ErrUnauthorized) {
				next := *r.URL // keep the filters, drop a stale ?auth= that would beat the new cookie
				v := next.Query()
				failed := v.Get("error") == "token" // the server sends a bad token back here
				name := ""
				if failed {
					name = v.Get("name") // and the device name typed, so it need not be typed again
				}
				v.Del("auth")
				v.Del("error")
				v.Del("name")
				next.RawQuery = v.Encode()
				page(w, "signin", http.StatusUnauthorized, struct {
					meta
					Failed bool
					Name   string
				}{meta{Title: "Sign in: clrd", Path: next.RequestURI(), Base: base(r)}, failed, name})
				return
			}
			if err != nil {
				log.Printf("web: snapshot: %v", err)
				http.Error(w, "could not load notifications", http.StatusBadGateway)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			c := snap.Counts
			titles := map[string]string{"inbox": "Inbox", "snoozed": "Snoozed", "cleared": "Cleared", "settings": "Settings"}
			page(w, "app", 200, struct {
				meta
				View, Heading, Topic, Q, Here string
				Snap                          Snapshot
				Inbox, Snoozed, Cleared       int
			}{
				meta: meta{Title: badge(c["active"]+c["acknowledged"]) + titles[view] + ": clrd", Path: r.URL.Path, Base: base(r)},
				View: view, Heading: titles[view], Topic: q.Topic, Q: q.Q, Here: r.URL.RequestURI(), Snap: snap,
				Inbox:   c["active"] + c["acknowledged"],
				Snoozed: c["snoozed"],
				Cleared: c["dismissed"] + c["resolved"],
			})
		}
	}
	mux.HandleFunc("GET /app", app("inbox"))
	mux.HandleFunc("GET /app/{$}", app("inbox"))
	mux.HandleFunc("GET /app/snoozed", app("snoozed"))
	mux.HandleFunc("GET /app/cleared", app("cleared"))
	mux.HandleFunc("GET /app/settings", app("settings"))
	// The style guide: every component in every state, no data, no sign-in.
	mux.HandleFunc("GET /app/ui", func(w http.ResponseWriter, r *http.Request) {
		page(w, "ui", 200, meta{Title: "Components: clrd", Path: "/app/ui", Base: base(r)})
	})

	// The card buttons as a plain form post, so they work without JavaScript;
	// then back to the page they came from. Snooze takes a duration because
	// only the browser knows its time zone (the script sends "until" itself).
	mux.HandleFunc("POST /app/act", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad form.", http.StatusBadRequest)
			return
		}
		f := r.PostForm
		// Back to an inbox page only. Redirect cleans dot segments, so a path
		// with ".." or a backslash could turn into "/\host": refuse those.
		next := f.Get("next")
		if p, _, _ := strings.Cut(next, "?"); (p != "/app" && !strings.HasPrefix(p, "/app/")) || strings.Contains(p, "..") || strings.Contains(p, "\\") {
			next = "/app"
		}
		act, until := f.Get("act"), (*time.Time)(nil)
		if s := f.Get("snooze"); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 || d > 31*24*time.Hour {
				http.Error(w, "Snooze needs a duration like 1h.", http.StatusBadRequest)
				return
			}
			t := time.Now().Add(d)
			act, until = "snooze", &t
		}
		var err error
		if reply := f.Get("reply"); reply != "" {
			act = "reply"
			// seq 0 would skip the server's "changed since you looked" check.
			if seq, perr := strconv.ParseInt(f.Get("seq"), 10, 64); perr != nil || seq <= 0 {
				err = errors.New("reply without the card's seq")
			} else {
				err = api.Reply(r, f.Get("id"), reply, seq)
			}
		} else if act == "ack" || act == "snooze" || act == "dismiss" || act == "reopen" {
			err = api.Act(r, f.Get("id"), act, until)
		} else {
			http.Error(w, "Unknown action.", http.StatusBadRequest)
			return
		}
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			log.Printf("web: %s: %v", act, err)
			http.Error(w, "Could not change that notification. Go back and reload the page.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, next, http.StatusSeeOther) // signed out: next shows the sign-in page
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https: http:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		mux.ServeHTTP(w, r)
	})
}
