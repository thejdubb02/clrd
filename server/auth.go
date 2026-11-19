package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Token scopes (spec section 4).
const (
	ScopePublish = "publish"
	ScopeRead    = "read"
	ScopeAdmin   = "admin"
)

const tokenPrefix = "clrd_"

// ntfyTokenPrefix: tokens imported from ntfy keep working (issue #117).
const ntfyTokenPrefix = "tk_"

func isToken(raw string) bool {
	return strings.HasPrefix(raw, tokenPrefix) || strings.HasPrefix(raw, ntfyTokenPrefix)
}

const sessionCookie = "clrd_session"

// Grant is one per-topic rule (spec section 4). Topic is a name or a
// pattern with *, matched ignoring case; Scopes is read and/or publish, empty
// for deny. The first grant that matches decides.
type Grant struct {
	Topic  string   `json:"topic"`
	Scopes []string `json:"scopes"`
}

// Token is an API token. Name is the device name shown as state_by.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	Topics     []string   `json:"topics"`           // empty = every topic
	Grants     []Grant    `json:"grants,omitempty"` // set = only these, in order
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

// limited reports whether the token is held to some topics.
func (t *Token) limited() bool { return len(t.Topics) > 0 || len(t.Grants) > 0 }

// may reports whether the token may use scope on topic. Topic "" asks only
// about the scope. Every topic check goes through here.
func (t *Token) may(scope, topic string) bool {
	if !t.Has(scope) {
		return false
	}
	if topic == "" {
		return true
	}
	if len(t.Grants) > 0 {
		lt := strings.ToLower(topic)
		for _, g := range t.Grants {
			if ok, _ := path.Match(strings.ToLower(g.Topic), lt); ok {
				return slices.Contains(g.Scopes, scope)
			}
		}
		return false
	}
	return len(t.Topics) == 0 || slices.Contains(t.Topics, topic)
}

// Has reports whether the token holds scope (admin holds all).
func (t *Token) Has(scope string) bool {
	return slices.Contains(t.Scopes, scope) || slices.Contains(t.Scopes, ScopeAdmin)
}

