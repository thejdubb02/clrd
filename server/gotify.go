package server

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Gotify compatibility (spec section 5, issue #116): Gotify's publish call,
// POST /message with an app token, so senders built for Gotify (scripts,
// Sonarr, Apprise, shoutrrr, the gotify CLI) work by changing only the URL.
// A Gotify app is one clrd token limited to one topic; the token decides
// the topic, and the same scope checks as the native API apply.

// gotifyKey is the token a Gotify sender puts on the request: the
// X-Gotify-Key header, ?token= (which ntfy never uses), or a Bearer that is
// not a clrd or ntfy token.
func gotifyKey(r *http.Request) string {
	if k := r.Header.Get("X-Gotify-Key"); k != "" {
		return k
	}
	if k := r.URL.Query().Get("token"); k != "" {
		return k
	}
	return gotifyBearer(r)
}

func gotifyBearer(r *http.Request) string {
	scheme, k, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if k = strings.TrimSpace(k); !strings.EqualFold(scheme, "Bearer") || isToken(k) {
		return "" // parsed as rawToken does, so a clrd token stays on the ntfy path
	}
	return k
}

// gotifyMessage: POST /message. Without a Gotify key this is ntfy's publish
// to the topic "message", as before.
func (s *Server) gotifyMessage(w http.ResponseWriter, r *http.Request) error {
	// A key that is sent but empty is a wrong key, never the ntfy path.
	if _, hdr := r.Header["X-Gotify-Key"]; !hdr && !r.URL.Query().Has("token") && gotifyBearer(r) == "" {
		r.SetPathValue("topic", "message")
		return s.ntfyPublish(w, r)
	}
	if err := s.gotifyPublish(w, r, gotifyKey(r)); err != nil {
		status, _, msg := s.failStatus(w, err)
		// Gotify's error shape, so its clients show the reason.
		writeJSON(w, status, map[string]any{"error": http.StatusText(status), "errorCode": status, "errorDescription": msg})
	}
	return nil
}

type gotifyIn struct {
	Title    string         `json:"title"`
	Message  string         `json:"message"`
	Priority *int           `json:"priority"`
	Extras   map[string]any `json:"extras"`
}

func (s *Server) gotifyPublish(w http.ResponseWriter, r *http.Request, key string) error {
	tok, app := s.lookupToken(r.Context(), key), gotifyApp{}
	if tok == nil && !isToken(key) {
		tok, app = s.lookupGotifyApp(r.Context(), key)
	}
	if tok == nil {
		return errf(401, "unauthorized", "you need to provide a valid access token or user credentials to access this api")
	}
	topic, err := gotifyTopic(tok)
	if err != nil {
		return err
	}
	var in gotifyIn
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
	} else { // form or multipart, like Gotify's gin binding
		if err := r.ParseMultipartForm(maxBody); err != nil && err != http.ErrNotMultipart {
			return errf(400, "bad_request", "bad form body: %v", err)
		}
		in.Title, in.Message = r.FormValue("title"), r.FormValue("message")
		if p := r.FormValue("priority"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return errf(400, "bad_request", "priority must be a number")
			}
			in.Priority = &n
		}
	}
	if in.Message == "" {
		return errf(400, "bad_request", "Field 'message' is required")
	}
	pub := PublishInput{Topic: topic, Title: in.Title, Message: in.Message, Markdown: new(bool)}
	if strings.TrimSpace(pub.Title) == "" {
		pub.Title = tok.Name // Gotify uses the app name
	}
	if in.Priority == nil {
		in.Priority = app.priority // the app's default, as Gotify does
	}
	if in.Priority != nil {
		pub.Priority = gotifyPriority(*in.Priority)
	}
	if extra(in.Extras, "client::display", "contentType") == "text/markdown" {
		*pub.Markdown = true // Gotify's default is plain text
	}
	note, _ := in.Extras["client::notification"].(map[string]any)
	if click, _ := note["click"].(map[string]any); click != nil {
		pub.Click, _ = click["url"].(string)
	}
	if img := ntfyIcon(extra(in.Extras, "client::notification", "bigImageUrl")); img != "" {
		pub.Attachment = &Attachment{URL: img}
	}
	n, err := s.Publish(r.Context(), tok, pub)
	if err != nil {
		return err
	}
	out := map[string]any{"id": n.Seq, "appid": app.id, "message": in.Message, "title": pub.Title, "date": n.UpdatedAt.Format(time.RFC3339Nano), "priority": 0}
	if in.Priority != nil {
		out["priority"] = *in.Priority
	}
	if in.Extras != nil {
		out["extras"] = in.Extras
	}
	writeJSON(w, 200, out)
	return nil
}

