package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Users (issue #143, spec section 4): a user is one person and owns typed-in
// tokens (tokens.user_id); every browser or phone made from one belongs to
// the same user. A user's grants, when set, replace its devices' own topics
// and grants at every request.

// User is one person. Grants nil limits nothing.
type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Grants    *[]Grant  `json:"grants"`
	Kind      string    `json:"kind"` // person, or agent made from an invite (#454)
	CreatedAt time.Time `json:"created_at"`
	// LastSeenAt is the newest of its typed-in tokens, nil for never.
	LastSeenAt *time.Time `json:"last_seen_at"`
}

// withUser applies the user of t's top token to t. A token holding admin
// keeps its own access; an error means refuse the token, never let it by.
func (s *Server) withUser(ctx context.Context, t *Token) error {
	var user, grants sql.NullString
	err := s.db.QueryRowContext(ctx, `WITH RECURSIVE up(id, n) AS
	  (SELECT ?, 0 UNION SELECT parent_id, n + 1 FROM sessions JOIN up ON token_id = up.id)
	  SELECT t.user_id, p.grants FROM up JOIN tokens t ON t.id = up.id LEFT JOIN people p ON p.id = t.user_id
	  ORDER BY n DESC LIMIT 1`, t.ID).Scan(&user, &grants)
	if err != nil || !user.Valid {
		return err
	}
	t.User = user.String
	if !grants.Valid {
		return errf(401, "unauthorized", "user %s not found", user.String)
	}
	if grants.String == "" || slices.Contains(t.Scopes, ScopeAdmin) {
		return nil
	}
	var gs []Grant
	if err := json.Unmarshal([]byte(grants.String), &gs); err != nil {
		return err
	}
	if len(gs) == 0 { // no grant is no topic, not every topic
		gs = []Grant{{Topic: "*", Scopes: []string{}}}
	}
	t.Topics, t.Grants = nil, gs // may() keeps t.Scopes as the ceiling
	return nil
}

// topsSQL is a WITH RECURSIVE part that makes tops: the top tokens owning
// device ? (its own top token, and every top token of that one's user), so
// all of one person's devices count as "mine" (#143).
const topsSQL = `up(id, n) AS (SELECT ?, 0 UNION SELECT parent_id, n + 1 FROM sessions JOIN up ON token_id = up.id),
  top(id) AS (SELECT id FROM up ORDER BY n DESC LIMIT 1),
  tops(id) AS (SELECT id FROM top UNION SELECT t.id FROM tokens t JOIN tokens o ON t.user_id = o.user_id JOIN top ON o.id = top.id)`

// topsIn is the tops of topsSQL for device id.
func topsIn(ctx context.Context, q querier, id string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE `+topsSQL+` SELECT id FROM tops`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var top string
		if err := rows.Scan(&top); err != nil {
			return nil, err
		}
		out[top] = true
	}
	return out, rows.Err()
}

// dropStreams closes the streams of the tokens matching where and every
// device under them, so they reconnect under the new rules (as anonymous
// access does, #375). They are not revoked: they may reconnect at once.
func (s *Server) dropStreams(ctx context.Context, where string, arg any) {
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE k(id) AS
	  (SELECT id FROM tokens WHERE `+where+` UNION SELECT token_id FROM sessions JOIN k ON parent_id = k.id) SELECT id FROM k`, arg)
	if err != nil {
		s.log.Warn("drop streams", "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		s.hub.drop(id)
	}
}

// userGrants checks a user's grants as sent: a list, or null for no limit.
// It returns them as stored ("" for null) and as shown.
func userGrants(raw json.RawMessage) (stored string, gs *[]Grant, err error) {
	if len(raw) == 0 {
		return "", nil, errf(400, "bad_request", "grants required: a list of rules, [] for no topic, or null for no limit")
	}
	if string(raw) == "null" {
		return "", nil, nil
	}
	var list []Grant
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", nil, errf(400, "bad_request", "grants: %v", err)
	}
	if err := checkGrants(list, []string{ScopeRead, ScopePublish}); err != nil {
		return "", nil, err
	}
	b, _ := json.Marshal(list)
	return string(b), &list, nil
}

