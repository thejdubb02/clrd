package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Token scopes (spec section 4).
const (
	ScopePublish = "publish"
	ScopeRead    = "read"
	ScopeAdmin   = "admin"
)

const tokenPrefix = "clrd_"
const sessionCookie = "clrd_session"

// Token is an API token. Name is the device name shown as state_by.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	Topics     []string   `json:"topics"` // empty = every topic
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

// Allows reports whether the token's topic allow-list admits topic.
func (t *Token) Allows(topic string) bool {
	return len(t.Topics) == 0 || slices.Contains(t.Topics, topic)
}

// Has reports whether the token holds scope (admin holds all).
func (t *Token) Has(scope string) bool {
	return slices.Contains(t.Scopes, scope) || slices.Contains(t.Scopes, ScopeAdmin)
}

// need checks scope and, when topic is set, the allow-list.
// seesAll: a topic-limited token only learns about devices limited to its own topics.
func (t *Token) seesAll(o *Token) bool {
	if len(t.Topics) == 0 {
		return true
	}
	if len(o.Topics) == 0 {
		return false
	}
	for _, topic := range o.Topics {
		if !t.Allows(topic) {
			return false
		}
	}
	return true
}

func (t *Token) need(scope, topic string) error {
	if t == nil {
		return errf(401, "unauthorized", "missing or invalid token")
	}
	if !t.Has(scope) {
		return errf(403, "forbidden", "token lacks the %s scope", scope)
	}
	if topic != "" && !t.Allows(topic) {
		return errf(403, "forbidden", "token may not use topic %q", topic)
	}
	return nil
}

// needFullAdmin: a topic-limited admin could otherwise mint an unlimited token.
func (t *Token) needFullAdmin() error {
	if err := t.need(ScopeAdmin, ""); err != nil {
		return err
	}
	if len(t.Topics) > 0 {
		return errf(403, "forbidden", "managing tokens needs an admin token without a topic list")
	}
	return nil
}

func randomString(n int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		rand.Read(buf)
		if buf[0] < 248 { // 248 = 4*62, keeps the draw uniform
			out = append(out, alphabet[buf[0]%62])
		}
	}
	return string(out)
}

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CreateToken stores a new token and returns it with its secret value,
// which is shown once and never stored.
func (s *Server) CreateToken(ctx context.Context, name string, scopes, topics []string) (*Token, string, error) {
	if name == "" {
		return nil, "", errf(400, "bad_request", "name required")
	}
	if len(scopes) == 0 {
		return nil, "", errf(400, "bad_request", "at least one scope required")
	}
	for _, sc := range scopes {
		if sc != ScopePublish && sc != ScopeRead && sc != ScopeAdmin {
			return nil, "", errf(400, "bad_request", "unknown scope %q", sc)
		}
	}
	for _, t := range topics {
		if err := validTopic(t); err != nil {
			return nil, "", err
		}
	}
	raw := tokenPrefix + randomString(32)
	t := &Token{ID: randomString(12), Name: name, Scopes: scopes, Topics: topics, CreatedAt: now()}
	if t.Topics == nil {
		t.Topics = []string{}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (id, name, hash, scopes, topics, created_at) VALUES (?,?,?,?,?,?)`,
		t.ID, t.Name, hashToken(raw), strings.Join(scopes, ","), strings.Join(t.Topics, ","), ms(t.CreatedAt))
	return t, raw, err
}

const tokenCols = `id, name, scopes, topics, created_at, last_seen_at`

func scanToken(r scanner) (*Token, error) {
	var t Token
	var scopes, topics string
	var created int64
	var seen sql.NullInt64
	if err := r.Scan(&t.ID, &t.Name, &scopes, &topics, &created, &seen); err != nil {
		return nil, err
	}
	t.Scopes, t.Topics, t.CreatedAt = splitList(scopes), splitList(topics), fromMS(created)
	if seen.Valid {
		v := fromMS(seen.Int64)
		t.LastSeenAt = &v
	}
	return &t, nil
}

// Tokens lists tokens. When seenSince is non-zero, only those seen after it.
func (s *Server) Tokens(ctx context.Context, seenSince time.Time) ([]*Token, error) {
	q, args := `SELECT `+tokenCols+` FROM tokens ORDER BY created_at`, []any{}
	if !seenSince.IsZero() {
		q, args = `SELECT `+tokenCols+` FROM tokens WHERE last_seen_at > ? ORDER BY last_seen_at DESC`, []any{ms(seenSince)}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken revokes a token.
func (s *Server) DeleteToken(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errf(404, "not_found", "token not found")
	}
	s.revoked.Store(id, true) // open streams check this before every send
	return nil
}

func (s *Server) lookupToken(ctx context.Context, raw string) *Token {
	if !strings.HasPrefix(raw, tokenPrefix) {
		return nil
	}
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE hash = ?`, hashToken(raw)))
	if err != nil {
		return nil
	}
	s.touch(t)
	return t
}

// touch records that a device was seen, at most once a minute per token.
func (s *Server) touch(t *Token) {
	n := time.Now()
	if last, ok := s.seen.Load(t.ID); ok && n.Sub(last.(time.Time)) < time.Minute {
		return
	}
	s.seen.Store(t.ID, n)
	if _, err := s.db.Exec(`UPDATE tokens SET last_seen_at = ? WHERE id = ?`, ms(n), t.ID); err != nil {
		s.log.Warn("touch token", "err", err)
	}
}

