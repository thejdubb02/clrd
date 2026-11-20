// Package server is the clrd server: SQLite store, event log, native and
// ntfy-compatible HTTP API, WebSocket and SSE sync. See server/README.md for
// how other packages mount pages on it.
package server

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed openapi.json
var openapiJSON []byte

// Options configure a Server. ConfigFromEnv fills them from CLRD_* variables.
type Options struct {
	Listen    string        // CLRD_LISTEN, default ":8080"
	DB        string        // CLRD_DB, default DefaultDB()
	BaseURL   string        // CLRD_BASE_URL, public URL used in links; unset, links use the address the request came in on
	Retention time.Duration // CLRD_RETENTION, default 720h
	Logger    *slog.Logger  // default slog.Default()

	// HidePublicPages (CLRD_PUBLIC_PAGES=off) is for a private server: / goes to
	// the inbox, /docs and /sitemap.xml are 404, and robots.txt disallows all.
	HidePublicPages bool

	Attachments     string // CLRD_ATTACHMENTS, default "attachments" next to DB
	AttachmentMax   int64  // CLRD_ATTACHMENT_MAX_MB, bytes, default 15 MB
	AttachmentTotal int64  // CLRD_ATTACHMENT_TOTAL_MB, bytes, default 5000 MB

	RateToken int // CLRD_RATE_TOKEN, publishes a minute per token, default 60, -1 off
	RateTopic int // CLRD_RATE_TOPIC, publishes a minute per topic, default 60, -1 off

	// TrustedProxies (CLRD_TRUSTED_PROXIES, comma-separated CIDRs or
	// addresses) are the only peers whose X-Forwarded-For is read. Unset,
	// any loopback or private-network peer is trusted.
	TrustedProxies []netip.Prefix
}

