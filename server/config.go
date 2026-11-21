package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	Name   string          `json:"name"`
	Grants json.RawMessage `json:"grants"` // as on the API: a list, [] for no topic, null or left out for no limit
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
// removed; rows made on the API and not named are left alone.
func (s *Server) applyConfig(ctx context.Context) error {
	if s.opt.Config == "" {
		return nil
	}
	f, err := os.Open(s.opt.Config)
	if err != nil {
		return fmt.Errorf("CLRD_CONFIG: %w", err)
	}
	defer f.Close()
	var c Config
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields() // a typo must not quietly drop a limit
	if err := dec.Decode(&c); err != nil {
		return fmt.Errorf("CLRD_CONFIG %s: %w", s.opt.Config, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	gone, err := applyConfigTx(ctx, tx, &c)
	if err != nil {
		return fmt.Errorf("CLRD_CONFIG %s: %w", s.opt.Config, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.revokeAll(gone)
	s.log.Info("config applied", "file", s.opt.Config, "users", len(c.Users), "tokens", len(c.Tokens), "removed_tokens", len(gone))
	return nil
}

func applyConfigTx(ctx context.Context, tx *sql.Tx, c *Config) (gone []string, err error) {
	users := map[string]string{} // name to id
	for _, u := range c.Users {
		name := strings.TrimSpace(u.Name)
		if name == "" || len(name) > 64 {
			return nil, fmt.Errorf("user %q: name required, at most 64 characters", u.Name)
		}
		if _, dup := users[name]; dup {
			return nil, fmt.Errorf("user %q listed twice", name)
		}
		if len(u.Grants) == 0 {
			u.Grants = json.RawMessage("null")
		}
		stored, _, err := userGrants(u.Grants)
		if err != nil {
			return nil, fmt.Errorf("user %q: %w", name, err)
		}
		id := randomString(12)
		if err := tx.QueryRowContext(ctx, `INSERT INTO people (id, name, grants, created_at, config) VALUES (?,?,?,?,1)
		  ON CONFLICT (name) DO UPDATE SET grants = excluded.grants, config = 1 RETURNING id`, id, name, stored, ms(now())).Scan(&id); err != nil {
			return nil, err
		}
		users[name] = id
	}
	hashes := []any{}
	for _, t := range c.Tokens {
		secret := t.Secret
		if v, ok := strings.CutPrefix(secret, "$"); ok {
			secret = os.Getenv(v)
		}
		if len(secret) < 20 {
			return nil, fmt.Errorf("token %q: secret must be at least 20 characters (or a $VARIABLE that holds one)", t.Name)
		}
		tok := Token{Name: t.Name, Scopes: t.Scopes, Topics: t.Topics, Grants: t.Grants, Approve: t.Approve, User: users[t.User]}
		if t.User != "" && tok.User == "" {
			return nil, fmt.Errorf("token %q: user %q is not in users", t.Name, t.User)
		}
		if tok.Approve == "" {
			tok.Approve = ApproveDenied
		}
		if err := checkToken(&tok); err != nil {
			return nil, fmt.Errorf("token %q: %w", t.Name, err)
		}
		hash := hashToken(secret)
		if slices.Contains(hashes, any(hash)) {
			return nil, fmt.Errorf("token %q: secret used twice", t.Name)
		}
		hashes = append(hashes, hash)
		grants := ""
		if len(tok.Grants) > 0 {
			g, _ := json.Marshal(tok.Grants)
			grants = string(g)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tokens (id, name, hash, scopes, topics, grants, created_at, approve, user_id, config)
		  VALUES (?,?,?,?,?,?,?,?,NULLIF(?, ''),1) ON CONFLICT (hash) DO UPDATE SET name = excluded.name, scopes = excluded.scopes,
		  topics = excluded.topics, grants = excluded.grants, approve = excluded.approve, user_id = excluded.user_id, config = 1`,
			randomString(12), tok.Name, hash, strings.Join(tok.Scopes, ","), strings.Join(tok.Topics, ","), grants, ms(now()), tok.Approve, tok.User); err != nil {
			return nil, err
		}
	}
	// Tokens the file made and no longer lists go, with the browsers signed in with them.
	marks := strings.TrimSuffix(strings.Repeat("?,", len(hashes)), ",")
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE k(id) AS
	  (SELECT id FROM tokens WHERE config = 1 AND hash NOT IN (`+marks+`) UNION SELECT token_id FROM sessions JOIN k ON parent_id = k.id)
	  DELETE FROM tokens WHERE id IN k RETURNING id`, hashes...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		gone = append(gone, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := []any{}
	for n := range users {
		names = append(names, n)
	}
	marks = strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	if _, err := tx.ExecContext(ctx, `DELETE FROM people WHERE config = 1 AND name NOT IN (`+marks+`)`, names...); err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return nil, fmt.Errorf("a user dropped from the file still has tokens made on the API: detach them first")
		}
		return nil, err
	}
	return gone, nil
}