// rawToken pulls a token from the request: Bearer, Basic (ntfy style: the
// token as password or user), ?auth= (ntfy: base64 of an Authorization
// header value), then, when cookie is true, the session cookie.
func rawToken(r *http.Request, cookie bool) (raw string, fromCookie bool) {
	h := r.Header.Get("Authorization")
	if a := r.URL.Query().Get("auth"); h == "" && a != "" {
		for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
			if b, err := enc.DecodeString(a); err == nil {
				h = string(b)
				break
			}
		}
	}
	scheme, v, _ := strings.Cut(h, " ")
	if strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(v), false
	}
	if strings.EqualFold(scheme, "Basic") {
		r2 := &http.Request{Header: http.Header{"Authorization": {"Basic " + v}}}
		if u, p, ok := r2.BasicAuth(); ok {
			if strings.HasPrefix(p, tokenPrefix) {
				return p, false
			}
			return u, false
		}
	}
	if c, err := r.Cookie(sessionCookie); cookie && err == nil {
		return c.Value, true
	}
	return "", false
}

// Authenticate returns the token behind the request (Bearer, Basic,
// ?auth= or the session cookie), or nil. For mounted handlers (web, MCP).
// Cookie-authenticated writes from another origin are refused.
func (s *Server) Authenticate(r *http.Request) *Token {
	return s.auth(r, true)
}

func (s *Server) auth(r *http.Request, cookie bool) *Token {
	raw, fromCookie := rawToken(r, cookie)
	if raw == "" {
		return nil
	}
	if fromCookie && r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
		return nil
	}
	return s.lookupToken(r.Context(), raw)
}

// sameOrigin: SameSite=Lax already blocks cross-site cookie POSTs; this also
// stops sibling subdomains. A missing Origin header (old browsers) passes.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || strings.HasPrefix(s.opt.BaseURL, "https://")})
}

// handleSession signs a browser in: JSON {"token"} or a form post with
// token and optional next.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) error {
	if !sameOrigin(r) {
		return errf(403, "forbidden", "sign-in must come from this site")
	}
	isForm := !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
	var in struct {
		Token string `json:"token"`
		Next  string `json:"next"`
	}
	if isForm {
		in.Token, in.Next = r.PostFormValue("token"), r.PostFormValue("next")
	} else if err := decodeJSON(r, &in); err != nil {
		return err
	}
	next := in.Next
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		next = "/app"
	}
	t := s.lookupToken(r.Context(), strings.TrimSpace(in.Token))
	if t == nil && isForm { // back to the page, which shows the error (issue #14)
		u, err := url.Parse(next)
		if err != nil {
			u = &url.URL{Path: "/app"}
		}
		q := u.Query()
		q.Set("error", "token")
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusSeeOther)
		return nil
	}
	if t == nil {
		return errf(401, "unauthorized", "invalid token")
	}
	s.setCookie(w, r, strings.TrimSpace(in.Token), 365*24*3600)
	if isForm {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return nil
	}
	writeJSON(w, 200, t)
	return nil
}

func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) error {
	s.setCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) hasAdmin(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens WHERE ','||scopes||',' LIKE '%,admin,%'`).Scan(&n)
	return n > 0, err
}

// bootstrap creates a one-time setup code when no admin token exists.
func (s *Server) bootstrap(ctx context.Context) error {
	if ok, err := s.hasAdmin(ctx); ok || err != nil {
		return err
	}
	s.setupMu.Lock()
	s.setupCode = randomString(24)
	s.setupMu.Unlock()
	s.log.Info("no admin token yet: open this link once to create one (or run `clrd token create`)",
		"url", strings.TrimSuffix(s.opt.BaseURL, "/")+"/setup?code="+s.setupCode)
	return nil
}

var setupPage = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>clrd setup</title><style>body{font:16px system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem;color:#0F1114;background:#F5F3EE}
input,button{font:inherit;padding:.5rem .75rem;border:1px solid #DEDAD1;border-radius:12px}button{background:#0B7A55;color:#fff;border:0}
code{font-family:ui-monospace,monospace;background:#fff;padding:.25rem .5rem;border-radius:8px;word-break:break-all}</style></head><body>
<h1>clrd setup</h1>
{{if .Token}}<p>Admin token for <b>{{.Name}}</b>. Copy it now; it is not shown again.</p><p><code>{{.Token}}</code></p>
<p>This browser is signed in. <a href="/app">Open the inbox</a>.</p>
{{else}}<p>Create the first admin token.</p>
<form method="post" action="/setup"><input type="hidden" name="code" value="{{.Code}}">
<p><label>Device name <input name="name" value="admin" required></label></p><p><button>Create admin token</button></p></form>{{end}}
</body></html>`))

// handleSetup: GET or HEAD shows the form (and never uses the code up), POST
// creates the admin token, shows it once and signs the browser in. The code
// dies as soon as any admin token exists, including one made on the CLI.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) error {
	code := r.FormValue("code")
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.setupCode != "" {
		ok, err := s.hasAdmin(r.Context())
		if err != nil {
			return err
		}
		if ok {
			s.setupCode = ""
		}
	}
	if s.setupCode == "" || subtle.ConstantTimeCompare([]byte(code), []byte(s.setupCode)) != 1 {
		return errf(404, "not_found", "no setup pending, or wrong code")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		return setupPage.Execute(w, map[string]string{"Code": code})
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		name = "admin"
	}
	t, raw, err := s.CreateToken(r.Context(), name, []string{ScopeAdmin}, nil)
	if err != nil {
		return err
	}
	s.setupCode = ""
	s.setCookie(w, r, raw, 365*24*3600)
	return setupPage.Execute(w, map[string]string{"Token": raw, "Name": t.Name})
}

// apiError is an HTTP error with the spec's {"error","code"} body.
type apiError struct {
	status    int
	code, msg string
}

func (e *apiError) Error() string { return e.msg }

func errf(status int, code, format string, args ...any) error {
	return &apiError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

func errorStatus(err error) (int, string, string) {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status, ae.code, ae.msg
	}
	return 500, "internal", "internal error"
}
