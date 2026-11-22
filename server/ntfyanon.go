package server

import (
	"context"
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
		t, err := s.anonymousToken(r.Context())
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
		writeJSON(w, 200, map[string]any{"enabled": true, "grants": grants, "token_id": t.ID})
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
			Grants *[]Grant `json:"grants"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.Grants == nil { // a typo or {} must not wipe the rules
			return errf(400, "bad_request", "grants required: a list of rules, or [] to turn anonymous access off")
		}
		var scopes []string
		for _, g := range *in.Grants {
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
		gone, err := dropAnonymous(r.Context(), tx)
		if err != nil {
			return err
		}
		if len(scopes) > 0 { // no grant with a scope is no access: off
			// A new id each time, so the old one's streams are revoked like
			// any token's, even one that read the old grants a moment ago.
			t := &Token{ID: anonymousID + "-" + randomString(12), Name: "anonymous", Scopes: scopes, Grants: *in.Grants, CreatedAt: now()}
			if err := insertToken(r.Context(), tx, t, hashToken(tokenPrefix+randomString(32))); err != nil {
				return err // checkToken: read or publish only, never admin
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.revokeAll(gone)
		return get(w, r)
	})
}

// dropAnonymous deletes every anonymous token row, old ids included, and
// returns their ids for revokeAll once the transaction commits.
func dropAnonymous(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM tokens WHERE `+anonymousWhere+` RETURNING id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gone []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		gone = append(gone, id)
	}
	return gone, rows.Err()
}

func (s *Server) revokeAll(ids []string) {
	for _, id := range ids {
		s.revoked.Store(id, true) // streams check this before every send
		s.hub.drop(id)            // and wake on this, so an idle stream closes now
	}
}
