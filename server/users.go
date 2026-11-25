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

// Merged is the reply to a merge: the person kept, how many tokens moved,
// and every token whose topic rules changed, wider or narrower (#143).
type Merged struct {
	User          User          `json:"user"`
	Moved         int           `json:"moved_tokens"`
	AccessChanged []MergedToken `json:"access_changed"`
}

// MergedToken is a token whose topic rules a merge changed.
type MergedToken struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	User string `json:"from_user"` // the person it belonged to before
}

// mergeUsers moves every token of person from under person into, gives into
// the grants of both, moves from's subscriptions and deletes from, in one
// transaction (#143). Only an admin signed in on the web or the app may,
// never a typed-in token: a leaked admin token cannot fold someone's phone
// into another person. No token's approve setting is touched.
func (s *Server) mergeUsers(ctx context.Context, tok *Token, into, from string) (Merged, error) {
	var out Merged
	if err := tok.needFullAdmin(); err != nil {
		return out, err
	}
	var session bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE token_id = ?)`, tok.ID).Scan(&session); err != nil {
		return out, err
	}
	if !session {
		return out, errf(403, "forbidden", "merging people needs an admin signed in on the web or the app, not a token")
	}
	if from == "" || from == into {
		return out, errf(400, "bad_request", "from required: the id of another person")
	}
	err := s.changeSubs(ctx, "u:"+into, nil, true, "", func(tx *sql.Tx, _ bool) error {
		grants := map[string]string{}
		for _, id := range []string{into, from} {
			var g, kind string
			var config int
			err := tx.QueryRowContext(ctx, `SELECT grants, kind, config FROM people WHERE id = ?`, id).Scan(&g, &kind, &config)
			if err == sql.ErrNoRows {
				return errf(404, "not_found", "no person %q", id)
			}
			if err != nil {
				return err
			}
			if kind != "person" {
				return errf(409, "conflict", "%q is an agent: only people merge", id)
			}
			if config != 0 {
				return errf(409, "conflict", "%q is managed by the config file: merge them there", id)
			}
			grants[id] = g
		}
		merged, err := mergeGrants(grants[into], grants[from])
		if err != nil {
			return err
		}
		// Each token of either side whose rules change is listed. An admin
		// token keeps its own access (withUser), so it never changes.
		rows, err := tx.QueryContext(ctx, `SELECT id, name, user_id FROM tokens WHERE user_id IN (?, ?)
		  AND ',' || scopes || ',' NOT LIKE '%,admin,%' ORDER BY created_at, id`, into, from)
		if err != nil {
			return err
		}
		out.AccessChanged = []MergedToken{}
		for rows.Next() {
			var m MergedToken
			if err := rows.Scan(&m.ID, &m.Name, &m.User); err != nil {
				rows.Close()
				return err
			}
			if grants[m.User] != merged {
				out.AccessChanged = append(out.AccessChanged, m)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE tokens SET user_id = ? WHERE user_id = ?`, into, from)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		out.Moved = int(n)
		// into's own choice wins where both chose for one topic.
		for _, q := range []string{
			`INSERT INTO subscriptions (owner, topic, subscribed) SELECT ?, topic, subscribed FROM subscriptions WHERE owner = ? ON CONFLICT DO NOTHING`,
			`DELETE FROM subscriptions WHERE owner = ?2`,
			`DELETE FROM sub_owners WHERE owner = ?2`,
		} {
			if _, err := tx.ExecContext(ctx, q, "u:"+into, "u:"+from); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM people WHERE id = ?`, from); err != nil {
			return err
		}
		var created int64
		out.User = User{ID: into}
		err = tx.QueryRowContext(ctx, `UPDATE people SET grants = ? WHERE id = ? RETURNING name, kind, created_at`, merged, into).Scan(&out.User.Name, &out.User.Kind, &created)
		out.User.CreatedAt = fromMS(created)
		if merged != "" {
			out.User.Grants = &[]Grant{}
			json.Unmarshal([]byte(merged), out.User.Grants)
		}
		return err
	})
	if err != nil {
		return Merged{}, err
	}
	s.dropStreams(context.WithoutCancel(ctx), `user_id = ?`, into)
	return out, nil
}

// mergeGrants is two people's stored grants as one: null on either side
// limits nothing, so the result does not either; otherwise into's rules come
// first, then from's not already there. Rules match in order, so where both
// name a topic, into's rule decides. Either side's devices may end up with
// more or fewer topics; mergeUsers lists them.
func mergeGrants(into, from string) (string, error) {
	if into == "" || from == "" {
		return "", nil
	}
	var a, b []Grant
	if err := json.Unmarshal([]byte(into), &a); err != nil {
		return "", err
	}
	if err := json.Unmarshal([]byte(from), &b); err != nil {
		return "", err
	}
	for _, g := range b {
		if !slices.ContainsFunc(a, func(h Grant) bool { return h.Topic == g.Topic && slices.Equal(h.Scopes, g.Scopes) }) {
			a = append(a, g)
		}
	}
	raw, _ := json.Marshal(a)
	stored, _, err := userGrants(raw)
	return stored, err
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
	s.api("POST /api/v1/users/{id}/merge", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var in struct {
			From string `json:"from"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		out, err := s.mergeUsers(r.Context(), tok, r.PathValue("id"), in.From)
		if err != nil {
			return err
		}
		writeJSON(w, 200, out)
		return nil
	})
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
