// Package clrd is a tiny client for clrd (https://github.com/thejdubb02/clrd).
//
//	c := clrd.New("", "") // CLRD_URL and CLRD_TOKEN from the environment
//	c.Send(ctx, clrd.Message{Topic: "infra", Message: "Disk 91%", ID: "disk-build"})
//	c.Resolve(ctx, "infra", "disk-build")
package clrd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Notification is the server's notification (docs/SPEC.md section 2).
type Notification struct {
	ID          string     `json:"id"`
	Topic       string     `json:"topic"`
	ExternalID  string     `json:"external_id,omitempty"`
	Title       string     `json:"title,omitempty"`
	Message     string     `json:"message"`
	Priority    int        `json:"priority"`
	Tags        []string   `json:"tags,omitempty"`
	Click       string     `json:"click,omitempty"`
	Actions     []Action   `json:"actions,omitempty"`
	State       string     `json:"state"`
	StateBy     string     `json:"state_by,omitempty"`
	SnoozeUntil *time.Time `json:"snooze_until,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Seq         int64      `json:"seq"`
}

// Action is a link button on a notification.
type Action struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Message is what Send publishes. ID is your stable key (external_id).
type Message struct {
	Topic    string   `json:"topic"`
	Message  string   `json:"message"`
	Title    string   `json:"title,omitempty"`
	Priority int      `json:"priority,omitempty"`
	ID       string   `json:"external_id,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
	Actions  []Action `json:"actions,omitempty"`
}

// Error is a non-2xx answer from the server.
type Error struct {
	Status  int
	Message string `json:"error"`
	Code    string `json:"code"`
}

func (e *Error) Error() string { return fmt.Sprintf("clrd: %d: %s", e.Status, e.Message) }

// Client talks to one clrd server.
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client // redirects are never followed, whatever its CheckRedirect says
}

// New makes a client. Empty url or token fall back to CLRD_URL and CLRD_TOKEN;
// the url defaults to https://clrd.itsjustin.me.
func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("CLRD_URL")
	}
	if baseURL == "" {
		baseURL = "https://clrd.itsjustin.me"
	}
	if token == "" {
		token = os.Getenv("CLRD_TOKEN")
	}
	return &Client{URL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	// Never follow a redirect: Go would resend the token to the new address
	// (even over plain http on the same host) and turn a POST into a GET.
	hc := http.Client{Timeout: 10 * time.Second}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		e := &Error{Status: res.StatusCode}
		if json.Unmarshal(raw, e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(raw))
		}
		if loc := res.Header.Get("Location"); res.StatusCode < 400 && loc != "" {
			e.Message = "redirected to " + loc + ", not followed: point the client at the final URL"
		}
		return e
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) one(ctx context.Context, method, path string, in any) (*Notification, error) {
	var n Notification
	if err := c.do(ctx, method, path, in, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

func byID(id, action string) string {
	return "/api/v1/notifications/" + url.PathEscape(id) + "/" + action
}

func byKey(topic, key, action string) string {
	return "/" + url.PathEscape(topic) + "/" + url.PathEscape(key) + "/" + action
}

// Send publishes a notification.
func (c *Client) Send(ctx context.Context, m Message) (*Notification, error) {
	return c.one(ctx, "POST", "/api/v1/notifications", m)
}

// Resolve clears the open notification with this topic and external id.
func (c *Client) Resolve(ctx context.Context, topic, externalID string) (*Notification, error) {
	return c.one(ctx, "POST", byKey(topic, externalID, "resolve"), nil)
}

// ResolveID clears a notification by server id.
func (c *Client) ResolveID(ctx context.Context, id string) (*Notification, error) {
	return c.one(ctx, "POST", byID(id, "resolve"), nil)
}

// Ack acknowledges the open notification with this topic and external id.
func (c *Client) Ack(ctx context.Context, topic, externalID string) (*Notification, error) {
	return c.one(ctx, "POST", byKey(topic, externalID, "ack"), nil)
}

// AckID acknowledges a notification by server id.
func (c *Client) AckID(ctx context.Context, id string) (*Notification, error) {
	return c.one(ctx, "POST", byID(id, "ack"), nil)
}

// Dismiss clears a notification by server id, as a user would.
func (c *Client) Dismiss(ctx context.Context, id string) (*Notification, error) {
	return c.one(ctx, "POST", byID(id, "dismiss"), nil)
}

// Snooze hides a notification for a duration like "1h".
func (c *Client) Snooze(ctx context.Context, id, duration string) (*Notification, error) {
	return c.one(ctx, "POST", byID(id, "snooze"), map[string]string{"for": duration})
}

// Get fetches one notification by server id.
func (c *Client) Get(ctx context.Context, id string) (*Notification, error) {
	return c.one(ctx, "GET", "/api/v1/notifications/"+url.PathEscape(id), nil)
}

// List returns notifications. state is "open" (default when empty), "all" or a state name.
func (c *Client) List(ctx context.Context, topic, state string) ([]Notification, error) {
	q := url.Values{}
	if state == "" {
		state = "open"
	}
	q.Set("state", state)
	if topic != "" {
		q.Set("topic", topic)
	}
	// ponytail: spec does not pin the list envelope; accept a bare array or {"notifications":[...]}.
	var raw json.RawMessage
	if err := c.do(ctx, "GET", "/api/v1/notifications?"+q.Encode(), nil, &raw); err != nil {
		return nil, err
	}
	var list []Notification
	if json.Unmarshal(raw, &list) == nil {
		return list, nil
	}
	var env struct {
		Notifications []Notification `json:"notifications"`
	}
	err := json.Unmarshal(raw, &env)
	return env.Notifications, err
}
