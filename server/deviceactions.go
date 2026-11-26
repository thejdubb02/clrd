package server

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Device actions (#401, ntfy's http and broadcast buttons): a request the
// reader's device makes when the button is tapped. A publisher could make
// every reader's phone call any address, internal ones included, so a topic
// takes them only after an admin turns device_actions on; off by default.
// Turning it off later keeps what was already published, like an icon.

// HTTPAction is a request the device sends: method (POST when empty), an
// http(s) URL, headers and a body.
type HTTPAction struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// BroadcastAction is an Android broadcast with string extras. An empty
// intent is the app's default one.
type BroadcastAction struct {
	Intent string            `json:"intent,omitempty"`
	Extras map[string]string `json:"extras,omitempty"`
}

var (
	headerRe  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	intentRe  = regexp.MustCompile(`^[A-Za-z0-9_.]{0,128}$`)
	extraRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	methodsOK = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}
)

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isDeviceAction(a Action) bool { return a.HTTP != nil || a.Broadcast != nil }

// checkDeviceAction bounds what a publisher can make a device send.
func checkDeviceAction(a Action) error {
	bad := func(f string, v ...any) error {
		return errf(400, "bad_request", "action %q: "+f, append([]any{a.Label}, v...)...)
	}
	if isDeviceAction(a) && strings.TrimSpace(a.Label) == "" {
		return bad("needs a label")
	}
	if h := a.HTTP; h != nil {
		if h.Method = strings.ToUpper(h.Method); h.Method == "" {
			h.Method = "POST" // ntfy's default
		}
		if !slices.Contains(methodsOK, h.Method) {
			return bad("method must be one of %s", strings.Join(methodsOK, ", "))
		}
		u, err := url.Parse(h.URL)
		if err != nil || len(h.URL) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return bad("http.url must be an http(s) URL of at most 2048 characters")
		}
		if h.Body != "" && (h.Method == "GET" || h.Method == "HEAD") {
			return bad("a %s request has no body", h.Method)
		}
		if len(h.Headers) > 10 || len(h.Body) > 4096 {
			return bad("at most 10 headers and a 4096 byte body")
		}
		for k, v := range h.Headers {
			if !headerRe.MatchString(k) || len(v) > 1024 || strings.ContainsAny(v, "\r\n") {
				return bad("invalid header %q", k)
			}
		}
	}
	if b := a.Broadcast; b != nil {
		if !intentRe.MatchString(b.Intent) {
			return bad("invalid intent %q", b.Intent)
		}
		if len(b.Extras) > 10 {
			return bad("at most 10 extras")
		}
		for k, v := range b.Extras {
			if !extraRe.MatchString(k) || len(v) > 1024 {
				return bad("invalid extra %q", k)
			}
		}
	}
	return nil
}

// gateDeviceActions refuses http and broadcast actions on a topic that has
// not turned them on, or drops them for a compat sender (dropDevice). A
// publish checks once early, so a refused file is never uploaded, and again
// under s.mu, which the switch also takes: a publish that ends after an admin
// turned it off never keeps one (#447).
func (s *Server) gateDeviceActions(ctx context.Context, q querier, in *PublishInput) error {
	if !slices.ContainsFunc(in.Actions, isDeviceAction) {
		return nil
	}
	var on bool
	err := q.QueryRowContext(ctx, `SELECT device_actions FROM topics WHERE topic = ?`, in.Topic).Scan(&on)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	switch {
	case on:
		return nil
	case in.dropDevice:
		in.Actions = withoutDeviceActions(slices.Clone(in.Actions))
		return nil
	}
	return errf(403, "forbidden", "topic %q does not take http or broadcast actions: an admin turns them on with device_actions", in.Topic)
}

func withoutDeviceActions(actions []Action) []Action {
	kept := actions[:0]
	for _, a := range actions {
		if !isDeviceAction(a) {
			kept = append(kept, a)
		}
	}
	clear(actions[len(kept):])
	return kept
}

// SetTopicDeviceActions turns http and broadcast actions on or off for a
// topic. Admin only: a publisher must not be able to allow them itself.
func (s *Server) SetTopicDeviceActions(ctx context.Context, tok *Token, topic string, on bool) error {
	if err := validTopic(topic); err != nil {
		return err
	}
	if err := tok.need(ScopeAdmin, topic); err != nil {
		return err
	}
	s.mu.Lock() // in order with publishes, which read it under s.mu
	defer s.mu.Unlock()
	if !on { // drop a row that held only this switch
		if _, err := s.db.ExecContext(ctx, `DELETE FROM topics WHERE topic = ? AND icon = '' AND checkin_ms IS NULL AND approvals = 0`, topic); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `UPDATE topics SET device_actions = 0 WHERE topic = ?`, topic)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO topics (topic, device_actions) VALUES (?, 1)
		ON CONFLICT(topic) DO UPDATE SET device_actions = 1`, topic)
	return err
}
