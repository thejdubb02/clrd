package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Which other web sites may use the ntfy and Gotify routes from a browser
// (#374). Empty by default: phones, scripts and clrd's own pages never send
// a foreign Origin, so they work; a page on another site is refused, its
// WebSocket included. An admin adds an origin (say https://ntfy.sh for
// ntfy's hosted web app) or "*" for any site, as ntfy itself answers.

// ntfyOriginList is the stored allow list, read once and kept until set.
func (s *Server) ntfyOriginList(ctx context.Context) ([]string, error) {
	if p := s.origins.Load(); p != nil {
		return *p, nil
	}
	var raw string
	list := []string{}
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'ntfy_origins'`).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &list)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	// A first read racing an admin's save must not overwrite the new list.
	if !s.origins.CompareAndSwap(nil, &list) {
		return *s.origins.Load(), nil
	}
	return list, nil
}

// ntfyCORS checks a browser request from another site against the allow
// list and sets the CORS reply headers when it passes. A request with no
// Origin, or from clrd's own pages, passes untouched.
func (s *Server) ntfyCORS(w http.ResponseWriter, r *http.Request) error {
	o := r.Header.Get("Origin")
	// Every reply depends on Origin, so a cache must not share them.
	w.Header().Add("Vary", "Origin")
	if o == "" || s.sameOrigin(r) {
		return nil
	}
	list, err := s.ntfyOriginList(r.Context())
	if err != nil {
		return err
	}
	h := w.Header()
	switch {
	case slices.Contains(list, "*"):
		h.Set("Access-Control-Allow-Origin", "*")
	case slices.Contains(list, strings.ToLower(o)):
		h.Set("Access-Control-Allow-Origin", o)
	default:
		return errf(403, "forbidden", "requests from %s are not allowed: an admin can add it with PUT /api/v1/ntfy/origins", o)
	}
	h.Add("Access-Control-Expose-Headers", "Retry-After")
	return nil
}

// cleanOrigin turns "https://Ntfy.sh/" into "https://ntfy.sh", or fails for
// anything that is not "*" or a bare scheme://host[:port].
func cleanOrigin(o string) (string, error) {
	o = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(o)), "/")
	if o == "*" {
		return o, nil
	}
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return "", errf(400, "bad_request", "origin %q must be \"*\" or scheme://host[:port], like https://ntfy.sh", o)
	}
	return o, nil
}

func (s *Server) ntfyOriginRoutes() {
	get := func(w http.ResponseWriter, r *http.Request) error {
		list, err := s.ntfyOriginList(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"origins": list})
		return nil
	}
	s.api("GET /api/v1/ntfy/origins", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		return get(w, r)
	})
	s.api("PUT /api/v1/ntfy/origins", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		var in struct {
			Origins *[]string `json:"origins"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.Origins == nil { // a typo or {} must not change the list
			return errf(400, "bad_request", "origins required: a list, or [] for clrd's own pages only")
		}
		list := []string{}
		for _, o := range *in.Origins {
			c, err := cleanOrigin(o)
			if err != nil {
				return err
			}
			if !slices.Contains(list, c) {
				list = append(list, c)
			}
		}
		raw, _ := json.Marshal(list)
		s.mu.Lock() // two saves must store in the order they wrote
		_, err := s.db.ExecContext(r.Context(), `INSERT INTO settings (key, value) VALUES ('ntfy_origins', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(raw))
		if err == nil {
			s.origins.Store(&list)
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
		return get(w, r)
	})
}
