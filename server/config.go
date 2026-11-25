package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// Config is the file CLRD_CONFIG names (#155): users and tokens declared
// once, so a server can be rebuilt from it. Read at every start.
type Config struct {
	Users  []ConfigUser  `json:"users"`
	Tokens []ConfigToken `json:"tokens"`
}

type ConfigUser struct {
	Name   string   `json:"name"`
	Grants *[]Grant `json:"grants"` // as on the API: a list, [] for no topic, null or left out for no limit
}

type ConfigToken struct {
	Name    string   `json:"name"`
	Secret  string   `json:"secret"` // the value devices send; "$NAME" reads the environment variable NAME
	Scopes  []string `json:"scopes"`
	Topics  []string `json:"topics"`
	Grants  []Grant  `json:"grants"`
	Approve string   `json:"approve"`
	User    string   `json:"user"` // a user name from users
}

// applyConfig makes the users and tokens in the file match it. A token is
// known by its secret, so one already made on the API is taken over when the
// file names its secret. Rows the file made before and no longer lists are
// removed; rows made on the API are never removed, and a dropped user a
// token made on the API still points at is kept (#440).
func (s *Server) applyConfig(ctx context.Context) error {
	if s.opt.Config == "" {
		return nil
	}
	f, err := os.Open(s.opt.Config)
	if err != nil {
		return fmt.Errorf("CLRD_CONFIG: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields() // a typo must not quietly drop a limit
	var p *Config
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("CLRD_CONFIG %s: %w", s.opt.Config, err)
	}
	var rest json.RawMessage
	if p == nil || dec.Decode(&rest) != io.EOF {
		return fmt.Errorf("CLRD_CONFIG %s: one JSON object expected", s.opt.Config)
	}
	c := *p
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	gone, kept, err := applyConfigTx(ctx, tx, &c)
	if err != nil {
		return fmt.Errorf("CLRD_CONFIG %s: %w", s.opt.Config, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.revokeAll(gone)
	for _, n := range kept {
		s.log.Warn("config: a user dropped from the file is kept, made on the API or holding its tokens; delete it there if it should go", "user", n)
	}
	s.log.Info("config applied", "file", s.opt.Config, "users", len(c.Users), "tokens", len(c.Tokens), "removed_tokens", len(gone))
	return nil
}

func applyConfigTx(ctx context.Context, tx *sql.Tx, c *Config) (gone, kept []string, err error) {
	users := map[string]string{} // name to id
	for _, u := range c.Users {
		name := strings.TrimSpace(u.Name)
		if name == "" || len(name) > 64 {
			return nil, nil, fmt.Errorf("user %q: name required, at most 64 characters", u.Name)
		}
		if _, dup := users[name]; dup {
			return nil, nil, fmt.Errorf("user %q listed twice", name)
		}
		raw, _ := json.Marshal(u.Grants) // decoded strictly above, so a typo in a grant stops the start
		stored, _, err := userGrants(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("user %q: %w", name, err)
		}
		id := randomString(12)
		// Never an agent's row: taking it over would give the agent the file's grants.
		// A person made on the API is managed by the file (config 2) but never
		// deleted by it (#440); one the file made is config 1.
		err = tx.QueryRowContext(ctx, `INSERT INTO people (id, name, grants, created_at, config) VALUES (?,?,?,?,1)
		  ON CONFLICT (name) DO UPDATE SET grants = excluded.grants, config = CASE config WHEN 0 THEN 2 ELSE config END
		  WHERE kind != 'agent' RETURNING id`, id, name, stored, ms(now())).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, fmt.Errorf("user %q: an agent has this name; rename the user in the file", name)
		}
		if err != nil {
			return nil, nil, err
		}
		users[name] = id
	}
	hashes := []any{}
	for _, t := range c.Tokens {
		secret := t.Secret
		if v, ok := strings.CutPrefix(secret, "$"); ok {
			secret = os.Getenv(v)
		}
		if len(secret) < 20 || !isToken(secret) {
			return nil, nil, fmt.Errorf("token %q: secret must start with %s and be at least 20 characters (or a $VARIABLE that holds one)", t.Name, tokenPrefix)
		}
		tok := Token{Name: t.Name, Scopes: t.Scopes, Topics: t.Topics, Grants: t.Grants, Approve: t.Approve, User: users[t.User]}
		if t.User != "" && tok.User == "" {
			return nil, nil, fmt.Errorf("token %q: user %q is not in users", t.Name, t.User)
		}
		if tok.Approve == "" {
			tok.Approve = ApproveDenied
		}
		if err := checkToken(&tok); err != nil {
			return nil, nil, fmt.Errorf("token %q: %w", t.Name, err)
		}
		hash := hashToken(secret)
		if slices.Contains(hashes, any(hash)) {
			return nil, nil, fmt.Errorf("token %q: secret used twice", t.Name)
		}
		hashes = append(hashes, hash)
		grants := ""
		if len(tok.Grants) > 0 {
			g, _ := json.Marshal(tok.Grants)
			grants = string(g)
		}
		// Browsers signed in with a token copy its access, so a change here signs them out.
		var id, was string
		const access = `scopes || '|' || topics || '|' || grants || '|' || approve || '|' || IFNULL(user_id, '')`
		if err := tx.QueryRowContext(ctx, `SELECT id, `+access+` FROM tokens WHERE hash = ?`, hash).Scan(&id, &was); err != nil && err != sql.ErrNoRows {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tokens (id, name, hash, scopes, topics, grants, created_at, approve, user_id, config)
		  VALUES (?,?,?,?,?,?,?,?,NULLIF(?, ''),1) ON CONFLICT (hash) DO UPDATE SET name = excluded.name, scopes = excluded.scopes,
		  topics = excluded.topics, grants = excluded.grants, approve = excluded.approve, user_id = excluded.user_id, config = 1`,
			randomString(12), tok.Name, hash, strings.Join(tok.Scopes, ","), strings.Join(tok.Topics, ","), grants, ms(now()), tok.Approve, tok.User); err != nil {
			return nil, nil, err
		}
		var is string
		tx.QueryRowContext(ctx, `SELECT `+access+` FROM tokens WHERE hash = ?`, hash).Scan(&is)
		if id != "" && is != was {
			ids, err := dropTokens(ctx, tx, `SELECT token_id FROM sessions WHERE parent_id = ?`, id)
			if err != nil {
				return nil, nil, err
			}
			gone = append(gone, ids...)
		}
	}
	// Tokens the file made and no longer lists go, with the browsers signed in with them.
	marks := strings.TrimSuffix(strings.Repeat("?,", len(hashes)), ",")
	ids, err := dropTokens(ctx, tx, `SELECT id FROM tokens WHERE config = 1 AND hash NOT IN (`+marks+`)`, hashes...)
	if err != nil {
		return nil, nil, err
	}
	gone = append(gone, ids...)
	names := []any{}
	for n := range users {
		names = append(names, n)
	}
	marks = strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	// A dropped user goes back to the API, as it was, when the API made it or
	// a token made there still points at it (#440); the rest are deleted.
	rows, err := tx.QueryContext(ctx, `UPDATE people SET config = 0 WHERE config != 0 AND kind != 'agent' AND name NOT IN (`+marks+`)
	  AND (config = 2 OR id IN (SELECT user_id FROM tokens WHERE user_id IS NOT NULL)) RETURNING name`, names...)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		kept = append(kept, n)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM people WHERE config = 1 AND kind != 'agent' AND name NOT IN (`+marks+`)`, names...); err != nil {
		return nil, nil, err
	}
	return gone, kept, nil
}

// dropTokens deletes the tokens seed selects and every browser signed in
// with them, at any depth, and returns their ids.
func dropTokens(ctx context.Context, tx *sql.Tx, seed string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE k(id) AS (`+seed+` UNION SELECT token_id FROM sessions JOIN k ON parent_id = k.id)
	  DELETE FROM tokens WHERE id IN k RETURNING id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	_, err = tx.ExecContext(ctx, `DELETE FROM hidden WHERE token_id NOT IN (SELECT id FROM tokens)`) // what they cleared for themselves (#767)
	return ids, err
}
