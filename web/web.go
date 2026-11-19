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
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path"
	"regexp"
	"sort"
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
	ID          string     `json:"id"`
	Topic       string     `json:"topic"`
	ExternalID  string     `json:"external_id,omitempty"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	Priority    int        `json:"priority"`
	Tags        []string   `json:"tags"`
	Click       string     `json:"click,omitempty"`
	Actions     []Action   `json:"actions,omitempty"`
	State       string     `json:"state"`
	StateBy     string     `json:"state_by"`
	SnoozeUntil *time.Time `json:"snooze_until"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Seq         int64      `json:"seq"`
}

type Action struct {
	Label string `json:"label"`
	URL   string `json:"url"`
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
	Items   []Notification
	Counts  map[string]int // open and recent rows by state
	Topics  []Topic
	Devices []Device // seen in the last 10 minutes
	Me      string   // device name of the signed-in token, the state_by its own actions get
}

// API is what the server provides. The request is passed so the server can
// read its own session cookie. Return ErrUnauthorized when there is no valid
// session; the page then shows nothing but a sign-in notice.
type API interface {
	Snapshot(r *http.Request, q Query) (Snapshot, error)
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
	"card": func(n Notification, view string) any {
		return struct {
			N    Notification
			View string
		}{n, view}
	},
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
}).ParseFS(files, "templates/*.html"))

type meta struct {
	Title, Desc, Path, Base string
}

func (m meta) URL() string { return m.Base + m.Path }

type doc struct {
	Key, Title string
	HTML       template.HTML
}

// docs/*.md (and subdirectories) rendered once at start, keyed by lowercase
// path without .md: docs/SPEC.md is served at /docs/spec.
var docs, docList = loadDocs()

var mdLink = regexp.MustCompile(`href="(?:\./)?(?:docs/)?([A-Za-z0-9_/-]+)\.md(#[^"]*)?"`)

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
		// ponytail: links are resolved from docs/, so ../x.md inside a subdirectory is not rewritten.
		html := mdLink.ReplaceAllStringFunc(b.String(), func(s string) string {
			g := mdLink.FindStringSubmatch(s)
			return `href="/docs/` + strings.ToLower(g[1]) + g[2] + `"`
		})
		key := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(p, "docs/"), ".md"))
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
		key := strings.TrimSuffix(strings.ToLower(r.PathValue("key")), ".md")
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
				v.Del("auth")
				v.Del("error")
				next.RawQuery = v.Encode()
				page(w, "signin", http.StatusUnauthorized, struct {
					meta
					Failed bool
				}{meta{Title: "Sign in: clrd", Path: next.RequestURI(), Base: base(r)}, failed})
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
				View, Heading, Topic, Q string
				Snap                    Snapshot
				Inbox, Snoozed, Cleared int
			}{
				meta: meta{Title: titles[view] + ": clrd", Path: r.URL.Path, Base: base(r)},
				View: view, Heading: titles[view], Topic: q.Topic, Q: q.Q, Snap: snap,
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

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		mux.ServeHTTP(w, r)
	})
}
