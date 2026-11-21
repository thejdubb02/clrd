package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Pushover compatibility (issue #116): Pushover's publish call, POST
// /1/messages.json, so senders built for Pushover (scripts, Apprise,
// Home Assistant, Uptime Kuma) work by changing only the host. A Pushover
// app is one clrd token limited to one topic, the same as a Gotify app.

// pushoverErr is an error in Pushover's shape: the bad parameter, if any,
// names itself, as Pushover's clients read it.
type pushoverErr struct {
	param, msg string
	status     int
}

func (e *pushoverErr) Error() string { return e.msg }

func pushoverBad(param, msg string) error { return &pushoverErr{param, msg, 400} }

func (s *Server) pushoverRoutes() {
	s.route("POST /1/messages.json", func(w http.ResponseWriter, r *http.Request) error {
		return s.pushoverReply(w, func() (map[string]any, error) { return s.pushoverPublish(r) })
	})
	s.route("POST /1/users/validate.json", func(w http.ResponseWriter, r *http.Request) error {
		return s.pushoverReply(w, func() (map[string]any, error) {
			f, _, err := pushoverForm(r)
			if err != nil {
				return nil, err
			}
			// Valid means this key could send: the same checks a publish makes.
			tok, err := s.pushoverToken(r.Context(), f)
			if err != nil {
				return nil, err
			}
			topic, err := appTopic(tok, "Pushover")
			if err != nil {
				return nil, err
			}
			if err := tok.need(ScopePublish, topic); err != nil {
				return nil, err
			}
			return map[string]any{"group": 0, "devices": []string{}, "licenses": []string{}}, nil
		})
	})
	// An admin makes a Pushover app: a publish-only token for one topic whose
	// key is shaped like Pushover's (30 letters and digits), which Apprise checks.
	s.api("POST /api/v1/pushover/apps", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		var in struct {
			Name  string `json:"name"`
			Topic string `json:"topic"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		key := randomString(30)
		t, err := s.insertAppToken(r.Context(), "pushover-"+randomString(12), in.Name, in.Topic, key, nil)
		if err != nil {
			return err
		}
		// user is only for senders that insist on one: clrd never checks it.
		writeJSON(w, 200, map[string]any{"token": key, "user": randomString(30), "name": t.Name, "topic": in.Topic, "token_id": t.ID})
		return nil
	})
}

// pushoverReply writes Pushover's envelope: status 1 and a request id, or
// status 0 with the errors and the bad parameter.
func (s *Server) pushoverReply(w http.ResponseWriter, h func() (map[string]any, error)) error {
	id := pushoverRequestID()
	out, err := h()
	if err == nil {
		out["status"], out["request"] = 1, id
		writeJSON(w, 200, out)
		return nil
	}
	status, msg, param := 0, "", ""
	if pe, ok := err.(*pushoverErr); ok {
		status, msg, param = pe.status, pe.msg, pe.param
	} else {
		status, _, msg = s.failStatus(w, err)
	}
	out = map[string]any{"errors": []string{msg}, "status": 0, "request": id}
	if param != "" {
		out[param] = "invalid"
	}
	writeJSON(w, status, out)
	return nil
}

func pushoverRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// pushoverForm reads the parameters as Pushover takes them: a form,
// multipart (with the attachment file) or JSON.
func pushoverForm(r *http.Request) (map[string]string, *multipart.FileHeader, error) {
	f := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var m map[string]any
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil { // the body is capped in ServeHTTP
			return nil, nil, pushoverBad("", "bad JSON body")
		}
		for k, v := range m {
			switch v := v.(type) {
			case string:
				f[k] = v
			case nil:
			default: // numbers and booleans, as Pushover's JSON takes them
				b, _ := json.Marshal(v)
				f[k] = string(b)
			}
		}
		return f, nil, nil
	}
	if err := r.ParseMultipartForm(maxBody); err != nil && err != http.ErrNotMultipart {
		return nil, nil, pushoverBad("", "bad form body")
	}
	for k := range r.Form {
		f[k] = r.Form.Get(k)
	}
	if r.MultipartForm != nil {
		if fh := r.MultipartForm.File["attachment"]; len(fh) > 0 {
			return f, fh[0], nil
		}
	}
	return f, nil, nil
}

// pushoverToken finds the sender's token: a clrd token, or a Pushover app
// key. Only Pushover app keys are looked up by their raw form here, so no
// other stored key (an imported Gotify key, say) can send through this door.
func (s *Server) pushoverToken(ctx context.Context, f map[string]string) (*Token, error) {
	key := f["token"]
	tok := s.lookupToken(ctx, key)
	if tok == nil && key != "" && !isToken(key) {
		tok = s.tokenWhere(ctx, `hash = ? AND id LIKE 'pushover-%'`, hashToken(key))
	}
	if tok == nil {
		return nil, pushoverBad("token", "application token is invalid")
	}
	if f["user"] == "" {
		return nil, pushoverBad("user", "user identifier is invalid")
	}
	return tok, nil
}

func (s *Server) pushoverPublish(r *http.Request) (map[string]any, error) {
	f, fh, err := pushoverForm(r)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		return nil, err
	}
	tok, err := s.pushoverToken(r.Context(), f)
	if err != nil {
		return nil, err
	}
	topic, err := appTopic(tok, "Pushover")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(f["message"]) == "" {
		return nil, pushoverBad("message", "message cannot be blank")
	}
	pub := PublishInput{Topic: topic, Title: f["title"], Message: f["message"], Click: f["url"], Format: "text"}
	if strings.TrimSpace(pub.Title) == "" {
		pub.Title = tok.Name // Pushover uses the app name
	}
	switch {
	case f["html"] == "1":
		pub.Message, pub.Format = pushoverHTML(pub.Message), "markdown"
	case f["monospace"] == "1":
		pub.Message, pub.Format = "```\n"+pub.Message+"\n```", "markdown"
	}
	out := map[string]any{}
	p := 0
	if v := f["priority"]; v != "" {
		if p, err = strconv.Atoi(v); err != nil || p < -2 || p > 2 {
			return nil, pushoverBad("priority", "priority must be -2, -1, 0, 1 or 2")
		}
	}
	pub.Priority = p + 3 // -2 lowest is 1, 2 emergency is 5 (urgent)
	if p == 2 {
		// Emergency: re-alert every retry seconds until expire, as Pushover does.
		retry, err1 := strconv.Atoi(f["retry"])
		expire, err2 := strconv.Atoi(f["expire"])
		if err1 != nil || retry < 30 {
			return nil, pushoverBad("retry", "retry is required for priority 2 and must be at least 30 seconds")
		}
		if err2 != nil || expire < 1 || expire > 10800 {
			return nil, pushoverBad("expire", "expire is required for priority 2 and must be at most 10800 seconds")
		}
		// Pushover rings again if expire reaches the first retry; clrd's
		// shortest repeat is 60s, so a short window still gets one.
		if expire >= retry {
			retry = max(retry, 60)
			pub.Repeat, pub.RepeatTimes = strconv.Itoa(retry)+"s", min(max(expire/retry, 1), 50)
		}
		out["receipt"] = randomString(30)
	} else if v := f["ttl"]; v != "" {
		ttl, err := strconv.Atoi(v)
		if err != nil || ttl < 1 {
			return nil, pushoverBad("ttl", "ttl must be a positive number of seconds")
		}
		pub.Expires = strconv.Itoa(ttl) + "s"
	}
	switch {
	case fh != nil:
		file, err := fh.Open()
		if err != nil {
			return nil, err
		}
		defer file.Close()
		_, err = s.PublishFile(r.Context(), tok, pub, fh.Filename, file)
		return out, err
	case f["attachment_base64"] != "":
		b, err := base64.StdEncoding.DecodeString(f["attachment_base64"])
		if err != nil {
			return nil, pushoverBad("attachment_base64", "attachment_base64 is not valid base64")
		}
		name := "attachment"
		if ext := strings.TrimPrefix(f["attachment_type"], "image/"); ext != f["attachment_type"] && ext != "" {
			name += "." + ext
		}
		_, err = s.PublishFile(r.Context(), tok, pub, name, bytes.NewReader(b))
		return out, err
	}
	_, err = s.Publish(r.Context(), tok, pub)
	return out, err
}

var (
	pushoverBold   = regexp.MustCompile(`(?is)<(?:b|strong)\b[^>]*>(.*?)</(?:b|strong)\s*>`)
	pushoverItalic = regexp.MustCompile(`(?is)<(?:i|em)\b[^>]*>(.*?)</(?:i|em)\s*>`)
	pushoverLink   = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*["']([^"']*)["'][^>]*>(.*?)</a\s*>`)
	pushoverBreak  = regexp.MustCompile(`(?i)<br\s*/?>`)
	pushoverTag    = regexp.MustCompile(`<[^>]*>`)
)

// pushoverHTML turns the HTML Pushover takes (b, i, u, font, a) into
// Markdown: bold, italic and links kept, other tags dropped.
func pushoverHTML(s string) string {
	s = pushoverBreak.ReplaceAllString(s, "\n")
	s = pushoverLink.ReplaceAllString(s, "[$2]($1)")
	s = pushoverBold.ReplaceAllString(s, "**$1**")
	s = pushoverItalic.ReplaceAllString(s, "*$1*")
	return html.UnescapeString(pushoverTag.ReplaceAllString(s, ""))
}