type gotifyApp struct {
	id       int64
	priority *int
}

// lookupGotifyApp finds the token behind a Gotify-shaped key. Only POST
// /message calls it, so these keys work nowhere else.
func (s *Server) lookupGotifyApp(ctx context.Context, key string) (*Token, gotifyApp) {
	var app gotifyApp
	var id string
	if key == "" || s.db.QueryRowContext(ctx, `SELECT g.id, g.priority, g.token_id FROM gotify_apps g JOIN tokens t ON t.id = g.token_id WHERE t.hash = ?`,
		hashToken(key)).Scan(&app.id, &app.priority, &id) != nil {
		return nil, gotifyApp{}
	}
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE id = ?`, id))
	if err != nil {
		return nil, gotifyApp{}
	}
	s.touch(ctx, t)
	return t, app
}

// gotifyAppRoutes: an admin makes a Gotify app, a publish-only token for one
// topic whose secret is shaped like Gotify's ("A" and 14 characters), the
// only shape shoutrrr and Watchtower accept.
func (s *Server) gotifyAppRoutes() {
	s.api("POST /api/v1/gotify/apps", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		var in struct {
			Name     string `json:"name"`
			Topic    string `json:"topic"`
			Priority *int   `json:"priority"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.Priority != nil && (*in.Priority < 0 || *in.Priority > 10) {
			return errf(400, "bad_request", "priority is Gotify's 0 to 10")
		}
		key := "A" + randomString(14)
		t := &Token{ID: randomString(12), Name: in.Name, Scopes: []string{ScopePublish}, Topics: []string{in.Topic}, CreatedAt: now()}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := insertToken(r.Context(), tx, t, hashToken(key)); err != nil {
			return err
		}
		res, err := tx.ExecContext(r.Context(), `INSERT INTO gotify_apps (token_id, priority) VALUES (?, ?)`, t.ID, in.Priority)
		if err != nil {
			return err
		}
		appid, _ := res.LastInsertId()
		if err := tx.Commit(); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"id": appid, "token": key, "name": t.Name, "topic": in.Topic, "token_id": t.ID, "defaultPriority": in.Priority})
		return nil
	})
}

func extra(extras map[string]any, ns, key string) string {
	m, _ := extras[ns].(map[string]any)
	v, _ := m[key].(string)
	return v
}

// gotifyTopic is the one topic the token may publish to. Gotify has no
// topics, so a token for every topic (or several) cannot say which.
func gotifyTopic(t *Token) (string, error) {
	var topics []string
	if len(t.Grants) > 0 {
		for _, g := range t.Grants {
			if slices.Contains(g.Scopes, ScopePublish) {
				topics = append(topics, g.Topic)
			}
		}
	} else {
		topics = t.Topics
	}
	if len(topics) != 1 || strings.Contains(topics[0], "*") {
		return "", errf(400, "bad_request", "a Gotify app token must be a clrd token limited to one topic, so clrd knows where to post (clrd token create --name myapp --scope publish --topics mytopic)")
	}
	return topics[0], nil
}

// gotifyPriority maps Gotify's 0..10 to clrd's 1..5 the way the Gotify app
// treats them: 0 silent, 1-3 low, 4-7 normal, 8 and up high. Never 5
// (urgent), which Gotify has no word for.
func gotifyPriority(p int) int {
	switch {
	case p <= 0:
		return 1
	case p <= 3:
		return 2
	case p <= 7:
		return 3
	}
	return 4
}
