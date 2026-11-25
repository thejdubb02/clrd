// Package web serves the clrd landing page, the docs and the inbox. Every page
// is rendered on the server; static/inbox.js only adds live updates.
//
// The server mounts Handler as its GET fallback and implements API. See
// README.md in this directory for the seam.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thejdubb02/clrd"
	"github.com/thejdubb02/clrd/internal/gzfs"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// staticHash maps each file under static/ to a short hash of its bytes. A page
// names its scripts and stylesheet with ?v=<hash>, so after an upgrade a
// browser fetches the new file instead of pairing the new page with an old
// cached one (#410).
var staticHash = func() map[string]string {
	m := map[string]string{}
	fs.WalkDir(files, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(files, p)
			s := sha256.Sum256(b)
			m[strings.TrimPrefix(p, "static/")] = hex.EncodeToString(s[:6])
		}
		return nil
	})
	return m
}()

// Notification mirrors docs/SPEC.md section 2.
type Notification struct {
	ID          string      `json:"id"`
	Topic       string      `json:"topic"`
	ExternalID  string      `json:"external_id,omitempty"`
	Title       string      `json:"title"`
	Message     string      `json:"message"`
	Priority    int         `json:"priority"`
	Tags        []string    `json:"tags"`
	Click       string      `json:"click,omitempty"`
	Icon        string      `json:"icon,omitempty"`   // http(s) image URL
	Color       string      `json:"color"`            // the sender's own #RRGGBB, "" for none
	Format      string      `json:"format,omitempty"` // "markdown" (also when empty) or "text"
	Attachment  *Attachment `json:"attachment,omitempty"`
	Actions     []Action    `json:"actions,omitempty"`
	State       string      `json:"state"`
	StateBy     string      `json:"state_by"`
	StateByID   string      `json:"state_by_id"`
	Reply       string      `json:"reply"`      // the reply action tapped, "" until someone answers
	ReplyText   string      `json:"reply_text"` // what was typed with a text reply action (#153), "" for none
	SnoozeUntil *time.Time  `json:"snooze_until"`
	Repeat      *Repeat     `json:"repeat"` // re-alert until answered (docs/repeat.md), or nil
	Marks       []Mark      `json:"marks"`  // who saw or dealt with it (docs/SPEC.md section 2)
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Seq         int64       `json:"seq"`
}

// Mark is one reader's note that they saw or actioned a notification. It
// never changes the state.
type Mark struct {
	By   string `json:"by"`
	ByID string `json:"by_id"`
	Mark string `json:"mark"` // seen or actioned
	Note string `json:"note"`
	Link string `json:"link"`
}

// Repeat is how often a notification has alerted again, and when it will
// next; Next is nil once the repeats stopped.
type Repeat struct {
	Count int        `json:"count"`
	Next  *time.Time `json:"next"`
}

// Action is a link (URL), an answer button (Reply) or a device action (HTTP
// or Broadcast, #401) that only a phone runs; the inbox shows it, never runs it.
type Action struct {
	Label     string           `json:"label"`
	URL       string           `json:"url"`
	Reply     string           `json:"reply,omitempty"`
	Text      bool             `json:"text,omitempty"` // answered with typed text (#153)
	HTTP      *HTTPAction      `json:"http,omitempty"`
	Broadcast *BroadcastAction `json:"broadcast,omitempty"`
}

// BroadcastAction is an Android broadcast; Intent "" is the app's default.
type BroadcastAction struct {
	Intent string `json:"intent"`
}

// HTTPAction is the request a phone sends; headers and body are not shown.
type HTTPAction struct {
	Method string `json:"method"`
	URL    string `json:"url"`
}

// Attachment is a file on a notification: uploaded (/api/v1/files/<id>, with
// Type and Size) or external (an http(s) URL, Type "").
type Attachment struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// Image reports whether the card can show the file inline: the types the
// server serves inline (server/files.go); anything else it sends as a download.
func (a Attachment) Image() bool {
	switch a.Type {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	case "": // external: the server never saw it, so go by the name or the link
		for _, s := range []string{a.Name, strings.SplitN(strings.SplitN(a.URL, "?", 2)[0], "#", 2)[0]} {
			switch strings.ToLower(path.Ext(s)) {
			case ".png", ".jpg", ".jpeg", ".gif", ".webp":
				return true
			}
		}
	}
	return false
}

// Links are the actions that open a URL.
func (n Notification) Links() (out []Action) {
	for _, a := range n.Actions {
		if a.URL != "" {
			out = append(out, a)
		}
	}
	return out
}

// Replies are the plain answer buttons still open: none once someone has answered.
func (n Notification) Replies() (out []Action) {
	for _, a := range n.Actions {
		if a.Reply != "" && !a.Text && n.Reply == "" {
			out = append(out, a)
		}
	}
	return out
}

// TextReply is the open reply answered with typed text (#153), or nil.
func (n Notification) TextReply() *Action {
	for _, a := range n.Actions {
		if a.Reply != "" && a.Text && n.Reply == "" {
			return &a
		}
	}
	return nil
}

// DeviceActions are the http and broadcast buttons, shown read-only (#450).
func (n Notification) DeviceActions() (out []Action) {
	for _, a := range n.Actions {
		if a.HTTP != nil || a.Broadcast != nil {
			out = append(out, a)
		}
	}
	return out
}

// Answer is the label of the reply that was tapped.
func (n Notification) Answer() string {
	for _, a := range n.Actions {
		if a.Reply == n.Reply {
			return a.Label
		}
	}
	return n.Reply
}

// Heading is the title or, when a sender gave none (common with ntfy-style
// publishes), the message's first line as plain text (#303).
func (n Notification) Heading() string {
	if n.Title != "" {
		return n.Title
	}
	line, _, _ := strings.Cut(strings.TrimSpace(n.Message), "\n")
	if n.Format == "text" {
		return line
	}
	if s := strings.TrimSpace(html.UnescapeString(tags.ReplaceAllString(body(line, n.Format, true, false), ""))); s != "" {
		return s
	}
	return line
}

// Body is the message, less the first line when that became the heading.
func (n Notification) Body() string {
	if n.Title != "" {
		return n.Message
	}
	_, rest, _ := strings.Cut(strings.TrimSpace(n.Message), "\n")
	return strings.TrimLeft(rest, "\r\n") // keeps an indented code block's indent
}

var tags = regexp.MustCompile(`<[^>]*>`)

type Topic struct {
	Name    string `json:"name"`
	Open    int    `json:"open"`
	Icon    string `json:"icon"` // image URL new notifications in the topic get, "" for none
	Silence bool   `json:"-"`    // this token may silence the topic (#326)
	// Approvals: an approval request or question was sent here (#623).
	Approvals bool `json:"approvals"`
}

// SubTopic is one row of Settings, Topics: a topic the reader can read, as
// GET /api/v1/subscriptions returns it (docs/SPEC.md section 2.3).
type SubTopic struct {
	Topic      string     `json:"topic"`
	Subscribed bool       `json:"subscribed"`
	Open       int        `json:"open"`
	LastAt     *time.Time `json:"last_at"`
	Approvals  bool       `json:"approvals"` // as on Topic (#623)
}

// ApprovalTopic is where approval prompts go unless an agent names another
// (server/approve.go). Unsubscribing from it always warns, even before the
// first request, as does any topic the server marks with Approvals (#623).
const ApprovalTopic = "agents"

// approvals says whether unsubscribing from topic should warn first.
func (s Snapshot) approvals(topic string) bool {
	return topic == ApprovalTopic || slices.ContainsFunc(s.Topics, func(t Topic) bool { return t.Name == topic && t.Approvals })
}

// topicApprovals asks the server whether topic carries approval requests,
// for a plain form post that has no snapshot. An error warns, to be safe.
func topicApprovals(api API, r *http.Request, topic string) bool {
	snap, err := api.Snapshot(r, Query{States: []string{}})
	return err != nil || snap.approvals(topic)
}

