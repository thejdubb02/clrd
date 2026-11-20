package server

import (
	"context"
	"net/http"

	"golang.org/x/crypto/bcrypt"
)

// anonymousID is the token ntfy routes use when a request sends no
// credentials at all (spec section 4). The import makes it; revoke it to
// turn anonymous access off.
const anonymousID = "anonymous"

// bcryptSlots caps concurrent password checks, so a flood of Basic
// requests cannot pin every CPU. ponytail: no per-IP limit yet.
var bcryptSlots = make(chan struct{}, 4)

// dummyHash is compared against when the user does not exist, so an
// unknown name takes as long as a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("clrd-no-such-user"), bcrypt.DefaultCost)

// ntfyAuth is auth for ntfy-compatible routes only: a token as on every
// route, else an imported ntfy password (Basic user:password), else the
// anonymous token when no credentials were sent. The native API never
// calls this, so it stays token-only.
func (s *Server) ntfyAuth(r *http.Request) *Token {
	if t := s.auth(r, false); t != nil {
		return t
	}
	ctx := r.Context()
	h := authHeader(r)
	if h == "" {
		t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE id = ?`, anonymousID))
		if err != nil {
			return nil
		}
		return t
	}
	user, pass, ok := (&http.Request{Header: http.Header{"Authorization": {h}}}).BasicAuth()
	if !ok || pass == "" || isToken(pass) {
		return nil // a wrong credential is never anonymous
	}
	return s.passwordUser(ctx, user, pass)
}

func (s *Server) passwordUser(ctx context.Context, user, pass string) *Token {
	var hash, id string
	if err := s.db.QueryRowContext(ctx, `SELECT hash, token_id FROM users WHERE name = ?`, user).Scan(&hash, &id); err != nil {
		hash, id = string(dummyHash), ""
	}
	select {
	case bcryptSlots <- struct{}{}:
	case <-ctx.Done():
		return nil
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass))
	<-bcryptSlots
	if err != nil || id == "" {
		return nil
	}
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE id = ?`, id))
	if err != nil {
		return nil
	}
	s.touch(ctx, t)
	return t
}