// seesAll: a topic-limited token only learns about devices limited to its
// own topics; a token with grants only about itself.
func (t *Token) seesAll(o *Token) bool {
	if len(t.Grants) > 0 || len(o.Grants) > 0 {
		return !t.limited() || o.ID == t.ID
	}
	if len(t.Topics) == 0 {
		return true
	}
	if len(o.Topics) == 0 {
		return false
	}
	for _, topic := range o.Topics {
		if !slices.Contains(t.Topics, topic) {
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
	if !t.may(scope, topic) {
		return errf(403, "forbidden", "token may not %s topic %q", scope, topic)
	}
	return nil
}

// needFullAdmin: a topic-limited admin could otherwise mint an unlimited token.
func (t *Token) needFullAdmin() error {
	if err := t.need(ScopeAdmin, ""); err != nil {
		return err
	}
	if t.limited() {
		return errf(403, "forbidden", "managing tokens needs an admin token without a topic list")
	}
	return nil
}

var grantPattern = regexp.MustCompile(`^[A-Za-z0-9_*-]{1,64}$`)

// checkToken validates a token before it is stored.
func checkToken(t *Token) error {
	if t.Name == "" {
		return errf(400, "bad_request", "name required")
	}
	if len(t.Scopes) == 0 {
		return errf(400, "bad_request", "at least one scope required")
	}
	for _, sc := range t.Scopes {
		if sc != ScopePublish && sc != ScopeRead && sc != ScopeAdmin {
			return errf(400, "bad_request", "unknown scope %q", sc)
		}
	}
	for _, tp := range t.Topics {
		if err := validTopic(tp); err != nil {
			return err
		}
	}
	if len(t.Grants) == 0 {
		return nil
	}
	if len(t.Topics) > 0 || slices.Contains(t.Scopes, ScopeAdmin) {
		return errf(400, "bad_request", "grants cannot go with topics or the admin scope")
	}
	for _, g := range t.Grants {
		if !grantPattern.MatchString(g.Topic) {
			return errf(400, "bad_request", "bad grant topic %q", g.Topic)
		}
		for _, sc := range g.Scopes {
			if (sc != ScopePublish && sc != ScopeRead) || !slices.Contains(t.Scopes, sc) {
				return errf(400, "bad_request", "grant scope %q must be read or publish and held by the token", sc)
			}
		}
	}
	return nil
}

// insertToken stores t with the hash of its secret.
func insertToken(ctx context.Context, db execer, t *Token, hash string) error {
	if err := checkToken(t); err != nil {
		return err
	}
	if t.Topics == nil {
		t.Topics = []string{}
	}
	grants := ""
	if len(t.Grants) > 0 {
		b, _ := json.Marshal(t.Grants)
		grants = string(b)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO tokens (id, name, hash, scopes, topics, grants, created_at) VALUES (?,?,?,?,?,?,?)`,
		t.ID, t.Name, hash, strings.Join(t.Scopes, ","), strings.Join(t.Topics, ","), grants, ms(t.CreatedAt))
	return err
}

type execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
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
	return s.createToken(ctx, &Token{Name: name, Scopes: scopes, Topics: topics})
}

func (s *Server) createToken(ctx context.Context, t *Token) (*Token, string, error) {
	raw := tokenPrefix + randomString(32)
	t.ID, t.CreatedAt = randomString(12), now()
	if err := insertToken(ctx, s.db, t, hashToken(raw)); err != nil {
		return nil, "", err
	}
	return t, raw, nil
}

const tokenCols = `id, name, scopes, topics, grants, created_at, last_seen_at`

func scanToken(r scanner) (*Token, error) {
	var t Token
	var scopes, topics, grants string
	var created int64
	var seen sql.NullInt64
	if err := r.Scan(&t.ID, &t.Name, &scopes, &topics, &grants, &created, &seen); err != nil {
		return nil, err
	}
	if grants != "" {
		if err := json.Unmarshal([]byte(grants), &t.Grants); err != nil || len(t.Grants) == 0 {
			return nil, fmt.Errorf("token %s: bad grants", t.ID) // never fall back to no limits
		}
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

// DeleteToken revokes a token and every web session signed in with it.
func (s *Server) DeleteToken(ctx context.Context, id string) error {
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE k(id) AS
	  (SELECT ? UNION SELECT token_id FROM sessions JOIN k ON parent_id = k.id)
	  DELETE FROM tokens WHERE id IN k RETURNING id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for ; rows.Next(); n++ {
		var gone string
		if err := rows.Scan(&gone); err != nil {
			return err
		}
		s.revoked.Store(gone, true) // streams check this before every send
		s.hub.drop(gone)            // and wake on this, so an idle stream closes now
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		return errf(404, "not_found", "token not found")
	}
	return nil
}

func (s *Server) lookupToken(ctx context.Context, raw string) *Token {
	if !isToken(raw) {
		return nil
	}
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE hash = ?`, hashToken(raw)))
	if err != nil {
		return nil
	}
	s.touch(ctx, t)
	return t
}

// touch records that a device was seen, at most once a minute per token.
func (s *Server) touch(ctx context.Context, t *Token) {
	n := time.Now()
	if last, ok := s.seen.Load(t.ID); ok && n.Sub(last.(time.Time)) < time.Minute {
		return
	}
	s.seen.Store(t.ID, n)
	if _, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_seen_at = ? WHERE id = ?`, ms(n), t.ID); err != nil && ctx.Err() == nil {
		s.log.Warn("touch token", "err", err)
	}
}

// rawToken pulls a token from the request: Bearer, Basic (ntfy style: the
// token as password or user), ?auth= (ntfy: base64 of an Authorization
// header value), then, when cookie is true, the session cookie.
func rawToken(r *http.Request, cookie bool) (raw string, fromCookie bool) {
	h := authHeader(r)
	scheme, v, _ := strings.Cut(h, " ")
	if strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(v), false
	}
	if strings.EqualFold(scheme, "Basic") {
		r2 := &http.Request{Header: http.Header{"Authorization": {"Basic " + v}}}
		if u, p, ok := r2.BasicAuth(); ok {
			if isToken(p) {
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

// authHeader is the Authorization header, or ntfy's ?auth= (base64 of one).
func authHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if a := r.URL.Query().Get("auth"); h == "" && a != "" {
		for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
			if b, err := enc.DecodeString(a); err == nil {
				return string(b)
			}
		}
		return a // not base64: still a credential, so never anonymous
	}
	return h
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
	if fromCookie && r.Method != http.MethodGet && r.Method != http.MethodHead && !s.sameOrigin(r) {
		return nil
	}
	return s.lookupToken(r.Context(), raw)
}

// sameOrigin: SameSite=Lax already blocks cross-site cookie POSTs; this also
// stops sibling subdomains. A missing Origin header (old browsers) passes.
func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host != r.Host {
		return false
	}
	return u.Scheme == "https" || !s.https(r) // a plain-http page cannot act on an https site
}

func (s *Server) https(r *http.Request) bool {
	return r.TLS != nil || strings.HasPrefix(s.opt.BaseURL, "https://")
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.https(r) || r.Header.Get("X-Forwarded-Proto") == "https"}) // behind a TLS proxy; a spoofed header only adds Secure
}

// handleSession signs a browser in: JSON {"token"} or a form post with
// token and optional next.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) error {
	if !s.sameOrigin(r) {
		return errf(403, "forbidden", "sign-in must come from this site")
	}
	isForm := !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
	var in struct {
		Token string `json:"token"`
		Next  string `json:"next"`
		Name  string `json:"name"`
	}
	if isForm {
		in.Token, in.Next, in.Name = r.PostFormValue("token"), r.PostFormValue("next"), r.PostFormValue("name")
	} else if err := decodeJSON(r, &in); err != nil {
		return err
	}
	// Parse first: a control character fails url.Parse, http.Redirect then
	// skips its cleaning, and a browser strips "/\t/evil.com" to "//evil.com".
	// No backslash anywhere: http.Redirect cleans "/./\evil.com" to "/\evil.com".
	next := in.Next
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" ||
		!strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
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
		if in.Name != "" {
			q.Set("name", in.Name) // the page fills it back in (issue #33)
		}
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusSeeOther)
		return nil
	}
	if t == nil {
		return errf(401, "unauthorized", "invalid token")
	}
	st, err := s.startSession(w, r, t, in.Name)
	if err != nil {
		return err
	}
	if isForm {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return nil
	}
	writeJSON(w, 200, st)
	return nil
}

// startSession signs this browser in with its own token (issue #30): same
// scopes, topics and grants as parent, its own name in the device list and in
// state_by, revocable alone, and revoked with its parent.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, parent *Token, name string) (*Token, error) {
	if err := s.endSession(r); err != nil {
		return nil, err
	}
	ctx := context.WithoutCancel(r.Context()) // never leave a half-made session
	st, raw, err := s.createToken(ctx, &Token{Name: deviceName(name, r.UserAgent()), Scopes: parent.Scopes, Topics: parent.Topics, Grants: parent.Grants})
	if err != nil {
		return nil, err
	}
	// Only while the parent exists, so a racing revoke cannot miss this one.
	res, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_id, parent_id) SELECT ?, id FROM tokens WHERE id = ?`, st.ID, parent.ID)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			err = errf(401, "unauthorized", "token revoked")
		}
	}
	if err != nil {
		if derr := s.DeleteToken(ctx, st.ID); derr != nil {
			s.log.Warn("drop half-made session", "err", derr)
		}
		return nil, err
	}
	s.capSessions(ctx, parent.ID)
	s.setCookie(w, r, raw, 365*24*3600)
	return st, nil
}

