package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Agent invites (issue #454, spec section 4): a person, or an admin token,
// makes one short string. An agent claims it once, with no token, and gets a
// user of kind agent with its own token, held to the topics the invite named
// and denied for approvals (#187). Invites live in s.pairs next to pair and
// link codes (pair.go): same single use, expiry and 429 cap, their own kind.

const (
	inviteTTL    = 15 * time.Minute
	inviteMinTTL = time.Minute
	inviteMaxTTL = time.Hour
)

// invite is what an agent invite holds until it is claimed.
type invite struct {
	name   string
	grants []Grant // exact topics from a person, patterns only from a full admin
	owner  string  // the creator's top token: the agent shows in its device list
	by     string  // the creator's device name, for the device log
}

func (s *Server) agentRoutes() {
	s.api("POST /api/v1/agents/invite", s.handleInviteCreate)
	s.route("POST /api/v1/agents/claim", s.handleInviteClaim)
	s.route("GET /agent", s.handleInvitePage)
}

// mayInvite refuses tok unless it is a full admin token or a browser or
// phone of a person (not of an agent), so an agent never invites another
// with only its own token (#454).
func (s *Server) mayInvite(ctx context.Context, tok *Token) error {
	if tok.needFullAdmin() == nil {
		return nil
	}
	var session, agent bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE token_id = ?),
	  EXISTS (SELECT 1 FROM people WHERE id = ? AND kind = 'agent')`, tok.ID, tok.User).Scan(&session, &agent)
	if err == nil && (!session || agent) {
		err = errf(403, "forbidden", "an agent invite needs a person signed in on the web or the app, or an admin token")
	}
	return err
}

// MayInvite says whether tok may make an agent invite, so the web inbox
// offers the form only where it works (#456).
func (s *Server) MayInvite(ctx context.Context, tok *Token) bool { return s.mayInvite(ctx, tok) == nil }

// inviteGrants turns the topics asked for into the agent's grants: each gets
// the read and publish tok holds there, never more. Only a full admin may
// name a pattern.
func inviteGrants(tok *Token, topics []string) ([]Grant, error) {
	if len(topics) == 0 || len(topics) > 64 {
		return nil, errf(400, "bad_request", "topics required: 1 to 64 topic names")
	}
	admin := tok.needFullAdmin() == nil
	out := []Grant{}
	for _, t := range topics {
		if admin {
			if !grantPattern.MatchString(t) {
				return nil, errf(400, "bad_request", "invalid topic %q: letters, digits, _, - and *, at most 64", t)
			}
		} else if err := validTopic(t); err != nil {
			return nil, err
		}
		g := Grant{Topic: t, Scopes: []string{}}
		for _, sc := range []string{ScopeRead, ScopePublish} {
			if admin || tok.may(sc, t) {
				g.Scopes = append(g.Scopes, sc)
			}
		}
		if len(g.Scopes) == 0 {
			return nil, errf(403, "forbidden", "you may not use topic %q, so neither may an agent you invite", t)
		}
		out = append(out, g)
	}
	return out, nil
}

func agentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", errf(400, "bad_request", "name required, at most 64 characters")
	}
	return name, nil
}

func (s *Server) nameTaken(ctx context.Context, name string) error {
	var taken bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM people WHERE name = ?)`, name).Scan(&taken)
	if err == nil && taken {
		err = errf(409, "conflict", "a user named %q exists", name)
	}
	return err
}