func (s *Server) userRoutes() {
	admin := func(h func(w http.ResponseWriter, r *http.Request) error) func(http.ResponseWriter, *http.Request, *Token) error {
		return func(w http.ResponseWriter, r *http.Request, tok *Token) error {
			if err := tok.needFullAdmin(); err != nil {
				return err
			}
			return h(w, r)
		}
	}
	s.api("GET /api/v1/users", admin(func(w http.ResponseWriter, r *http.Request) error {
		rows, err := s.db.QueryContext(r.Context(), `SELECT id, name, grants, created_at, kind,
		  (SELECT MAX(last_seen_at) FROM tokens WHERE user_id = people.id) FROM people ORDER BY created_at, rowid`)
		if err != nil {
			return err
		}
		defer rows.Close()
		out := []User{}
		for rows.Next() {
			var u User
			var grants string
			var created int64
			var seen sql.NullInt64
			if err := rows.Scan(&u.ID, &u.Name, &grants, &created, &u.Kind, &seen); err != nil {
				return err
			}
			if seen.Valid {
				v := fromMS(seen.Int64)
				u.LastSeenAt = &v
			}
			if grants != "" {
				u.Grants = &[]Grant{}
				json.Unmarshal([]byte(grants), u.Grants)
			}
			u.CreatedAt = fromMS(created)
			out = append(out, u)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"users": out})
		return nil
	}))
	s.api("POST /api/v1/users", admin(func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			Name   string          `json:"name"`
			Grants json.RawMessage `json:"grants"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		u := User{ID: randomString(12), Name: strings.TrimSpace(in.Name), Kind: "person", CreatedAt: now()}
		if u.Name == "" || len(u.Name) > 64 {
			return errf(400, "bad_request", "name required, at most 64 characters")
		}
		if len(in.Grants) == 0 {
			in.Grants = json.RawMessage("null") // a new user limits nothing unless told to
		}
		stored, gs, err := userGrants(in.Grants)
		if err != nil {
			return err
		}
		u.Grants = gs
		_, err = s.db.ExecContext(r.Context(), `INSERT INTO people (id, name, grants, created_at) VALUES (?,?,?,?)`, u.ID, u.Name, stored, ms(u.CreatedAt))
		if err != nil && strings.Contains(err.Error(), "UNIQUE") {
			return errf(409, "conflict", "a user named %q exists", u.Name)
		}
		if err != nil {
			return err
		}
		writeJSON(w, 200, u)
		return nil
	}))
	s.api("PUT /api/v1/users/{id}", admin(func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			Grants json.RawMessage `json:"grants"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		stored, gs, err := userGrants(in.Grants)
		if err != nil {
			return err
		}
		id := r.PathValue("id")
		var u User
		var created int64
		err = s.db.QueryRowContext(r.Context(), `UPDATE people SET grants = ? WHERE id = ? RETURNING id, name, kind, created_at`, stored, id).Scan(&u.ID, &u.Name, &u.Kind, &created)
		if err == sql.ErrNoRows {
			return errf(404, "not_found", "user not found")
		}
		if err != nil {
			return err
		}
		u.Grants, u.CreatedAt = gs, fromMS(created)
		s.dropStreams(context.WithoutCancel(r.Context()), `user_id = ?`, id)
		writeJSON(w, 200, u)
		return nil
	}))
	s.api("DELETE /api/v1/users/{id}", admin(func(w http.ResponseWriter, r *http.Request) error {
		res, err := s.db.ExecContext(r.Context(), `DELETE FROM people WHERE id = ?`, r.PathValue("id"))
		if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
			return errf(409, "conflict", "the user still has tokens: detach them first")
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errf(404, "not_found", "user not found")
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
	s.api("PUT /api/v1/tokens/{id}/user", admin(func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			User *string `json:"user"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.User == nil {
			return errf(400, "bad_request", `user required: a user id, or "" to detach`)
		}
		id := r.PathValue("id")
		// Its devices now follow the new owner's set: a change of that set, in
		// the same transaction, so each catches up with a reset (#617). The old
		// owner's rows stay.
		owner := "t:" + id
		if *in.User != "" {
			owner = "u:" + *in.User
		}
		err := s.changeSubs(r.Context(), owner, nil, true, "", func(tx *sql.Tx, _ bool) error {
			// Only a typed-in token: a browser or phone follows the token it came from.
			res, err := tx.ExecContext(r.Context(), `UPDATE tokens SET user_id = NULLIF(?, '') WHERE id = ?
			  AND id NOT IN (SELECT token_id FROM sessions) AND id NOT LIKE 'pushover-%' AND id NOT IN (SELECT token_id FROM gotify_apps)
			  AND NOT `+anonymousWhere, *in.User, id)
			if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
				return errf(400, "bad_request", "no user %q", *in.User)
			}
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errf(404, "not_found", "no such typed-in token (a browser or phone follows the token it came from, and a Gotify or Pushover app keeps its one topic)")
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.dropStreams(context.WithoutCancel(r.Context()), `id = ?`, id)
		writeJSON(w, 200, map[string]string{"id": id, "user": *in.User})
		return nil
	}))
}
