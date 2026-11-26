package server

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
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

func hasGotifyKey(r *http.Request) bool {
	_, header := r.Header["X-Gotify-Key"]
	return header || r.URL.Query().Has("token") || gotifyBearer(r) != ""
}

// gotifyMessage: POST /message. Without a Gotify key this is ntfy's publish
// to the topic "message", as before.
func (s *Server) gotifyMessage(w http.ResponseWriter, r *http.Request) error {
	// A key that is sent but empty is a wrong key, never the ntfy path.
	if !hasGotifyKey(r) {
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
	topic, err := appTopic(tok, "Gotify")
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
	*pub.Markdown, pub.Click, pub.Attachment = gotifyExtras(in.Extras)
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
	key = gotifyStored(key)
	if key == "" || s.db.QueryRowContext(ctx, `SELECT g.id, g.priority, g.token_id FROM gotify_apps g JOIN tokens t ON t.id = g.token_id WHERE t.hash = ?`,
		hashToken(key)).Scan(&app.id, &app.priority, &id) != nil {
		return nil, gotifyApp{}
	}
	t := s.tokenWhere(ctx, `id = ?`, id)
	if t == nil {
		return nil, gotifyApp{}
	}
	return t, app
}

// gotifyStored is a key in the form Gotify stores it, the form an imported
// key was hashed in. Gotify 3 keeps a "gtfy" key by its ed25519 public key
// (its auth.EnhancedToken.PublicForm), so the stored form alone cannot send;
// older keys are stored as sent. A "gtfy" key is either ident.seed or the
// signed ident.pubkey.unixtime.signature, good for 15 minutes either side of
// its time, as Gotify takes it. "" for a "gtfy" key that does not pass.
func gotifyStored(key string) string {
	rest, ok := strings.CutPrefix(key, "gtfy")
	if !ok {
		return key
	}
	f := strings.Split(rest, ".")
	if len(f) < 2 {
		return ""
	}
	k, err := base64.RawURLEncoding.DecodeString(f[1])
	switch {
	case err != nil:
		return ""
	case len(f) == 2 && len(k) == ed25519.SeedSize:
		k = ed25519.NewKeyFromSeed(k).Public().(ed25519.PublicKey)
	case len(f) == 4 && len(k) == ed25519.PublicKeySize && gotifySigned(k, f[2], f[3]):
	default:
		return ""
	}
	return "gtfy" + f[0] + "." + base64.RawURLEncoding.EncodeToString(k)
}

// gotifySigned checks a signed Gotify key: Ed25519ph over "iat=<time>".
func gotifySigned(pub []byte, ts, sig string) bool {
	t, err := strconv.ParseInt(ts, 10, 64)
	s, err2 := base64.RawURLEncoding.DecodeString(sig)
	if d := now().Unix() - t; err != nil || err2 != nil || d > 15*60 || d < -15*60 {
		return false
	}
	h := sha512.Sum512([]byte("iat=" + strconv.FormatInt(t, 10)))
	return ed25519.VerifyWithOptions(pub, h[:], s, &ed25519.Options{Hash: crypto.SHA512}) == nil
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
		var appid int64
		t, err := s.insertAppToken(r.Context(), randomString(12), in.Name, in.Topic, key, func(tx *sql.Tx, t *Token) error {
			res, err := tx.ExecContext(r.Context(), `INSERT INTO gotify_apps (token_id, priority) VALUES (?, ?)`, t.ID, in.Priority)
			if err == nil {
				appid, _ = res.LastInsertId()
			}
			return err
		})
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"id": appid, "token": key, "name": t.Name, "topic": in.Topic, "token_id": t.ID, "defaultPriority": in.Priority})
		return nil
	})
}

// insertAppToken stores an app key for a compat sender: a publish-only
// token for one topic, with whatever row its API keeps beside it.
func (s *Server) insertAppToken(ctx context.Context, id, name, topic, key string, more func(*sql.Tx, *Token) error) (*Token, error) {
	t := &Token{ID: id, Name: name, Scopes: []string{ScopePublish}, Topics: []string{topic}, CreatedAt: now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := insertToken(ctx, tx, t, hashToken(key)); err != nil {
		return nil, err
	}
	if more != nil {
		if err := more(tx, t); err != nil {
			return nil, err
		}
	}
	return t, tx.Commit()
}

// gotifyExtras reads the extras clrd keeps: Markdown (Gotify's default is
// plain text), the click URL and the big image.
func gotifyExtras(extras map[string]any) (markdown bool, click string, att *Attachment) {
	note, _ := extras["client::notification"].(map[string]any)
	if c, _ := note["click"].(map[string]any); c != nil {
		click, _ = c["url"].(string)
	}
	if img := ntfyIcon(extra(extras, "client::notification", "bigImageUrl")); img != "" {
		att = &Attachment{URL: img}
	}
	return extra(extras, "client::display", "contentType") == "text/markdown", click, att
}

func extra(extras map[string]any, ns, key string) string {
	m, _ := extras[ns].(map[string]any)
	v, _ := m[key].(string)
	return v
}

// appTopic is the one topic the token may publish to. Gotify and Pushover
// have no topics, so a token for every topic (or several) cannot say which.
func appTopic(t *Token, kind string) (string, error) {
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
		return "", errf(400, "bad_request", "a %s app token must be a clrd token limited to one topic, so clrd knows where to post (clrd token create --name myapp --scope publish --topics mytopic)", kind)
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