func (s *Server) handleInviteCreate(w http.ResponseWriter, r *http.Request, tok *Token) error {
	var in struct {
		Name      string   `json:"name"`
		Topics    []string `json:"topics"`
		ExpiresIn string   `json:"expires_in"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.mayInvite(ctx, tok); err != nil {
		return err
	}
	name, err := agentName(in.Name)
	if err != nil {
		return err
	}
	grants, err := inviteGrants(tok, in.Topics)
	if err != nil {
		return err
	}
	ttl := inviteTTL
	if in.ExpiresIn != "" {
		if ttl, err = time.ParseDuration(in.ExpiresIn); err != nil || ttl < inviteMinTTL || ttl > inviteMaxTTL {
			return errf(400, "bad_request", "expires_in: a duration from 1m to 1h, like \"15m\"")
		}
	}
	if err := s.nameTaken(ctx, name); err != nil {
		return err
	}
	top, err := s.topOf(ctx, tok.ID)
	if err != nil {
		return err
	}
	p := &pairing{ttl: ttl, invite: &invite{name: name, grants: grants, owner: top, by: tok.Name}}
	code, err := s.addPairing(p)
	if err != nil {
		return err
	}
	base := s.baseFor(r)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]any{
		"code":       code,
		"invite":     "clrd://agent?" + url.Values{"server": {base}, "code": {code}}.Encode(),
		"url":        base + "/agent#" + code,
		"expires_at": p.expires().UTC(),
		"name":       name,
		"grants":     grants,
	})
	return nil
}

// handleInviteClaim spends the code: the agent's user and token, in one
// transaction, and the token once with the guide.
func (s *Server) handleInviteClaim(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	s.pairMu.Lock()
	p := s.pairingLocked(in.Code, kindAgent)
	if p != nil {
		delete(s.pairs, in.Code) // single use, even when the claim below fails
	}
	s.pairMu.Unlock()
	if p == nil {
		return errf(410, "gone", "that invite expired or was already used")
	}
	iv := p.invite
	ctx := context.WithoutCancel(r.Context())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	uid := randomString(12)
	gs, _ := json.Marshal(iv.grants)
	// Only while the creator's token still exists: revoking it kills its invites.
	res, err := tx.ExecContext(ctx, `INSERT INTO people (id, name, grants, created_at, kind, owner)
	  SELECT ?,?,?,?,'agent',id FROM tokens WHERE id = ?`, uid, iv.name, string(gs), ms(now()), iv.owner)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errf(409, "conflict", "a user named %q was made since the invite; ask for a new one", iv.name)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errf(410, "gone", "the invite's maker was revoked")
	}
	// Token scopes are only those some grant holds: publish-only stays publish-only.
	var scopes []string
	for _, sc := range []string{ScopeRead, ScopePublish} {
		if slices.ContainsFunc(iv.grants, func(g Grant) bool { return slices.Contains(g.Scopes, sc) }) {
			scopes = append(scopes, sc)
		}
	}
	raw := tokenPrefix + randomString(32)
	t := &Token{ID: randomString(12), Name: iv.name, Scopes: scopes, Grants: iv.grants,
		User: uid, Approve: ApproveDenied, CreatedAt: now()}
	if err := insertToken(ctx, tx, t, hashToken(raw)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.logDevice(ctx, iv.owner, "linked", iv.name, t.ID, iv.by)
	s.devicesChanged.fire()
	g := s.guide(s.baseFor(r), iv)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"token": raw, "user": uid, "name": iv.name, "guide": g})
	return nil
}

// handleInvitePage is the https form, for an agent that can only fetch a
// URL: how to claim, as plain text. The code rides after # in the link, so
// it never reaches the server, a proxy's log or a Referer (#463); the page
// is the same for every invite, and the claim answer holds the topics.
func (s *Server) handleInvitePage(w http.ResponseWriter, r *http.Request) error {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	base := s.baseFor(r)
	g := s.guide(base, &invite{})
	fmt.Fprintf(w, "clrd agent invite on %s\nYour code is the part of your invite link after # (%s/agent#<code>). Single use, short-lived.\n\n", base, base)
	fmt.Fprintf(w, "1. Claim it once, with no token:\n   curl -sS -X POST %s/api/v1/agents/claim -H 'Content-Type: application/json' -d '{\"code\":\"<code>\"}'\n", base)
	fmt.Fprintf(w, "   The answer holds your token (shown only once: keep it) and a guide as JSON, with your topics in guide.grants.\n\n")
	fmt.Fprintf(w, "2. Send with Authorization: Bearer <token>:\n   POST %s  {\"topic\":\"<one of your topics>\",\"title\":\"...\",\"message\":\"...\"}\n", g.Publish)
	fmt.Fprintf(w, "   Ask a question: add \"actions\":[{\"label\":\"Yes\",\"reply\":\"yes\"},{\"label\":\"No\",\"reply\":\"no\"}]\n")
	fmt.Fprintf(w, "   Wait for the answer: GET %s until \"reply\" is set, or follow %s\n\n", g.Notification, g.Events)
	fmt.Fprintf(w, "3. MCP: %s with the same token (send, wait_for_reply, request_approve).\n\n", g.MCP)
	fmt.Fprintf(w, "Approvals: you may not answer approval requests, and never your own. To ask a person: POST %s\n", g.ApproveRequest)
	fmt.Fprintf(w, "Limits: %s\n", g.Limits.text())
	fmt.Fprintf(w, "A claim answers 410 when the invite expired or was used: ask the person who sent it for a new one.\n")
	return nil
}

// agentGuide is what an agent needs to start, in machine-readable form.
type agentGuide struct {
	Server         string      `json:"server"`
	Grants         []Grant     `json:"grants"`
	Publish        string      `json:"publish"`      // POST a notification, actions for a question
	Notification   string      `json:"notification"` // GET one: reply is set once answered
	Events         string      `json:"events"`       // server-sent events, every change
	MCP            string      `json:"mcp"`
	ApproveRequest string      `json:"approve_request"`
	Approve        string      `json:"approve"`
	Limits         guideLimits `json:"limits"`
}

type guideLimits struct {
	PublishPerMinute   int   `json:"publish_per_minute"` // 0 is no limit
	AttachmentMaxBytes int64 `json:"attachment_max_bytes"`
}

func (l guideLimits) text() string {
	rate := "no publish limit"
	if l.PublishPerMinute > 0 {
		rate = fmt.Sprintf("%d publishes a minute", l.PublishPerMinute)
	}
	return fmt.Sprintf("%s, attachments up to %d MB.", rate, l.AttachmentMaxBytes>>20)
}

func (s *Server) guide(base string, iv *invite) agentGuide {
	return agentGuide{
		Server: base, Grants: iv.grants,
		Publish:        base + "/api/v1/notifications",
		Notification:   base + "/api/v1/notifications/{id}",
		Events:         base + "/api/v1/events/sse",
		MCP:            base + "/mcp",
		ApproveRequest: base + "/api/v1/devices/approve-request",
		Approve:        ApproveDenied,
		Limits:         guideLimits{PublishPerMinute: max(s.opt.RateToken, 0), AttachmentMaxBytes: s.opt.AttachmentMax},
	}
}