// ConfigFromEnv reads the spec section 7 environment variables.
func ConfigFromEnv() (Options, error) {
	o := Options{Listen: os.Getenv("CLRD_LISTEN"), DB: os.Getenv("CLRD_DB"), BaseURL: os.Getenv("CLRD_BASE_URL")}
	if v := os.Getenv("CLRD_RETENTION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return o, fmt.Errorf("CLRD_RETENTION: want a duration like 720h, got %q", v)
		}
		o.Retention = d
	}
	o.Attachments = os.Getenv("CLRD_ATTACHMENTS")
	switch v := os.Getenv("CLRD_PUBLIC_PAGES"); v {
	case "", "on":
	case "off":
		o.HidePublicPages = true
	default:
		return o, fmt.Errorf("CLRD_PUBLIC_PAGES: want on or off, got %q", v)
	}
	for _, v := range []struct {
		env string
		to  *int64
	}{{"CLRD_ATTACHMENT_MAX_MB", &o.AttachmentMax}, {"CLRD_ATTACHMENT_TOTAL_MB", &o.AttachmentTotal}} {
		if e := os.Getenv(v.env); e != "" {
			mb, err := strconv.ParseInt(e, 10, 64)
			if err != nil || mb <= 0 || mb > 1<<40 {
				return o, fmt.Errorf("%s: want a whole number of megabytes, got %q", v.env, e)
			}
			*v.to = mb << 20
		}
	}
	for _, v := range []struct {
		env string
		to  *int
	}{{"CLRD_RATE_TOKEN", &o.RateToken}, {"CLRD_RATE_TOPIC", &o.RateTopic}} {
		if e := os.Getenv(v.env); e != "" {
			n, err := strconv.Atoi(e)
			if err != nil || n < 0 || n > 1e6 {
				return o, fmt.Errorf("%s: want publishes a minute (0 turns it off), got %q", v.env, e)
			}
			*v.to = cmp.Or(n, -1)
		}
	}
	for _, v := range strings.Split(os.Getenv("CLRD_TRUSTED_PROXIES"), ",") {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		p, err := netip.ParsePrefix(v)
		if err != nil {
			a, aerr := netip.ParseAddr(v)
			if aerr != nil {
				return o, fmt.Errorf("CLRD_TRUSTED_PROXIES: want CIDRs or addresses, got %q", v)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if p.Addr().Is4In6() && p.Bits() >= 96 { // the peer is unmapped before matching
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		o.TrustedProxies = append(o.TrustedProxies, p.Masked())
	}
	if o.TrustedProxies == nil && os.Getenv("CLRD_TRUSTED_PROXIES") != "" {
		return o, errors.New("CLRD_TRUSTED_PROXIES: set but lists no proxies")
	}
	return o, nil
}

// legacyDB was the only default before #242; a server already using it keeps it.
var legacyDB = "/data/clrd.db"

// DefaultDB is the database used when CLRD_DB is unset: legacyDB if it
// exists, else clrd/clrd.db under the user's data folder ($XDG_DATA_HOME or
// ~/.local/share). Same file from any working directory, so `clrd token
// create` finds the server's database, and no root needed.
func DefaultDB() string {
	// Anything but "not there" (even permission denied) keeps the old file, so
	// a server never silently starts over on an empty database.
	if _, err := os.Stat(legacyDB); !errors.Is(err, fs.ErrNotExist) {
		return legacyDB
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return legacyDB
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "clrd", "clrd.db")
}

// baseFor is the public URL for links handed to this request: CLRD_BASE_URL
// when set, else the address the request came in on, so a phone on the LAN
// gets a link it can open instead of localhost.
func (s *Server) baseFor(r *http.Request) string {
	if s.baseSet || r.Host == "" {
		return s.opt.BaseURL
	}
	// The browser's Origin carries the scheme it used, which a TLS proxy hides.
	if o, err := url.Parse(r.Header.Get("Origin")); err == nil && o.Host == r.Host && (o.Scheme == "https" || o.Scheme == "http") {
		return o.Scheme + "://" + r.Host
	}
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

// Server is the clrd server. Create it with New, add pages with Mount, then Run.
type Server struct {
	opt     Options
	baseSet bool // CLRD_BASE_URL given; else links follow the request's address
	db      *sql.DB
	log     *slog.Logger
	hub     hub

	mu       sync.Mutex // serialises writes: seq order = delivery order
	lastMS   int64      // newID state, guarded by mu
	lastRand [10]byte
	seq      atomic.Int64 // last committed event seq
	pruned   atomic.Int64 // events with seq <= pruned are gone
	epoch    string       // this database's id, sent with every cursor
	revoked  sync.Map     // token ids deleted while the server runs
	failing  sync.Map     // background job name -> true while it keeps failing; /healthz reports it

	seen       sync.Map // token id -> time last written to last_seen_at
	pwLimit    *limiter // ntfy password attempts per address
	tokLimit   *limiter // publishes per token, nil when off
	topicLimit *limiter // publishes per topic, nil when off
	setupMu    sync.Mutex
	setupCode  string
	pairMu     sync.Mutex
	pairs      map[string]*pairing // browser sign-ins waiting for approval, by code (pair.go)

	settingsChanged signal // fires after every settings change
	fetch           *fetcher

	mux    *http.ServeMux
	prefix string   // the path of CLRD_BASE_URL, "" at the root: every route is served under it
	routes []string // every pattern registered, checked against openapi.json
	mounts []mount
}

type mount struct {
	prefix string
	h      http.Handler
}

// New opens the database and builds the routes. It does not listen.
func New(opt Options) (*Server, error) {
	if opt.Listen == "" {
		opt.Listen = ":8080"
	}
	if opt.DB == "" {
		opt.DB = DefaultDB()
	}
	baseSet := opt.BaseURL != ""
	if !baseSet {
		opt.BaseURL = "http://localhost" + opt.Listen[strings.LastIndex(opt.Listen, ":"):]
	}
	opt.BaseURL = strings.TrimSuffix(opt.BaseURL, "/")
	base, err := url.Parse(opt.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("CLRD_BASE_URL: %w", err)
	}
	if p := base.Path; p == "/api" || strings.HasPrefix(p, "/api/") { // the server calls its own /api/v1 routes without the path
		return nil, fmt.Errorf("CLRD_BASE_URL: the path cannot start with /api")
	}
	if opt.Retention == 0 {
		opt.Retention = 720 * time.Hour
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.Attachments == "" {
		opt.Attachments = filepath.Join(filepath.Dir(opt.DB), "attachments")
	}
	if opt.AttachmentMax == 0 {
		opt.AttachmentMax = 15 << 20
	}
	opt.RateToken, opt.RateTopic = cmp.Or(opt.RateToken, 60), cmp.Or(opt.RateTopic, 60)
	if opt.AttachmentTotal == 0 {
		opt.AttachmentTotal = 5000 << 20
	}
	if err := os.MkdirAll(opt.Attachments, 0o750); err != nil {
		return nil, fmt.Errorf("attachments folder: %w", err)
	}
	db, err := openDB(opt.DB)
	if err != nil {
		return nil, err
	}
	s := &Server{opt: opt, baseSet: baseSet, db: db, log: opt.Logger, mux: http.NewServeMux(), prefix: strings.TrimSuffix(base.Path, "/"),
		pwLimit: newLimiter(10, time.Minute), tokLimit: perMinute(opt.RateToken), topicLimit: perMinute(opt.RateTopic)}
	s.hub.subs = map[chan Event]string{}
	var seq, pruned int64
	if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'events'`).Scan(&seq); err != nil && !errors.Is(err, sql.ErrNoRows) {
		db.Close()
		return nil, err
	}
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'pruned_through'`).Scan(&pruned); err != nil && !errors.Is(err, sql.ErrNoRows) {
		db.Close()
		return nil, err
	}
	// Minted once per database: seq numbers from another database mean nothing here.
	var epoch int64
	if _, err := db.Exec(`INSERT OR IGNORE INTO meta (key, value) VALUES ('epoch', ?)`, rand.Int64()); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'epoch'`).Scan(&epoch); err != nil {
		db.Close()
		return nil, err
	}
	// A restored backup keeps its epoch, so the sidecar (written on Close)
	// remembers the highest seq handed out: a head below it means the database
	// went back in time, and clients must start over (issue #63).
	var sideEpoch string
	var high int64
	if b, err := os.ReadFile(opt.DB + ".epoch"); err == nil {
		fmt.Sscan(string(b), &sideEpoch, &high)
	}
	// Another epoch there means a database swapped in under it, such as the same backup restored twice.
	if sideEpoch != "" && (sideEpoch != strconv.FormatInt(epoch, 36) || seq < high) {
		epoch = rand.Int64()
		if _, err := db.Exec(`UPDATE meta SET value = ? WHERE key = 'epoch'`, epoch); err != nil {
			db.Close()
			return nil, err
		}
		opt.Logger.Warn("database was restored or swapped: new epoch, clients start over", "seq", seq, "was", high)
	}
	s.epoch = strconv.FormatInt(epoch, 36)
	s.seq.Store(seq)
	s.pruned.Store(pruned)
	s.fetch = newFetcher(filepath.Join(filepath.Dir(opt.DB), "cache"))
	s.routesSetup()
	return s, nil
}

// Close records the epoch and head seq next to the database (see New), then closes it.
func (s *Server) Close() error {
	if err := os.WriteFile(s.opt.DB+".epoch", fmt.Appendf(nil, "%s %d\n", s.epoch, s.seq.Load()), 0o600); err != nil {
		s.log.Warn("could not write the epoch file", "err", err)
	}
	return s.db.Close()
}

// CurrentSeq is the seq of the last committed event.
func (s *Server) CurrentSeq() int64 { return s.seq.Load() }

// Epoch is this database's id, sent with every seq (see SPEC, Epoch).
func (s *Server) Epoch() string { return s.epoch }

// DBPath is the database file in use (CLRD_DB or DefaultDB).
func (s *Server) DBPath() string { return s.opt.DB }

// Path is p (a path from the root, like "/app") as the browser must see it:
// under the sub-path of CLRD_BASE_URL, if it has one.
func (s *Server) Path(p string) string { return s.prefix + p }

// Mount serves h for every path under prefix ("/docs" gets /docs and
// /docs/...; "/" gets only the exact root). Longest prefix wins and mounts
// are checked before the server's own routes. Call before Run.
func (s *Server) Mount(prefix string, h http.Handler) {
	prefix = "/" + strings.Trim(prefix, "/")
	for _, own := range []string{"/api", "/healthz", "/openapi.json", "/setup", "/v1"} {
		if prefix == own || strings.HasPrefix(prefix, own+"/") {
			panic("clrd: Mount: " + prefix + " belongs to the server")
		}
	}
	s.mounts = append(s.mounts, mount{prefix, h})
	sort.Slice(s.mounts, func(i, j int) bool { return len(s.mounts[i].prefix) > len(s.mounts[j].prefix) })
}

// bodyTimeout bounds how long a client may take to send a request body. A
// server-wide ReadTimeout would also cut WebSocket and SSE streams.
var bodyTimeout = 30 * time.Second

const maxBody = 1 << 20 // no route takes more, except a file upload

// uploadTimeout bounds a file upload, which streams instead of being buffered.
var uploadTimeout = 10 * time.Minute

// isUpload reports a request whose body is a file: a multipart native publish
// or an ntfy publish with a file name. Those stream to disk.
func isUpload(r *http.Request) bool {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		return false
	}
	if r.URL.Path == "/api/v1/notifications" {
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		return r.Method == http.MethodPost && mt == "multipart/form-data"
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	return p != "" && strings.Count(p, "/") <= 1 && ntfyFilename(r) != "" // /{topic} or /{topic}/{sid}
}

// ServeHTTP routes to a mount when one matches, else to the API.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Under a sub-path, strip it so the routes match as at the root. A path
	// without it passes through: a proxy may strip it first, and the web
	// sign-in link calls back in with a bare path.
	if p := r.URL.Path; s.prefix != "" && (p == s.prefix || strings.HasPrefix(p, s.prefix+"/")) {
		r2, u := *r, *r.URL
		u.Path = cmp.Or(strings.TrimPrefix(p, s.prefix), "/")
		u.RawPath = strings.TrimPrefix(u.RawPath, s.prefix)
		r2.URL = &u
		r = &r2
	}
	p := r.URL.Path
	var mounted http.Handler
	for _, m := range s.mounts {
		if m.prefix == "/" && r.Method != http.MethodGet && r.Method != http.MethodHead {
			continue // POST / is ntfy's JSON publish
		}
		if p == m.prefix || (m.prefix != "/" && strings.HasPrefix(p, m.prefix+"/")) {
			mounted = m.h
			break
		}
	}
	if r.ContentLength != 0 && mounted == nil && isUpload(r) {
		// Streamed to disk by the handler, under a longer deadline and the file cap.
		http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadTimeout))
		r.Body = http.MaxBytesReader(w, r.Body, s.opt.AttachmentMax+maxBody)
	} else if r.ContentLength != 0 { // read the whole body now, under a deadline
		rc := http.NewResponseController(w)
		rc.SetReadDeadline(time.Now().Add(bodyTimeout))
		b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		// On refusal the deadline stays, so net/http does not wait to drain the rest.
		if errors.Is(err, os.ErrDeadlineExceeded) {
			s.fail(w, errf(408, "timeout", "request body too slow"))
			return
		}
		if err != nil {
			s.fail(w, errf(400, "bad_request", "cannot read request body"))
			return
		}
		if len(b) > maxBody {
			s.fail(w, errf(413, "too_large", "request body over %d bytes", maxBody))
			return
		}
		rc.SetReadDeadline(time.Time{}) // long calls (MCP wait_for_reply) keep their context
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	if mounted != nil {
		mounted.ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodOptions && !strings.HasPrefix(p, "/api/") {
		// CORS preflight for the ntfy routes, answered on any path a mount does not own, like ntfy.
		// "*" does not cover Authorization, so it is named.
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, PUT, POST, PATCH, DELETE")
		h.Set("Access-Control-Allow-Headers", "*, Authorization")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// Run listens on Options.Listen and runs the background tickers until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	if err := s.bootstrap(ctx); err != nil {
		return err
	}
	go s.every(ctx, "snooze wake", wakeEvery, s.wakeSnoozed)
	go s.every(ctx, "expiry", wakeEvery, s.expireDue)
	go s.every(ctx, "check-ins", wakeEvery, s.checkins)
	go s.every(ctx, "prune", time.Hour, s.prune)
	go s.every(ctx, "cache sweep", time.Hour, s.fetch.sweep)
	go s.every(ctx, "webhooks out", hookEvery, s.deliverHooks)
	srv := &http.Server{Addr: s.opt.Listen, Handler: s, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }} // ends open streams on shutdown
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Info("clrd listening", "addr", s.opt.Listen, "base_url", s.opt.BaseURL, "db", s.opt.DB)
	if !s.baseSet {
		s.log.Info("CLRD_BASE_URL not set: phone links use the address each browser opened clrd on; set it to your public https URL behind a proxy")
	}
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

// wakeEvery is how often snoozed notifications are checked for waking.
var wakeEvery = 15 * time.Second

// jobFailures in a row mark a background job failing in /healthz.
const jobFailures = 3

func (s *Server) every(ctx context.Context, name string, d time.Duration, fn func(context.Context) error) {
	t := time.NewTicker(d)
	defer t.Stop()
	fails := 0
	for {
		if err := fn(ctx); err == nil {
			fails = 0
			s.failing.Delete(name)
		} else if ctx.Err() == nil {
			s.log.Error("background job", "job", name, "err", err)
			if fails++; fails >= jobFailures {
				s.failing.Store(name, true)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// route registers a pattern and remembers it for the openapi check.
func (s *Server) route(pattern string, h func(http.ResponseWriter, *http.Request) error) {
	s.routes = append(s.routes, pattern)
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			s.fail(w, err)
		}
	})
}

// api registers a native route that needs a token (cookie allowed).
func (s *Server) api(pattern string, h func(http.ResponseWriter, *http.Request, *Token) error) {
	s.route(pattern, func(w http.ResponseWriter, r *http.Request) error {
		tok := s.auth(r, true)
		if tok == nil {
			return errf(401, "unauthorized", noToken)
		}
		return h(w, r, tok)
	})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	status, code, msg := errorStatus(err)
	if ae := (*apiError)(nil); errors.As(err, &ae) && ae.retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(ae.retry.Seconds())))))
	}
	if status == 500 {
		s.log.Error("request failed", "err", err)
	}
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody)).Decode(v); err != nil {
		return errf(400, "bad_request", "bad JSON body: %v", err)
	}
	return nil
}

func (s *Server) routesSetup() {
	s.route("GET /healthz", func(w http.ResponseWriter, r *http.Request) error {
		if err := s.db.PingContext(r.Context()); err != nil {
			return err
		}
		var failing []string // names only: the errors are in the log, not on a public page
		s.failing.Range(func(k, _ any) bool { failing = append(failing, k.(string)); return true })
		if len(failing) > 0 {
			sort.Strings(failing)
			writeJSON(w, 503, map[string]any{"ok": false, "seq": s.CurrentSeq(), "failing": failing})
			return nil
		}
		writeJSON(w, 200, map[string]any{"ok": true, "seq": s.CurrentSeq()})
		return nil
	})
	s.route("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write(openapiJSON)
		return err
	})
	s.route("GET /setup", s.handleSetup)
	s.route("POST /setup", s.handleSetup)
	s.route("POST /api/v1/session", s.handleSession)
	s.settingsRoutes()
	s.fetchRoutes()
	s.route("DELETE /api/v1/session", s.handleSignOut)
	s.pairRoutes()
	s.linkRoutes()
	s.deviceRoutes()

	s.api("POST /api/v1/notifications", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var in PublishInput
		var n *Notification
		var err error
		if isUpload(r) {
			n, err = s.publishMultipart(r, tok)
		} else if err = decodeJSON(r, &in); err == nil {
			n, err = s.Publish(r.Context(), tok, in)
		}
		if err != nil {
			return err
		}
		writeJSON(w, 200, n)
		return nil
	})
	s.api("GET /api/v1/files/{id}", s.serveFile)
	s.api("GET /api/v1/notifications", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		q := r.URL.Query()
		seq := s.CurrentSeq() // before the query, so a ws?since=seq misses nothing
		limit, _ := strconv.Atoi(q.Get("limit"))
		ns, err := s.List(r.Context(), tok, ListQuery{Topics: splitList(q.Get("topic")), State: q.Get("state"),
			Q: q.Get("q"), Before: q.Get("before"), Limit: limit})
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"notifications": ns, "seq": seq, "epoch": s.epoch})
		return nil
	})
	s.api("GET /api/v1/notifications/{id}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		n, err := s.Get(r.Context(), tok, r.PathValue("id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, n)
		return nil
	})
	s.api("PATCH /api/v1/notifications/{id}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var in struct {
			Message string `json:"message"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		n, err := s.Edit(r.Context(), tok, r.PathValue("id"), in.Message)
		if err != nil {
			return err
		}
		writeJSON(w, 200, n)
		return nil
	})
	for _, action := range []string{"ack", "snooze", "dismiss", "resolve", "reopen"} {
		s.api("POST /api/v1/notifications/{id}/"+action, func(w http.ResponseWriter, r *http.Request, tok *Token) error {
			var in struct {
				For   string     `json:"for"`
				Until *time.Time `json:"until"`
				Via   string     `json:"via"`
			}
			err := decodeJSON(r, &in) // optional for every action but snooze: an old client sends no body
			var until *time.Time
			if action == "snooze" {
				if err != nil {
					return err
				}
				if until, err = snoozeUntil(in.For, in.Until); err != nil {
					return err
				}
			}
			n, err := s.Act(r.Context(), tok, r.PathValue("id"), action, cleanVia(in.Via, "api"), until)
			if err != nil {
				return err
			}
			writeJSON(w, 200, n)
			return nil
		})
	}
	s.api("POST /api/v1/notifications/{id}/reply", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var in struct {
			Reply string `json:"reply"`
			Seq   int64  `json:"seq"`
			Via   string `json:"via"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		n, err := s.Reply(r.Context(), tok, r.PathValue("id"), in.Reply, cleanVia(in.Via, "api"), in.Seq)
		if err != nil {
			return err
		}
		writeJSON(w, 200, n)
		return nil
	})
	s.api("POST /api/v1/notifications/clear", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var in struct {
			Topic string   `json:"topic"`
			IDs   []string `json:"ids"`
			Via   string   `json:"via"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		n, err := s.Clear(r.Context(), tok, in.Topic, in.IDs, cleanVia(in.Via, "api"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]int{"cleared": n})
		return nil
	})
	s.api("GET /api/v1/topics", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		ts, err := s.Topics(r.Context(), tok)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"topics": ts})
		return nil
	})
	s.api("GET /api/v1/counts", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		c, err := s.Counts(r.Context(), tok)
		if err != nil {
			return err
		}
		if v := r.URL.Query().Get("keep_cleared_days"); v != "" { // preview before shortening the setting
			days, err := strconv.Atoi(v)
			if err != nil {
				return errf(400, "bad_request", "keep_cleared_days must be a whole number of days")
			}
			if c["would_remove"], err = s.WouldRemove(r.Context(), tok, days); err != nil {
				return err
			}
		}
		writeJSON(w, 200, c)
		return nil
	})
	s.api("PUT /api/v1/topics/{topic}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var in struct {
			Icon    *string `json:"icon"`
			Checkin *string `json:"checkin"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.Icon == nil && in.Checkin == nil { // a missing key must not wipe anything
			return errf(400, "bad_request", `body needs "icon" (a URL, or "" to remove it) or "checkin" (like "25h", or "" for off)`)
		}
		topic, out := r.PathValue("topic"), map[string]string{"topic": r.PathValue("topic")}
		if in.Icon != nil { // checked before the timer changes, so a bad icon changes nothing
			if err := checkImageURL("icon", *in.Icon); err != nil {
				return err
			}
		}
		if in.Checkin != nil {
			var every time.Duration
			if *in.Checkin != "" {
				d, err := time.ParseDuration(*in.Checkin)
				if err != nil || d == 0 { // only "" turns it off
					return errf(400, "bad_request", `checkin must be a duration like "25h" or "30m"`)
				}
				every = d
			}
			if err := s.SetTopicCheckin(r.Context(), tok, topic, every); err != nil {
				return err
			}
			out["checkin"] = *in.Checkin
		}
		if in.Icon != nil {
			if err := s.SetTopicIcon(r.Context(), tok, topic, *in.Icon); err != nil {
				return err
			}
			out["icon"] = *in.Icon
		}
		writeJSON(w, 200, out)
		return nil
	})
	s.api("GET /api/v1/tokens", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		ts, err := s.Tokens(r.Context(), time.Time{})
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"tokens": ts})
		return nil
	})
	s.api("POST /api/v1/tokens", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		var in struct {
			Name   string   `json:"name"`
			Scopes []string `json:"scopes"`
			Topics []string `json:"topics"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		t, raw, err := s.CreateToken(r.Context(), in.Name, in.Scopes, in.Topics)
		if err != nil {
			return err
		}
		writeJSON(w, 200, struct {
			*Token
			Secret string `json:"token"`
		}{t, raw})
		return nil
	})
	s.api("DELETE /api/v1/tokens/{id}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		if err := s.DeleteToken(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	s.api("GET /api/v1/ntfy/ignored", s.handleNtfyIgnored)
	s.api("GET /api/v1/ws", s.handleWS)
	s.api("GET /api/v1/events", s.handleEvents)
	s.api("GET /api/v1/events/sse", s.handleSSE)

	// By external id, for scripts and monitors. No cookie: never a browser.
	for _, action := range []string{"resolve", "ack"} {
		s.route("POST /{topic}/{external_id}/"+action, func(w http.ResponseWriter, r *http.Request) error {
			var in struct {
				Via string `json:"via"`
			}
			_ = decodeJSON(r, &in) // optional: scripts send no body
			n, err := s.ActByKey(r.Context(), s.auth(r, false), r.PathValue("topic"), r.PathValue("external_id"), action, cleanVia(in.Via, "api"))
			if err != nil {
				return err
			}
			writeJSON(w, 200, n)
			return nil
		})
	}
	s.webhookRoutes()
	s.hookOutRoutes()
	s.silenceRoutes()
	s.ntfyRoutes()
}

// snoozeUntil reads {"for":"1h"} or {"until":"RFC3339"}.
func snoozeUntil(forStr string, until *time.Time) (*time.Time, error) {
	if until != nil {
		return until, nil
	}
	d, err := parseDuration(forStr)
	if err != nil || d <= 0 {
		return nil, errf(400, "bad_request", "snooze needs \"for\" (like 1h) or \"until\" (RFC3339)")
	}
	t := time.Now().Add(d)
	return &t, nil
}