// LinkedDevice is one row of the Settings device list (issue #246), as
// GET /api/v1/devices?all=1 returns it.
type LinkedDevice struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"` // token, browser, phone or agent
	Via        string     `json:"via"`
	ViaID      string     `json:"via_id"`
	Approve    string     `json:"approve"` // allowed, ask or denied: may it tap a reply (#329)
	LinkedAt   time.Time  `json:"linked_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	This       bool       `json:"this"`
	CanUnlink  bool       `json:"can_unlink"`
	Connected  bool       `json:"connected"` // has a stream open now (#300)
	CaughtUp   bool       `json:"caught_up"` // one of its streams is past replay
	// ClearsEverywhere: its clears reach every device; off, they hide on it alone (#767).
	ClearsEverywhere bool `json:"clears_everywhere"`
}

// Live is a device connected and caught up. The browser viewing the page
// counts as live; inbox.js shows when its own stream drops.
func (d LinkedDevice) Live() bool { return d.This || d.Connected && d.CaughtUp }

// Status is Live in words, for the dot labels and the Settings list.
func (d LinkedDevice) Status() string {
	switch {
	case d.Live():
		return "connected"
	case d.Connected:
		return "catching up"
	}
	return "offline"
}

// ApproveOption is one choice in a device's "May approve" menu (#329).
type ApproveOption struct {
	Value, Label string
	Selected     bool
}

// Own is d this browser or a token or device it hangs under, walked by id
// since names repeat. The server never lets a device raise those (#329).
func (s Snapshot) Own(d LinkedDevice) bool {
	id := s.MeID
	for range s.Linked { // at most one hop per device, so a loop in bad data ends
		if id == "" {
			return false
		}
		if id == d.ID {
			return true
		}
		i := slices.IndexFunc(s.Linked, func(x LinkedDevice) bool { return x.ID == id })
		if i < 0 {
			return false
		}
		id = s.Linked[i].ViaID
	}
	return false
}

// ApproveOptions is the menu for d, weakest first. For an Own device it
// stops at the current setting: the server refuses a raise there, so the
// page never offers one.
func (s Snapshot) ApproveOptions(d LinkedDevice) []ApproveOption {
	own := s.Own(d)
	var out []ApproveOption
	for _, o := range []ApproveOption{{"denied", "Denied", false}, {"ask", "Ask", false}, {"allowed", "Allowed", false}} {
		o.Selected = o.Value == d.Approve
		out = append(out, o)
		if o.Selected && own {
			break
		}
	}
	return out
}

// ApproveLabel is d's setting in words.
func (d LinkedDevice) ApproveLabel() string {
	switch d.Approve {
	case "allowed":
		return "Allowed"
	case "ask":
		return "Ask"
	}
	return "Denied"
}

// SyncDots is the sync indicator (#295): a dot for each device that syncs,
// this browser first, at most 6 shown, and how many are live.
type SyncDots struct {
	Dots       []LinkedDevice
	More, Live int
}

func (s Snapshot) Sync() (d SyncDots) {
	var all []LinkedDevice
	for _, l := range s.Linked {
		// A token that is not streaming is a script that publishes, not a device that syncs.
		if l.Kind != "token" || l.This || l.Connected {
			all = append(all, l)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].This && !all[j].This })
	for i, l := range all {
		if l.Live() {
			d.Live++
		}
		if i < 6 {
			d.Dots = append(d.Dots, l)
		} else {
			d.More++
		}
	}
	return d
}

// DeviceEvent is one line of the device activity log, newest first.
type DeviceEvent struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"` // linked, signed in or unlinked
	Device string    `json:"device"`
	By     string    `json:"by"`
}

// Query selects the cards for one inbox view.
type Query struct {
	States  []string // e.g. active, acknowledged
	Topic   string   // empty = all topics; one topic shows even when not subscribed (subscribed=all, SPEC 2.3)
	Q       string   // free-text search, empty = none
	Devices bool     // fill Log and SubTopics (the Settings page)
	Senders bool     // fill Senders (the Senders page, #119)
	Sender  string   // fill Sender with this one, or ErrNotFound
	Stats   int      // fill Stats for this many days back (the Stats page, #120); 0 = none
}

// Sender is one service or device that publishes (#119), as GET
// /api/v1/senders returns it (docs/SPEC.md section 5.1).
type Sender struct {
	Rules     SenderRules    `json:"rules"`
	Changes   []SenderChange `json:"-"` // its rule changes, newest first: its own page only
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Template  string         `json:"template"`
	UserAgent string         `json:"user_agent"`
	Address   string         `json:"address"`
	FirstSeen time.Time      `json:"first_seen"`
	LastSeen  time.Time      `json:"last_seen"`
	Count     int            `json:"count"`
	Topics    map[string]int `json:"topics"`
	Recent    []Notification `json:"recent"`
	Form      *RulesForm     `json:"-"` // a refused save, shown again
}

// KindLabel names the kind for people.
func (s Sender) KindLabel() string {
	if l, ok := map[string]string{"person": "Person", "ntfy_user": "ntfy user", "gotify_app": "Gotify app", "pushover_app": "Pushover app", "anonymous": "Anonymous"}[s.Kind]; ok {
		return l
	}
	return "Token"
}

// SenderRules are what an admin sets on a sender (SPEC 5.1); the zero value
// changes nothing.
type SenderRules struct {
	Paused      bool                `json:"paused"`
	Muted       bool                `json:"muted"`
	PriorityMin int                 `json:"priority_min"`
	PriorityMax int                 `json:"priority_max"`
	Routes      map[string][]string `json:"routes"`
	Expect      string              `json:"expect"`      // alarm when quiet this long, "" off; sent empty so a save clears it
	AlertTopic  string              `json:"alert_topic"` // where that alarm goes
}

// RulesForm is a refused save shown again: what was typed, the base it was
// typed on, and the server's message for the one field it named.
type RulesForm struct {
	Rules      SenderRules
	Base       int64
	Field, Err string
}

type rulesFormKey struct{}

// FormRules are the rules the form shows: a refused save's, else the saved.
func (s Sender) FormRules() SenderRules {
	if s.Form != nil {
		return s.Form.Rules
	}
	return s.Rules
}

// LastSeq is the seq of the newest change, 0 for none: the rules form sends
// it back so a save over a newer change is refused (#543). A refused save
// keeps the base it was typed on, so the stale check still holds.
func (s Sender) LastSeq() int64 {
	if s.Form != nil {
		return s.Form.Base
	}
	if len(s.Changes) == 0 {
		return 0
	}
	return s.Changes[0].Seq
}

// FieldErr is the server's message for this form field, "" for none.
func (s Sender) FieldErr(field string) string {
	if s.Form != nil && s.Form.Field == field {
		return s.Form.Err
	}
	return ""
}

// SenderChange is one logged edit of a sender's rules.
type SenderChange struct {
	Seq    int64       `json:"seq"`
	Before SenderRules `json:"before"`
	After  SenderRules `json:"after"`
	By     string      `json:"by"`
	At     time.Time   `json:"at"`
}

// RouteLines are the routes as the form edits them: "topic: target, target",
// one a line.
func (r SenderRules) RouteLines() string {
	var b strings.Builder
	for _, k := range r.routeKeys() {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(r.Routes[k], ", "))
	}
	return b.String()
}

func (r SenderRules) routeKeys() []string {
	keys := make([]string, 0, len(r.Routes))
	for k := range r.Routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Summary says the rules in a few words, for the change log.
func (r SenderRules) Summary() string {
	var p []string
	if r.Paused {
		p = append(p, "paused")
	}
	if r.Muted {
		p = append(p, "muted")
	}
	if r.PriorityMin > 0 || r.PriorityMax > 0 {
		lo, hi := r.PriorityMin, r.PriorityMax
		if lo == 0 {
			lo = 1
		}
		if hi == 0 {
			hi = 5
		}
		p = append(p, fmt.Sprintf("priority %d to %d", lo, hi))
	}
	for _, k := range r.routeKeys() {
		v := strings.Join(r.Routes[k], ", ")
		if v == "" {
			v = "nowhere"
		}
		if k == "*" {
			k = "any other topic"
		}
		p = append(p, k+" to "+v)
	}
	if r.Expect != "" {
		a := "alarm if quiet " + r.Expect
		if r.AlertTopic != "" {
			a += " in " + r.AlertTopic
		}
		p = append(p, a)
	}
	if len(p) == 0 {
		return "No rules"
	}
	s := strings.Join(p, "; ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// parseRoutes reads the routes box: "topic: target, target" a line, blank
// lines skipped; "topic:" with no target drops that topic's publishes.
func parseRoutes(s string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if k = strings.TrimSpace(k); !ok || k == "" {
			return nil, fmt.Errorf("route %q needs a topic, a colon, then its targets", l)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("topic %q has two routes; put its targets on one line", k)
		}
		to := []string{}
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				to = append(to, t)
			}
		}
		out[k] = to
	}
	return out, nil
}

