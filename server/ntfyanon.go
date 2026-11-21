package server

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
)

// ntfyAnonymousRoutes: an admin turns anonymous ntfy access on, changes its
// topics or turns it off (#375). It is the anonymous token's grants, so it
// is off until set, only ever read or publish, and only on ntfy routes.
func (s *Server) ntfyAnonymousRoutes() {
	get := func(w http.ResponseWriter, r *http.Request) error {
		t, err := scanToken(s.db.QueryRowContext(r.Context(), `SELECT `+tokenCols+` FROM tokens WHERE id = ?`, anonymousID))
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, 200, map[string]any{"enabled": false, "grants": []Grant{}})
			return nil
		} else if err != nil {
			return err
		}
		grants := t.Grants
		if grants == nil { // an old import without grants: every topic, its scopes
			grants = []Grant{{Topic: "*", Scopes: t.Scopes}}
		}
		writeJSON(w, 200, map[string]any{"enabled": true, "grants": grants})
		return nil
	}
	s.api("GET /api/v1/ntfy/anonymous", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		return get(w, r)
	})
	s.api("PUT /api/v1/ntfy/anonymous", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		var in struct {
			Grants []Grant `json:"grants"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		var scopes []string
		for _, g := range in.Grants {
			for _, sc := range g.Scopes {
				if !slices.Contains(scopes, sc) {
					scopes = append(scopes, sc)
				}
			}
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM tokens WHERE id = ?`, anonymousID); err != nil {
			return err
		}
		if len(scopes) > 0 { // no grant with a scope is no access: off
			t := &Token{ID: anonymousID, Name: "anonymous", Scopes: scopes, Grants: in.Grants, CreatedAt: now()}
			if err := insertToken(r.Context(), tx, t, hashToken(tokenPrefix+randomString(32))); err != nil {
				return err // checkToken: read or publish only, never admin
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.revoked.Delete(anonymousID) // a past revoke must not close the new one's streams
		s.hub.drop(anonymousID)       // open anonymous streams reconnect under the new rules
		return get(w, r)
	})
}