// maxSessions is how many browsers one token may have signed in at once.
// ponytail: fixed cap; make it an option if someone needs more.
const maxSessions = 20

// capSessions revokes the least recently used browsers of parent past
// maxSessions, so a sign-in loop cannot grow the token table (issue #50).
func (s *Server) capSessions(ctx context.Context, parent string) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_id FROM sessions JOIN tokens ON id = token_id
	  WHERE parent_id = ? ORDER BY COALESCE(last_seen_at, created_at) DESC, tokens.rowid DESC LIMIT -1 OFFSET ?`, parent, maxSessions)
	if err != nil {
		s.log.Warn("cap sessions", "err", err)
		return
	}
	var old []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			old = append(old, id)
		}
	}
	rows.Close()
	for _, id := range old {
		if err := s.DeleteToken(ctx, id); err != nil && !isNotFound(err) {
			s.log.Warn("cap sessions", "err", err)
		}
	}
}

// endSession revokes the token in this browser's cookie, only when sign-in
// minted it. Cookies set before #30 hold a real token; keep those.
func (s *Server) endSession(r *http.Request) error {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	var id string
	err = s.db.QueryRowContext(r.Context(), `SELECT token_id FROM sessions JOIN tokens ON id = token_id WHERE hash = ?`,
		hashToken(c.Value)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err == nil {
		if err = s.DeleteToken(r.Context(), id); isNotFound(err) { // a racing sign-out got it first
			err = nil
		}
	}
	return err
}

// deviceName is the name a signed-in browser shows as: the one given, else
// a label from the User-Agent such as "Firefox on Linux".
func deviceName(name, ua string) string {
	name = strings.Join(strings.FieldsFunc(name, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }), " ")
	if name == "" {
		pick := func(pairs ...string) string {
			for i := 0; i < len(pairs); i += 2 {
				if strings.Contains(ua, pairs[i]) {
					return pairs[i+1]
				}
			}
			return ""
		}
		name = pick("Edg/", "Edge", "OPR/", "Opera", "Firefox/", "Firefox", "Chrome/", "Chrome", "Safari/", "Safari")
		if name == "" {
			name = "Web browser"
		}
		if sys := pick("Android", "Android", "iPhone", "iOS", "iPad", "iPadOS", "Windows", "Windows",
			"CrOS", "ChromeOS", "Mac OS X", "macOS", "Linux", "Linux"); sys != "" {
			name += " on " + sys
		}
	}
	if r := []rune(name); len(r) > 64 {
		name = string(r[:64])
	}
	return name
}

func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) error {
	if !s.sameOrigin(r) {
		return errf(403, "forbidden", "sign-out must come from this site")
	}
	if err := s.endSession(r); err != nil {
		return err // keep the cookie so a retry can still revoke its token
	}
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
	// The browser gets its own token too, so signing out never revokes the
	// admin token shown below. If that fails, still show the admin token.
	if _, err := s.startSession(w, r, t, ""); err != nil {
		s.log.Warn("setup sign-in", "err", err)
	}
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