// Snapshot is everything one inbox page needs. Seq must be read before (or in
// the same transaction as) Items, so no event can fall between the page and
// the WebSocket replay that starts from Seq.
type Snapshot struct {
	Seq    int64
	Epoch  string // the database Seq belongs to; a different one means start over
	Items  []Notification
	Counts map[string]int // open and recent rows by state
	Topics []Topic
	Linked []LinkedDevice // every device this token can see, for the sync indicator and Settings
	Log    []DeviceEvent
	Me     string // device name of the signed-in token, the state_by its own actions get
	MeID   string // its token id, the state_by_id its own actions get (names can repeat)
	MarkID string // the by_id its marks get: its person, or the top of its token tree
	// Snooze times on each card's menu (Go durations) and the one a tap uses.
	SnoozeIntervals []string
	SnoozeDefault   string
	// Buttons a part swipe on a card shows on a touch screen, per side (docs/SPEC.md section 2.1).
	SwipeStart, SwipeEnd []string
	// Display settings for message bodies (docs/SPEC.md section 2.1).
	Formatting, Autolink, Favicons, LinkPreviews bool
	TopicPreviews                                map[string]bool // overrides LinkPreviews for one topic
	KeepClearedDays                              int             // days cleared messages are kept, 0 = forever
	TopicKeepClearedDays                         map[string]int  // overrides KeepClearedDays for one topic
	Sound                                        string          // this device's own sound (SPEC 2.2), "default" when unset
	Format                                       []FormatTopic   // format check (#308), Settings only: topic and sender groups with problems
	FormatOff                                    []FormatOff     // topics left out of the check (#320)
	FormatDismissed                              []FormatDismiss // findings dismissed (#320)
	Silences                                     []Silence       // running silences (docs/silences.md), Settings only
	// Topic subscriptions (docs/SPEC.md section 2.3). Filtered is false for a
	// token with no user, which always sees every topic and may not change
	// the list. Unsubscribed: Query.Topic is a topic the reader is not
	// subscribed to, shown anyway. SubTopics is Settings only.
	Filtered, NewTopics, Unsubscribed bool
	SubTopics                         []SubTopic
	CanSilence                        bool       // the token may start a silence (it can publish)
	Hooks                             []Hook     // outgoing webhooks (#238), Settings and admin only
	CanHooks                          bool       // the token may manage webhooks (admin)
	CanKeep                           bool       // the token has admin, so it may set how long cleared messages are kept
	CanApprove                        bool       // the token may change who may approve: allowed, all topics (#329)
	Templates                         []Template // saved webhook templates (#129), Settings only
	CanTemplates                      bool       // the token may publish, so it may save templates
	CanInvite                         bool       // the token may make an agent invite (#456): a person's session or a full admin
	BuiltinTemplates                  []string   // names of the built-in webhook templates
	CanSenders                        bool       // a full admin: the Senders page is theirs (#119)
	Senders                           []Sender   // the Senders page only
	Sender                            *Sender    // a sender's own page only
	Stats                             *Stats     // the Stats page only (#120)
	ColorRules                        []ColorRule
}

// ColorRule colors matching cards (docs/SPEC.md section 2.1, Color rules).
type ColorRule struct {
	Topic, Tag, Title, Color, Style string
	PriorityMin                     int `json:"priority_min"`
	PriorityMax                     int `json:"priority_max"`
}

// look is a card's color, RRGGBB in capitals, and style: the first rule
// whose set fields all match, else the sender's color as a bar, else none.
// The color becomes a class name, so anything but six hex digits is none.
func (s Snapshot) look(n Notification) (string, string) {
	color, style := n.Color, "bar"
	for _, r := range s.ColorRules {
		if (r.Topic == "" || r.Topic == n.Topic) && (r.Tag == "" || slices.Contains(n.Tags, r.Tag)) &&
			(r.PriorityMin == 0 || n.Priority >= r.PriorityMin) && (r.PriorityMax == 0 || n.Priority <= r.PriorityMax) &&
			strings.Contains(strings.ToLower(n.Title), strings.ToLower(r.Title)) {
			color, style = r.Color, r.Style
			break
		}
	}
	if !slices.Contains([]string{"tint", "solid", "gradient"}, style) {
		style = "bar" // a style this client does not know
	}
	if color = strings.ToUpper(strings.TrimPrefix(color, "#")); !hex6(color) {
		return "", ""
	}
	return color, style
}

func hex6(c string) bool { return len(c) == 6 && strings.Trim(c, "0123456789ABCDEF") == "" }

// cardColors is the card-colors.css query for the colors on a page.
func cardColors(s Snapshot) string {
	var cs []string
	for _, n := range s.Items {
		if c, _ := s.look(n); c != "" && !slices.Contains(cs, c) {
			cs = append(cs, c)
		}
	}
	return strings.Join(cs, ".")
}

