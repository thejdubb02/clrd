// Package server is the clrd server: SQLite store, event log, native and
// ntfy-compatible HTTP API, WebSocket and SSE sync. See server/README.md for
// how other packages mount pages on it.
package server

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
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
	DB        string        // CLRD_DB, default "/data/clrd.db"
	BaseURL   string        // CLRD_BASE_URL, public URL used in links
	Retention time.Duration // CLRD_RETENTION, default 720h
	Logger    *slog.Logger  // default slog.Default()

	Attachments     string // CLRD_ATTACHMENTS, default "attachments" next to DB
	AttachmentMax   int64  // CLRD_ATTACHMENT_MAX_MB, bytes, default 15 MB
	AttachmentTotal int64  // CLRD_ATTACHMENT_TOTAL_MB, bytes, default 5000 MB
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
	return o, nil
}

// Server is the clrd server. Create it with New, add pages with Mount, then Run.
type Server struct {
	opt Options
	db  *sql.DB
	log *slog.Logger
	hub hub

	mu       sync.Mutex // serialises writes: seq order = delivery order
	lastMS   int64      // newID state, guarded by mu
	lastRand [10]byte
	seq      atomic.Int64 // last committed event seq
	pruned   atomic.Int64 // events with seq <= pruned are gone
	epoch    string       // this database's id, sent with every cursor
	revoked  sync.Map     // token ids deleted while the server runs
	failing  sync.Map     // background job name -> true while it keeps failing; /healthz reports it

	seen      sync.Map // token id -> time last written to last_seen_at
	setupMu   sync.Mutex
	setupCode string

	mux    *http.ServeMux
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
		opt.DB = "/data/clrd.db"
	}
	if opt.BaseURL == "" {
		opt.BaseURL = "http://localhost" + opt.Listen[strings.LastIndex(opt.Listen, ":"):]
	}
	opt.BaseURL = strings.TrimSuffix(opt.BaseURL, "/")
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
	s := &Server{opt: opt, db: db, log: opt.Logger, mux: http.NewServeMux()}
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
	s.epoch = strconv.FormatInt(epoch, 36)
	s.seq.Store(seq)
	s.pruned.Store(pruned)
	s.routesSetup()
	return s, nil
}

// Close closes the database.
func (s *Server) Close() error { return s.db.Close() }

// CurrentSeq is the seq of the last committed event.
func (s *Server) CurrentSeq() int64 { return s.seq.Load() }

// Epoch is this database's id, sent with every seq (see SPEC, Epoch).
func (s *Server) Epoch() string { return s.epoch }

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
	go s.every(ctx, "prune", time.Hour, s.prune)
	srv := &http.Server{Addr: s.opt.Listen, Handler: s, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }} // ends open streams on shutdown
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Info("clrd listening", "addr", s.opt.Listen, "base_url", s.opt.BaseURL)
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
			return errf(401, "unauthorized", "missing or invalid token")
		}
		return h(w, r, tok)
	})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	status, code, msg := errorStatus(err)
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
	s.route("DELETE /api/v1/session", s.handleSignOut)

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
	for _, action := range []string{"ack", "snooze", "dismiss", "resolve", "reopen"} {
		s.api("POST /api/v1/notifications/{id}/"+action, func(w http.ResponseWriter, r *http.Request, tok *Token) error {
			var until *time.Time
			if action == "snooze" {
				var err error
				if until, err = snoozeUntil(r); err != nil {
					return err
				}
			}
			n, err := s.Act(r.Context(), tok, r.PathValue("id"), action, until)
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
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		n, err := s.Reply(r.Context(), tok, r.PathValue("id"), in.Reply, in.Seq)
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
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		n, err := s.Clear(r.Context(), tok, in.Topic, in.IDs)
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
	s.api("PUT /api/v1/topics/{topic}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var in struct {
			Icon *string `json:"icon"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.Icon == nil { // a missing key must not wipe the icon
			return errf(400, "bad_request", `body needs "icon": a URL, or "" to remove it`)
		}
		if err := s.SetTopicIcon(r.Context(), tok, r.PathValue("topic"), *in.Icon); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]string{"topic": r.PathValue("topic"), "icon": *in.Icon})
		return nil
	})
	s.api("GET /api/v1/devices", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		ts, err := s.Tokens(r.Context(), time.Now().Add(-10*time.Minute))
		if err != nil {
			return err
		}
		type device struct {
			ID         string     `json:"id"`
			Name       string     `json:"name"`
			LastSeenAt *time.Time `json:"last_seen_at"`
		}
		out := []device{}
		for _, t := range ts {
			if tok.seesAll(t) {
				out = append(out, device{t.ID, t.Name, t.LastSeenAt})
			}
		}
		writeJSON(w, 200, map[string]any{"devices": out})
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
	s.api("GET /api/v1/ws", s.handleWS)
	s.api("GET /api/v1/events", s.handleEvents)
	s.api("GET /api/v1/events/sse", s.handleSSE)

	// By external id, for scripts and monitors. No cookie: never a browser.
	for _, action := range []string{"resolve", "ack"} {
		s.route("POST /{topic}/{external_id}/"+action, func(w http.ResponseWriter, r *http.Request) error {
			n, err := s.ActByKey(r.Context(), s.auth(r, false), r.PathValue("topic"), r.PathValue("external_id"), action)
			if err != nil {
				return err
			}
			writeJSON(w, 200, n)
			return nil
		})
	}
	s.ntfyRoutes()
}

// snoozeUntil reads {"for":"1h"} or {"until":"RFC3339"}.
func snoozeUntil(r *http.Request) (*time.Time, error) {
	var in struct {
		For   string     `json:"for"`
		Until *time.Time `json:"until"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return nil, err
	}
	if in.Until != nil {
		return in.Until, nil
	}
	d, err := parseDuration(in.For)
	if err != nil || d <= 0 {
		return nil, errf(400, "bad_request", "snooze needs \"for\" (like 1h) or \"until\" (RFC3339)")
	}
	t := time.Now().Add(d)
	return &t, nil
}