// Template is a saved webhook template on one topic
// (docs/integrations/custom-webhooks.md). Every field but Each is a Go
// template; the secret is write only.
type Template struct {
	Topic     string `json:"topic"`
	Name      string `json:"name"`
	Each      string `json:"each"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Priority  string `json:"priority"`
	Tags      string `json:"tags"`
	Click     string `json:"click"`
	ID        string `json:"id"`
	Resolve   string `json:"resolve"`
	HasSecret bool   `json:"has_secret"`
}

// Hook is an outgoing webhook on one topic (docs/integrations/webhooks-out.md).
// The secret is write only: the page knows only whether one is set.
type Hook struct {
	Topic        string         `json:"topic"`
	Name         string         `json:"name"`
	URL          string         `json:"url"`
	Events       []string       `json:"events"`
	Body         string         `json:"body"` // a body template; "" sends the event (#239)
	HasSecret    bool           `json:"has_secret"`
	Enabled      bool           `json:"enabled"`
	FailingSince *time.Time     `json:"failing_since"`
	Deliveries   []HookDelivery `json:"-"` // newest first
}

// HookEvents are the events a hook can send, with their names on the page.
var HookEvents = []struct{ Name, Label string }{
	{"dismissed", "Dismissed"}, {"acknowledged", "Acknowledged"}, {"resolved", "Resolved"},
	{"snoozed", "Snoozed"}, {"action", "Action tapped"},
}

// Sends says whether the hook sends event e.
func (h Hook) Sends(e string) bool { return slices.Contains(h.Events, e) }

// HookDelivery is one try to send an event to a hook.
type HookDelivery struct {
	Seq       int64      `json:"seq"`
	Event     string     `json:"event"`
	Status    string     `json:"status"` // ok, pending or failed
	Attempts  int        `json:"attempts"`
	NextAt    *time.Time `json:"next_at"`
	Code      int        `json:"code"`
	Error     string     `json:"error"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// timeLeft says how long until t, to the minute: "3d 4h", "2h 5m", "12m".
func timeLeft(t time.Time) string {
	m := int(time.Until(t).Round(time.Minute).Minutes())
	switch {
	case m < 1:
		return "under a minute"
	case m >= 24*60:
		return fmt.Sprintf("%dd %dh", m/(24*60), m%(24*60)/60)
	case m >= 60:
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", m)
}

// Silence mutes a topic, a tag or both until a time (docs/silences.md).
type Silence struct {
	ID      string    `json:"id"`
	Topic   string    `json:"topic"`
	Tag     string    `json:"tag"`
	Until   time.Time `json:"until"`
	Comment string    `json:"comment"`
	By      string    `json:"created_by"`
	CanEnd  bool      `json:"-"` // this token may end it
}

// FormatTopic is one topic's format check result (docs/SPEC.md, format-check).
type FormatTopic struct {
	Topic    string          `json:"topic"`
	SenderID string          `json:"sender_id"` // "" when the sender is not known
	Sender   string          `json:"sender"`    // the sending token's name
	CanFix   bool            `json:"-"`         // this token may dismiss or leave the topic out
	Checked  int             `json:"checked"`
	Problems int             `json:"problems"`
	Findings []FormatFinding `json:"findings"`
	Prompt   string          `json:"prompt"`
}

// FormatCard is one topic in the format check, drawn as one card in
// Settings with a group per sender (#618).
type FormatCard struct {
	Topic    string
	Findings int  // finding rows across its senders
	CanFix   bool // this token may leave the topic out
	Groups   []FormatTopic
}

// FormatCards groups Format by topic, in the order topics first appear.
func (s Snapshot) FormatCards() (cards []FormatCard) {
	at := map[string]int{}
	for _, g := range s.Format {
		i, ok := at[g.Topic]
		if !ok {
			i, at[g.Topic] = len(cards), len(cards)
			cards = append(cards, FormatCard{Topic: g.Topic})
		}
		c := &cards[i]
		c.Findings += len(g.Findings)
		c.CanFix = c.CanFix || g.CanFix
		c.Groups = append(c.Groups, g)
	}
	return cards
}

// FormatOff is a topic left out of the format check (#320).
type FormatOff struct {
	Topic  string
	CanFix bool // this token may check it again
}

// FormatDismiss is a format check rule dismissed for a topic, for one
// sender or every sender when SenderID is "" (#320).
type FormatDismiss struct {
	Topic    string `json:"topic"`
	Rule     string `json:"rule"`
	SenderID string `json:"sender_id"`
	Sender   string `json:"sender"`
	CanFix   bool   `json:"-"` // this token may undo it
}

// Label is the rule in plain words.
func (d FormatDismiss) Label() string { return FormatFinding{Rule: d.Rule}.Label() }

type FormatFinding struct {
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

// Label is the rule in plain words.
func (f FormatFinding) Label() string {
	if s, ok := map[string]string{
		"markdown_as_text":  "Markdown sent as plain text, so the symbols show",
		"unknown_shortcode": "Emoji names clrd does not know, shown as typed",
		"no_title":          "No title",
		"long_body":         "Too long to read on a phone",
		"json_dump":         "Raw JSON instead of a sentence",
	}[f.Rule]; ok {
		return s
	}
	return f.Rule
}

// API is what the server provides. The request is passed so the server can
// read its own session cookie. Return ErrUnauthorized when there is no valid
// session; the page then shows nothing but a sign-in notice.
type API interface {
	Snapshot(r *http.Request, q Query) (Snapshot, error)
	// Act changes one notification for the signed-in browser: ack, snooze
	// (until is set), dismiss or reopen.
	Act(r *http.Request, id, action string, until *time.Time) error
	// Reply answers with one of the notification's reply actions, as shown at seq;
	// text is what was typed for a text action (#153), "" for a plain one.
	Reply(r *http.Request, id, reply, text string, seq int64) error
	// Mark sets the reader's own mark (seen or actioned); mark "" removes it.
	Mark(r *http.Request, id, mark, note, link string) error
	// StartSilence (dur is a Go duration) and EndSilence back the Settings
	// silence form without script. Their error text is shown on the page.
	StartSilence(r *http.Request, topic, tag, dur, comment string) error
	// SetSubscriptions changes the reader's topic subscriptions, all or
	// nothing (PUT /api/v1/subscriptions); a nil newTopics keeps it.
	SetSubscriptions(r *http.Request, newTopics *bool, topics map[string]bool) error
	EndSilence(r *http.Request, id string) error
	// FormatIgnore dismisses a format check rule in a topic (for one sender
	// when senderID is set), or with rule "" leaves the topic out; on false
	// undoes it.
	FormatIgnore(r *http.Request, topic, rule, senderID string, on bool) error
	// SaveHook adds or replaces a webhook. A nil body keeps the saved body
	// template, "" sends the event; an empty secret keeps the saved one unless
	// clearSecret.
	// DeleteHook removes it and its log.
	SaveHook(r *http.Request, topic, name, url string, events []string, body *string, secret string, clearSecret bool) error
	DeleteHook(r *http.Request, topic, name string) error
	// SaveTemplate saves a webhook template (t.Topic, t.Name); an empty
	// secret keeps the saved one unless clearSecret. DeleteTemplate removes it.
	SaveTemplate(r *http.Request, t Template, secret string, clearSecret bool) error
	DeleteTemplate(r *http.Request, topic, name string) error
	// SetSenderRules replaces a sender's rules (#119); UndoSenderChange puts
	// back the rules from before change seq. base is the newest change seq
	// the form was drawn from; a newer change refuses the save (#543).
	SetSenderRules(r *http.Request, id string, rules SenderRules, base int64) error
	UndoSenderChange(r *http.Request, id string, seq int64) error
}

var ErrUnauthorized = errors.New("web: unauthorized")

// ErrNotFound: Query.Sender names no sender.
var ErrNotFound = errors.New("web: not found")

// Views of the inbox and the states each one shows.
var views = map[string][]string{
	"inbox":   {"active", "acknowledged"},
	"snoozed": {"snoozed"},
	"cleared": {"dismissed", "resolved"},
}

// templates parses the pages for a site served under base: "" at the root,
// else the path of CLRD_BASE_URL, like "/clrd". Every link to the site
// starts with {{base}}.
func templates(base string) *template.Template {
	var t *template.Template
	t = template.Must(template.New("").Funcs(template.FuncMap{
		"base":          func() string { return base },
		"approvalTopic": func() string { return ApprovalTopic },
		// static is the URL of a file under static/, named by its content (#410).
		"static": func(p string) (string, error) {
			h, ok := staticHash[p]
			if !ok {
				return "", fmt.Errorf("no static file %q", p)
			}
			return base + "/static/" + p + "?v=" + h, nil
		},
		// multiline: a template field saved with a newline edits in a textarea,
		// since a one-line input would drop it (#376).
		"multiline": func(s string) bool { return strings.Contains(s, "\n") },
		// icon renders the i-<name> icon chosen by data, like a tip's.
		"icon": func(name string) (template.HTML, error) {
			var b strings.Builder
			err := t.ExecuteTemplate(&b, "i-"+name, nil)
			return template.HTML(b.String()), err
		},
		// local puts base in front of a path on this server ("/api/v1/files/x").
		"local": func(u string) string {
			if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
				return base + u
			}
			return u
		},
		"iso":   func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"clock": func(t time.Time) string { return t.UTC().Format("Jan 2, 15:04 UTC") },
		"took":  took,
		"card": func(n Notification, view, here string, snap Snapshot) any {
			if !snap.Formatting {
				n.Format = "text" // the untitled headline shows as typed too
			}
			c := struct {
				N             Notification
				View, Here    string
				Snooze        []string
				SnoozeDefault string
				Text          template.HTML
				Preview       bool
				Silence       bool  // offer Silence topic for 1 hour (#326)
				Mine          *Mark // the reader's own mark, which it may change or remove
				Unsub         bool  // offer Unsubscribe from the topic (SPEC 2.3)
				Approvals     bool  // the topic carries approval prompts, so Unsubscribe warns first
				Color, Style  string
			}{n, view, here, snap.SnoozeIntervals, snap.SnoozeDefault, renderBody(n.Body(), n.Format, snap, base), snap.PreviewsOn(n.Topic),
				slices.ContainsFunc(snap.Topics, func(t Topic) bool { return t.Name == n.Topic && t.Silence }), nil,
				snap.Filtered && !snap.Unsubscribed, snap.approvals(n.Topic), "", ""}
			c.Color, c.Style = snap.look(n)
			for i, m := range n.Marks {
				if snap.MarkID != "" && m.ByID == snap.MarkID {
					c.Mine = &n.Marks[i]
				}
			}
			return c
		},
		// demo is the landing page's sample inbox (#592): fixed data drawn by
		// the real card template, so the page always shows the inbox as it is.
		"demo": func() any {
			return struct {
				Items []Notification
				Snap  Snapshot
			}{demoInbox, Snapshot{Formatting: true, SnoozeIntervals: []string{"1h", "4h"}, SnoozeDefault: "1h"}}
		},
		"about":       about,
		"cardColors":  cardColors,
		"snoozeLabel": snoozeLabel,
		"left":        timeLeft,
		"hookEvents":  func() any { return HookEvents },
		// newHook is the add form's blank webhook: every event ticked.
		"newHook": func() Hook {
			h := Hook{}
			for _, e := range HookEvents {
				h.Events = append(h.Events, e.Name)
			}
			return h
		},
		"newTemplate": func() Template { return Template{} },
		"has":         func(list []string, s string) bool { return slices.Contains(list, s) },
		"add":         func(a, b int) int { return a + b },
		"priorities":  func() []string { return []string{"No limit", "1 min", "2 low", "3 default", "4 high", "5 urgent"} },
		"plural": func(n int, one, many string) string {
			if n == 1 {
				return one
			}
			return many
		},
	}).ParseFS(files, "templates/*.html"))
	return t
}

// About is Settings, About (#580): the version this binary runs and its
// history, read from the same JSON as GET /api/v1/version (SPEC 5.2).
type About struct {
	Version, Commit, Date string
	FullHistory           string `json:"full_history"`
	History               []struct {
		Version, Date string
		Changes       []struct {
			Section string
			Items   []template.HTML
		}
	}
}

var inlineGM = goldmark.New(goldmark.WithExtensions(extension.GFM)) // raw HTML stays escaped

// inlineMD renders one changelog line as inline Markdown (SPEC 5.2). A line
// that parses as anything but one paragraph (a "1. " list, a heading) shows
// as plain text instead.
func inlineMD(s string) template.HTML {
	var b bytes.Buffer
	_ = inlineGM.Convert([]byte(s), &b)
	if in, ok := strings.CutPrefix(b.String(), "<p>"); ok && strings.Count(in, "</p>") == 1 && strings.HasSuffix(in, "</p>\n") {
		return template.HTML(strings.TrimSuffix(in, "</p>\n"))
	}
	return template.HTML(template.HTMLEscapeString(s))
}

var about = sync.OnceValue(func() (a About) {
	_ = json.Unmarshal(clrd.VersionJSON(), &a)
	for i := range a.History {
		for _, c := range a.History[i].Changes {
			for k, item := range c.Items {
				c.Items[k] = inlineMD(string(item))
			}
		}
	}
	if len(a.Commit) > 7 {
		a.Commit = a.Commit[:7]
	}
	if len(a.Date) > 10 {
		a.Date = a.Date[:10]
	}
	return a
})

// PreviewsOn says whether a topic's cards show link previews, as the server
// decides for GET /api/v1/preview.
func (s Snapshot) PreviewsOn(topic string) bool {
	if v, ok := s.TopicPreviews[topic]; ok {
		return v
	}
	return s.LinkPreviews
}

// KeepTopics lists, sorted, the topics for Settings' keep by topic: every
// topic with notifications plus any with its own keep but none left (#336).
func (s Snapshot) KeepTopics() []string {
	var out []string
	for _, t := range s.Topics {
		out = append(out, t.Name)
	}
	for t := range s.TopicKeepClearedDays {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// TopicKeep is a topic's own keep in days for the Settings select, or -1 when
// it follows KeepClearedDays.
func (s Snapshot) TopicKeep(topic string) int {
	if v, ok := s.TopicKeepClearedDays[topic]; ok {
		return v
	}
	return -1
}

// TopicPreview is a topic's override for the Settings select: "", "on" or "off".
func (s Snapshot) TopicPreview(topic string) string {
	v, ok := s.TopicPreviews[topic]
	switch {
	case !ok:
		return ""
	case v:
		return "on"
	}
	return "off"
}

// snoozeLabel reads a Go duration as people say it: "1 hour", "2 days", "90 min".
// badge is the open count a browser tab shows before the page name.
func badge(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("(%d) ", n)
}

func snoozeLabel(v string) string {
	d, err := time.ParseDuration(v)
	if err != nil || d%time.Minute != 0 {
		return v
	}
	n, unit := int(d/time.Minute), "min"
	switch {
	case d%(24*time.Hour) == 0:
		n, unit = int(d/(24*time.Hour)), "day"
	case d%time.Hour == 0:
		n, unit = int(d/time.Hour), "hour"
	default:
		return fmt.Sprintf("%d min", n)
	}
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", n, unit)
}

type meta struct {
	Title, Desc, Path, Base string
}

func (m meta) URL() string { return m.Base + m.Path }

// Tip is one card in the empty inbox (issue #244). Copy is text to paste,
// with this server's own address in it; Link is a path under the inbox.
// Icon names an i-<name> icon in icons.html (#292).
type Tip struct{ Icon, Title, Text, Copy, Link, LinkText string }

// demoInbox is the landing page's sample inbox (#592). Fixed data, so the
// page and its test never change with the clock; every claim in it works on
// main: an urgent alert with a link, an agent's question with answer
// buttons, and an alert someone took and an agent marked.
var demoInbox = func() []Notification {
	at := time.Date(2026, 11, 20, 9, 12, 0, 0, time.UTC)
	return []Notification{
		{ID: "demo-backup", Topic: "backups", Title: "Nightly backup failed", Priority: 5, State: "active", Tags: []string{"restic"},
			Message: "restic exited 1 after 4m 12s on **nas-1**.", CreatedAt: at,
			Actions: []Action{{Label: "Open logs", URL: "https://grafana.example.com/d/backups"}}},
		{ID: "demo-deploy", Topic: "deploys", Title: "Deploy web-2 to production?", Priority: 4, State: "active",
			Message: "Tests passed on 4f2c1a9. The agent waits for your answer.", CreatedAt: at.Add(-3 * time.Minute),
			Actions: []Action{{Label: "Approve", Reply: "approve"}, {Label: "Deny", Reply: "deny"}}},
		{ID: "demo-disk", Topic: "infra", Title: "Disk full", Priority: 3, State: "acknowledged", StateBy: "pixel-9",
			Message: "web-1 at 95%.", CreatedAt: at.Add(-18 * time.Minute),
			Marks: []Mark{{By: "ops-agent", Mark: "actioned", Note: "pruned old images, now 71%"}}},
	}
}()

// topicName matches a topic the server accepts (server validTopic).
var topicName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// tips lists only what works on main today, the first-run tip first. Add a
// tip with one line here. The test command posts to the open topic, so it
// lands in this view; only a valid topic name, which is safe in a shell. The
// topic icon tip needs a topic to set an icon on.
func tips(b, topic string, topics []Topic) []Tip {
	to := "test"
	if topicName.MatchString(topic) {
		to = topic
	}
	ts := []Tip{
		{Icon: "terminal", Title: "Send your first notification", Text: "Run this with a token in CLRD_TOKEN. It lands here at once.",
			Copy: "curl -H \"Authorization: Bearer $CLRD_TOKEN\" \\\n  -d '{\"topic\":\"" + to + "\",\"message\":\"Hello from clrd\"}' \\\n  " + b + "/api/v1/notifications"},
		{Icon: "bot", Title: "Ask your AI agent to set it up", Text: "Paste this into Claude Code or another agent that runs commands.",
			Copy: "Add the clrd MCP server: run claude mcp add --transport http clrd " + b + "/mcp --header \"Authorization: Bearer $CLRD_TOKEN\". Then use its send tool to send me a test notification."},
		{Icon: "timer", Title: "Get pinged when a job finishes", Text: "Paste this into your agent before a long task.",
			Copy: "When this task is done, send me a clrd notification: POST " + b + "/api/v1/notifications with the header Authorization: Bearer $CLRD_TOKEN and the JSON {\"topic\":\"jobs\",\"message\":\"<what finished>\"}."},
		{Icon: "smartphone", Title: "Link your phone", Text: "Show a code in Settings and scan it with the clrd app on your phone. Clear on the phone and it clears here too.", Link: "/app/settings#link-h", LinkText: "Link a phone"},
		{Icon: "qr-code", Title: "Sign in another browser by QR", Text: "Open this address there, choose Show sign-in code, and approve it from your phone. No token to type.", Copy: b + "/app"},
		{Icon: "send", Title: "Already publish to ntfy?", Text: "ntfy-style publishing works here too. Point your scripts at this server.",
			Copy: "curl -H \"Authorization: Bearer $CLRD_TOKEN\" -d \"Backup done\" " + b + "/backups"},
		{Icon: "layers", Title: "Stack a busy topic", Text: "Turn on Stack by topic to fold a topic into one card you can clear at once.", Link: "/app/settings#inbox-h", LinkText: "Inbox settings"},
		{Icon: "clock", Title: "Snooze for later", Text: "Snooze a notification from its menu. It comes back and notifies again. Pick your own times in Settings.", Link: "/app/settings#snz-h", LinkText: "Snooze settings"},
		{Icon: "hash", Title: "Give each topic an icon", Text: "Set an icon per topic so you can tell them apart at a glance.", Link: "/app/settings#ticon-h", LinkText: "Topic icons"},
		{Icon: "bell-ring", Title: "Pick a sound", Text: "Choose the sound this browser plays when a notification arrives, or turn it off.", Link: "/app/settings#sound-h", LinkText: "Sound settings"},
	}
	if len(topics) == 0 {
		ts = slices.DeleteFunc(ts, func(t Tip) bool { return t.Icon == "hash" })
	}
	return ts
}

type doc struct {
	Key, Title string
	HTML       template.HTML
}

// docs/*.md (and subdirectories) rendered once at start, keyed by lowercase
// path without .md: docs/SPEC.md is served at /docs/spec, and a folder's
// README.md at the folder, docs/integrations/README.md at /docs/integrations.
var docs, docList = loadDocs()

// A relative link in a doc: no scheme, not absolute, not just a fragment.
var relLink = regexp.MustCompile(`href="([A-Za-z0-9_.][A-Za-z0-9_./-]*)(#[^"]*)?"`)

func docKey(p string) string {
	p = strings.ToLower(p)
	if dir, file := path.Split(p); file == "readme" && dir != "" {
		return strings.TrimSuffix(dir, "/")
	}
	return p
}

func loadDocs() (map[string]doc, []doc) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM), // no Typographer: it would add dashes and ellipses
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	m := map[string]doc{}
	add := func(p string) error {
		src, err := fs.ReadFile(clrd.Files, p)
		if err != nil {
			return err
		}
		if p == "CHANGELOG.md" { // the binary carries recent versions only (#580)
			src = append(src, "\nEvery version, older ones too: [full history on GitHub]("+clrd.FullHistory+").\n"...)
		}
		var b bytes.Buffer
		if err := md.Convert(src, &b); err != nil {
			return err
		}
		// Relative links resolve from this file's folder, as on GitHub: another
		// doc becomes its /docs/ page, anything else in the repo its GitHub page.
		// Code blocks and tables scroll sideways on a phone, so they take
		// keyboard focus (axe: scrollable-region-focusable).
		out := strings.NewReplacer("<pre>", `<pre tabindex="0">`, "<table>", `<table tabindex="0">`).Replace(b.String())
		html := relLink.ReplaceAllStringFunc(out, func(s string) string {
			g := relLink.FindStringSubmatch(s)
			t := path.Join(path.Dir(p), g[1])
			switch {
			case t == ".." || strings.HasPrefix(t, "../"):
				return s
			case strings.HasPrefix(t, "docs/") && strings.HasSuffix(t, ".md"):
				return `href="/docs/` + docKey(strings.TrimSuffix(strings.TrimPrefix(t, "docs/"), ".md")) + g[2] + `"`
			}
			return `href="https://github.com/thejdubb02/clrd/tree/main/` + t + g[2] + `"`
		})
		key := docKey(strings.TrimSuffix(strings.TrimPrefix(p, "docs/"), ".md"))
		title := key
		if line, _, _ := strings.Cut(string(src), "\n"); strings.HasPrefix(line, "# ") {
			title = strings.TrimPrefix(line, "# ")
		}
		m[key] = doc{key, title, template.HTML(html)}
		return nil
	}
	err := fs.WalkDir(clrd.Files, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".md" {
			return err
		}
		return add(p)
	})
	if err == nil {
		err = add("CHANGELOG.md") // served at /docs/changelog
	}
	if err != nil {
		panic(err)
	}
	list := make([]doc, 0, len(m))
	for _, d := range m {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return m, list
}

// Handler serves every web route. baseURL (CLRD_BASE_URL) makes canonical and
// Open Graph URLs absolute; empty falls back to the request host.
func Handler(api API, baseURL string) http.Handler {
	baseURL = strings.TrimRight(baseURL, "/")
	prefix := "" // the sub-path the site is served under, from baseURL
	if u, err := url.Parse(baseURL); err == nil {
		prefix = u.Path
	}
	tpl := templates(prefix)
	base := func(r *http.Request) string {
		if baseURL != "" {
			return baseURL
		}
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
	page := func(w http.ResponseWriter, name string, status int, data any) {
		var b bytes.Buffer
		if err := tpl.ExecuteTemplate(&b, name, data); err != nil {
			log.Printf("web: render %s: %v", name, err)
			http.Error(w, "render failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "no-cache") // a page always names current assets (#410)
		}
		w.WriteHeader(status)
		w.Write(b.Bytes())
	}
	static, _ := fs.Sub(files, "static")
	assets, _ := fs.Sub(clrd.Files, "assets")
	cached := func(prefix string, fsys fs.FS) http.Handler {
		h := http.StripPrefix(prefix, http.FileServerFS(fsys))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			h.ServeHTTP(w, r)
		})
	}

	// The packed build sends its stored gzip as is when it can (#690).
	fileServer := http.FileServerFS(static)
	statics := http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gzfs.Serve(w, r, files, "static/"+r.URL.Path) {
			fileServer.ServeHTTP(w, r)
		}
	}))
	mux := http.NewServeMux()
	// A URL that carries the file's current hash never changes content, so it
	// is cached for good. Any other (no ?v=, or an old one) is revalidated by
	// ETag, so a stale request still gets today's file (#410).
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		h, ok := staticHash[strings.TrimPrefix(r.URL.Path, "/static/")]
		if ok {
			w.Header().Set("ETag", `"`+h+`"`)
		}
		if ok && r.URL.Query().Get("v") == h {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Add("Vary", "Accept-Encoding")
		statics.ServeHTTP(w, r)
	})
	mux.Handle("GET /assets/", cached("/assets/", assets))
	// Card colors (#165): the CSP allows no inline styles, so each color on a
	// page is a class from here, with the text color that reads on it.
	mux.HandleFunc("GET /card-colors.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		for _, c := range strings.Split(r.URL.Query().Get("c"), ".") {
			if v, err := strconv.ParseUint(c, 16, 32); err == nil && hex6(c) {
				// WCAG relative luminance: dark text when it out-contrasts white (#696).
				lin := func(x uint64) float64 {
					s := float64(x&255) / 255
					if s <= 0.04045 {
						return s / 12.92
					}
					return math.Pow((s+0.055)/1.055, 2.4)
				}
				l, ink := 0.2126*lin(v>>16)+0.7152*lin(v>>8)+0.0722*lin(v)+0.05, "#FFFFFF"
				if l/0.0555 >= 1.05/l { // 0.0555 is #0F1114's luminance plus 0.05
					ink = "#0F1114"
				}
				fmt.Fprintf(w, ".cc-%s{--cc:#%s;--cc-ink:%s}\n", c, c, ink)
			}
		}
	})
	// Root icon paths that browsers, wrappers and home screens ask for by
	// name. The manifest's relative URLs resolve under a sub-path base.
	for p, f := range map[string][2]string{
		"/favicon.svg":          {"mark.svg", "image/svg+xml"},
		"/favicon.ico":          {"favicon.ico", "image/x-icon"},
		"/apple-touch-icon.png": {"apple-touch-icon.png", "image/png"},
		"/manifest.webmanifest": {"manifest.webmanifest", "application/manifest+json"},
	} {
		mux.HandleFunc("GET "+p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", f[1])
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeFileFS(w, r, clrd.Files, "assets/"+f[0])
		})
	}
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		tpl.ExecuteTemplate(w, "robots.txt", base(r))
	})
	mux.HandleFunc("GET /sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		tpl.ExecuteTemplate(w, "sitemap.xml", struct {
			Base string
			Docs []doc
		}{base(r), docList})
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		page(w, "landing", 200, meta{
			Title: "clrd: dismiss once, gone everywhere",
			Desc:  "clrd is a self-hosted notification server. Send an alert with one HTTP call; clear it on any device and it clears on all of them. ntfy compatible, open source.",
			Path:  "/", Base: base(r),
		})
	})
	mux.HandleFunc("GET /docs/{key...}", func(w http.ResponseWriter, r *http.Request) {
		key := docKey(strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(r.PathValue("key")), "/"), ".md"))
		data := struct {
			meta
			Doc  doc
			Docs []doc
		}{meta: meta{Title: "Docs: clrd", Desc: "Documentation for clrd, the self-hosted notification server.", Path: "/docs/", Base: base(r)}, Docs: docList}
		if key != "" {
			d, ok := docs[key]
			if !ok {
				data.Title, data.Path = "Not found: clrd docs", r.URL.Path
				page(w, "docs", http.StatusNotFound, data)
				return
			}
			d.HTML = template.HTML(strings.ReplaceAll(string(d.HTML), `href="/docs/`, `href="`+prefix+`/docs/`))
			data.Doc, data.Title, data.Path = d, d.Title+": clrd docs", "/docs/"+key
		}
		page(w, "docs", 200, data)
	})

	app := func(view string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			states := views[view]
			if states == nil { // settings: no list, the rest of the snapshot still counts
				states = []string{}
			}
			q := Query{States: states, Topic: r.URL.Query().Get("topic"), Q: strings.TrimSpace(r.URL.Query().Get("q")), Devices: view == "settings",
				Senders: view == "senders", Sender: r.PathValue("id")}
			if view == "stats" {
				q.Stats = statsDays(r.URL.Query().Get("days"))
			}
			snap, err := api.Snapshot(r, q)
			status := 200
			if errors.Is(err, ErrNotFound) {
				status, err = http.StatusNotFound, nil
			}
			if errors.Is(err, ErrUnauthorized) {
				next := *r.URL // keep the filters, drop a stale ?auth= that would beat the new cookie
				v := next.Query()
				failed := v.Get("error") == "token" // the server sends a bad token back here
				name := ""
				if failed {
					name = v.Get("name") // and the device name typed, so it need not be typed again
				}
				v.Del("auth")
				v.Del("error")
				v.Del("name")
				next.RawQuery = v.Encode()
				_, stale := r.Cookie("clrd_session") // a session that no longer works: unlinked or revoked (#246)
				page(w, "signin", http.StatusUnauthorized, struct {
					meta
					Failed, SignedOut bool
					Name              string
				}{meta{Title: "Sign in: clrd", Path: next.RequestURI(), Base: base(r)}, failed, !failed && stale == nil, name})
				return
			}
			if err != nil {
				log.Printf("web: snapshot: %v", err)
				http.Error(w, "could not load notifications", http.StatusBadGateway)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			c := snap.Counts
			titles := map[string]string{"inbox": "Inbox", "snoozed": "Snoozed", "cleared": "Cleared", "settings": "Settings", "senders": "Senders", "sender": "Sender", "stats": "Stats"}
			if snap.Sender != nil {
				titles["sender"] = snap.Sender.Name
			}
			if (view == "senders" || view == "sender" || view == "stats") && !snap.CanSenders {
				status = http.StatusForbidden
			}
			if fm, ok := r.Context().Value(rulesFormKey{}).(*RulesForm); ok && snap.Sender != nil && status == 200 {
				snap.Sender.Form, status = fm, http.StatusBadRequest
			}
			// A new tip on each visit; the send-a-test tip while nothing has ever arrived.
			ts, tip := tips(base(r), q.Topic, snap.Topics), 0
			if c["active"]+c["acknowledged"]+c["snoozed"]+c["dismissed"]+c["resolved"] > 0 {
				tip = rand.IntN(len(ts))
			}
			page(w, "app", status, struct {
				meta
				View, Heading, Topic, Q, Here string
				Snap                          Snapshot
				Inbox, Snoozed, Cleared       int
				Tips                          []Tip
				Tip                           int
			}{
				meta: meta{Title: badge(c["active"]+c["acknowledged"]) + titles[view] + ": clrd", Path: r.URL.Path, Base: base(r)},
				View: view, Heading: titles[view], Topic: q.Topic, Q: q.Q, Here: r.URL.RequestURI(), Snap: snap, Tips: ts, Tip: tip,
				Inbox:   c["active"] + c["acknowledged"],
				Snoozed: c["snoozed"],
				Cleared: c["dismissed"] + c["resolved"],
			})
		}
	}
	mux.HandleFunc("GET /app", app("inbox"))
	mux.HandleFunc("GET /app/{$}", app("inbox"))
	mux.HandleFunc("GET /app/snoozed", app("snoozed"))
	mux.HandleFunc("GET /app/cleared", app("cleared"))
	mux.HandleFunc("GET /app/settings", app("settings"))
	mux.HandleFunc("GET /app/senders", app("senders"))
	mux.HandleFunc("GET /app/stats", app("stats"))
	mux.HandleFunc("GET /app/senders/{id}", app("sender"))
	// A sender's rules (#119): save them all, or undo one change, as a plain
	// form post. The server checks the full admin.
	mux.HandleFunc("POST /app/senders/{id}", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad form.", http.StatusBadRequest)
			return
		}
		f, id := r.PostForm, r.PathValue("id")
		var err error
		if u := f.Get("undo"); u != "" {
			seq, perr := strconv.ParseInt(u, 10, 64)
			if err = perr; err == nil {
				err = api.UndoSenderChange(r, id, seq)
			}
		} else if !f.Has("priority_min") || !f.Has("priority_max") || !f.Has("base_seq") {
			// Not the rules form: never read a missing field as "clear it".
			err = errors.New("the rules form was incomplete")
		} else {
			rules := SenderRules{Paused: f.Get("paused") != "", Muted: f.Get("muted") != "",
				Expect: strings.TrimSpace(f.Get("expect")), AlertTopic: strings.TrimSpace(f.Get("alert_topic"))}
			var e1, e2, e3 error
			var base int64
			rules.PriorityMin, e1 = strconv.Atoi(f.Get("priority_min"))
			rules.PriorityMax, e2 = strconv.Atoi(f.Get("priority_max"))
			base, e3 = strconv.ParseInt(f.Get("base_seq"), 10, 64)
			if err = errors.Join(e1, e2, e3); err == nil {
				if rules.Routes, err = parseRoutes(f.Get("routes")); err == nil {
					err = api.SetSenderRules(r, id, rules, base)
				}
			}
			// A field the server named: the page again, typed values kept, the
			// message by that field.
			for _, field := range []string{"expect", "alert_topic"} {
				if err != nil && strings.HasPrefix(err.Error(), field) {
					fm := &RulesForm{Rules: rules, Base: base, Field: field, Err: strings.TrimPrefix(err.Error(), "alert_topic: ")}
					app("sender")(w, r.WithContext(context.WithValue(r.Context(), rulesFormKey{}, fm)))
					return
				}
			}
		}
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			http.Error(w, "Not saved: "+err.Error()+". Go back and try again.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/app/senders/"+url.PathEscape(id)+"#rules-h", http.StatusSeeOther)
	})
	// The style guide: every component in every state, no data, no sign-in.
	mux.HandleFunc("GET /app/ui", func(w http.ResponseWriter, r *http.Request) {
		page(w, "ui", 200, meta{Title: "Components: clrd", Path: "/app/ui", Base: base(r)})
	})

	// The card buttons as a plain form post, so they work without JavaScript;
	// then back to the page they came from. Snooze takes a duration because
	// only the browser knows its time zone (the script sends "until" itself).
	mux.HandleFunc("POST /app/act", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad form.", http.StatusBadRequest)
			return
		}
		f := r.PostForm
		next := inboxNext(f.Get("next"), "/app")
		act, until := f.Get("act"), (*time.Time)(nil)
		if s := f.Get("snooze"); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 || d > 31*24*time.Hour {
				http.Error(w, "Snooze needs a duration like 1h.", http.StatusBadRequest)
				return
			}
			t := time.Now().Add(d)
			act, until = "snooze", &t
		}
		var err error
		if reply := f.Get("reply"); reply != "" {
			act = "reply"
			// seq 0 would skip the server's "changed since you looked" check.
			if seq, perr := strconv.ParseInt(f.Get("seq"), 10, 64); perr != nil || seq <= 0 {
				err = errors.New("reply without the card's seq")
			} else {
				err = api.Reply(r, f.Get("id"), reply, strings.ReplaceAll(f.Get("text"), "\r\n", "\n"), seq)
			}
		} else if act == "ack" || act == "snooze" || act == "dismiss" || act == "reopen" {
			err = api.Act(r, f.Get("id"), act, until)
		} else {
			http.Error(w, "Unknown action.", http.StatusBadRequest)
			return
		}
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			log.Printf("web: %s: %v", act, err)
			if act == "reply" { // who may approve (#329): say why, the reader can do something about it
				http.Error(w, "Not answered: "+err.Error()+". Go back and reload the page.", http.StatusBadRequest)
				return
			}
			http.Error(w, "Could not change that notification. Go back and reload the page.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, next, http.StatusSeeOther) // signed out: next shows the sign-in page
	})

	// Card marks (#451) as a plain form post. The card has a form of its own
	// for them, so Enter in the note never presses Ack or a reply button.
	// mark "" removes the reader's own mark.
	mux.HandleFunc("POST /app/mark", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad form.", http.StatusBadRequest)
			return
		}
		f := r.PostForm
		mark, note, link := f.Get("mark"), strings.TrimSpace(f.Get("note")), strings.TrimSpace(f.Get("link"))
		if !f.Has("mark") {
			http.Error(w, "Bad form.", http.StatusBadRequest) // only Remove sends an empty mark
			return
		}
		if mark == "" {
			note, link = "", "" // Remove never fails on a draft note or link
		}
		err := api.Mark(r, f.Get("id"), mark, note, link)
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			log.Printf("web: mark: %v", err)
			http.Error(w, "Not marked: "+err.Error()+". Go back and reload the page.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, inboxNext(f.Get("next"), "/app"), http.StatusSeeOther) // signed out: next shows the sign-in page
	})

	// The Settings silence form and End now as a plain form post (#326). A set
	// end time needs the browser's time zone, so that one stays script-only.
	mux.HandleFunc("POST /app/silence", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad form.", http.StatusBadRequest)
			return
		}
		f := r.PostForm
		var err error
		if id := f.Get("end"); id != "" {
			err = api.EndSilence(r, id)
		} else if d := f.Get("for"); d == "" || d == "until" {
			http.Error(w, "Pick how long. A set end time needs JavaScript. Go back and try again.", http.StatusBadRequest)
			return
		} else {
			err = api.StartSilence(r, strings.TrimSpace(f.Get("topic")), strings.TrimSpace(f.Get("tag")), d, strings.TrimSpace(f.Get("comment")))
		}
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			http.Error(w, "Not saved: "+err.Error()+". Go back and try again.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, inboxNext(f.Get("next"), "/app/settings"), http.StatusSeeOther)
	})

	// Topic subscriptions (SPEC 2.3) as a plain form post: one topic from a
	// card or the not-subscribed banner, or the Settings list, which sends
	// only the rows changed since it was drawn so it never undoes a change
	// made on another device.
	mux.HandleFunc("POST /app/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad form.", http.StatusBadRequest)
			return
		}
		f := r.PostForm
		next := inboxNext(f.Get("next"), "/app/settings#topics")
		var newTopics *bool
		topics := map[string]bool{}
		if f.Has("was") || f.Has("new_topics_was") { // the Settings list, maybe with no rows yet
			for _, v := range f["was"] { // "1:name" or "0:name", as drawn
				was, t, _ := strings.Cut(v, ":")
				if on := slices.Contains(f["on"], t); on != (was == "1") {
					topics[t] = on
				}
			}
			if on := f.Get("new_topics") == "1"; f.Has("new_topics_was") && on != (f.Get("new_topics_was") == "1") {
				newTopics = &on
			}
		} else {
			t, on := f.Get("topic"), f.Get("subscribe") == "1"
			if !on && f.Get("sure") == "" && (t == ApprovalTopic || topicApprovals(api, r, t)) {
				// Not a silent no-op: Settings says what this hides, next to its switch.
				http.Redirect(w, r, "/app/settings#topics", http.StatusSeeOther)
				return
			}
			topics[t] = on
		}
		if newTopics != nil || len(topics) > 0 {
			if err := api.SetSubscriptions(r, newTopics, topics); err != nil && !errors.Is(err, ErrUnauthorized) {
				http.Error(w, "Not saved: "+err.Error()+". Go back and try again.", http.StatusBadRequest)
				return
			}
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	})

	// Settings format check (#320): dismiss a finding, leave a topic out, or
	// undo either, as a plain form post.
	mux.HandleFunc("POST /app/format", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad form.", http.StatusBadRequest)
			return
		}
		f := r.PostForm
		err := api.FormatIgnore(r, f.Get("topic"), f.Get("rule"), f.Get("sender_id"), f.Get("undo") == "")
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			http.Error(w, "Not saved: "+err.Error()+". Go back and try again.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/app/settings#format", http.StatusSeeOther)
	})

	// Settings webhooks (#238): save or delete one as a plain form post.
	mux.HandleFunc("POST /app/hooks", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad form.", http.StatusBadRequest)
			return
		}
		f := r.PostForm
		topic, name := strings.TrimSpace(f.Get("topic")), strings.TrimSpace(f.Get("name"))
		var err error
		if f.Get("delete") != "" {
			err = api.DeleteHook(r, topic, name)
		} else {
			var body *string // a tab from before the field kept no body: keep the saved one
			if _, ok := f["body"]; ok {
				b := strings.TrimSpace(f.Get("body"))
				body = &b
			}
			err = api.SaveHook(r, topic, name, strings.TrimSpace(f.Get("url")), f["events"], body, f.Get("secret"), f.Get("clear_secret") != "")
		}
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			http.Error(w, "Not saved: "+err.Error()+". Go back and try again.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/app/settings#hooks-h", http.StatusSeeOther)
	})

	// Settings webhook templates (#129): save or delete one as a plain form post.
	mux.HandleFunc("POST /app/templates", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad form.", http.StatusBadRequest)
			return
		}
		f := r.PostForm
		t := Template{Topic: strings.TrimSpace(f.Get("topic")), Name: strings.TrimSpace(f.Get("name")), Each: strings.TrimSpace(f.Get("each")),
			Title: f.Get("title"), Message: f.Get("message"), Priority: f.Get("priority"), Tags: f.Get("tags"),
			Click: f.Get("click"), ID: f.Get("id"), Resolve: f.Get("resolve")}
		var err error
		if f.Get("delete") != "" {
			err = api.DeleteTemplate(r, t.Topic, t.Name)
		} else {
			err = api.SaveTemplate(r, t, f.Get("secret"), f.Get("clear_secret") != "")
		}
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			http.Error(w, "Not saved: "+err.Error()+". Go back and try again.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/app/settings#tpl-h", http.StatusSeeOther)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https: http:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if prefix != "" {
			w = prefixed{w, prefix}
		}
		mux.ServeHTTP(w, r)
	})
}

// prefixed puts the sub-path in front of every redirect to a path on this
// site, including the ones ServeMux makes itself (/docs to /docs/).
type prefixed struct {
	http.ResponseWriter
	prefix string
}

func (w prefixed) WriteHeader(code int) {
	if l := w.Header().Get("Location"); strings.HasPrefix(l, "/") && !strings.HasPrefix(l, "//") {
		w.Header().Set("Location", w.prefix+l)
	}
	w.ResponseWriter.WriteHeader(code)
}

// inboxNext is where a form post goes back to: next if it is an inbox page,
// else def. Redirect cleans dot segments, so a path with ".." or a backslash
// could turn into "/\host": refuse those.
func inboxNext(next, def string) string {
	if p, _, _ := strings.Cut(next, "?"); (p != "/app" && !strings.HasPrefix(p, "/app/")) || strings.Contains(p, "..") || strings.Contains(p, "\\") {
		return def
	}
	return next
}
