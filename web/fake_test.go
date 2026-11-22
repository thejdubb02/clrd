package web

// Fake is an in-memory API for the page tests, seeded with a realistic mix of
// notifications. The live inbox is tested against the real binary (e2e/).

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type Fake struct {
	mu    sync.Mutex
	seq   int64
	items map[string]*Notification
}

// NewFake returns a Fake seeded with a realistic mix of notifications.
func NewFake() *Fake {
	f := &Fake{items: map[string]*Notification{}}
	now := time.Now().UTC().Truncate(time.Minute)
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	until := now.Add(3 * time.Hour)
	for _, n := range []Notification{
		{Topic: "home", Title: "Package delivered", Message: "Left at the front door.", Priority: 3, State: "dismissed", StateBy: "pixel-9", CreatedAt: ago(300)},
		{Topic: "infra", Title: "Load average back to normal", Message: "web-2 load 0.8 for 15 minutes.", Priority: 3, State: "resolved", StateBy: "gatus", CreatedAt: ago(240)},
		{Topic: "infra", Title: "TLS certificate renews in 7 days", Message: "notify.example.com, renewal is automatic.", Priority: 2, State: "snoozed", StateBy: "web", SnoozeUntil: &until, CreatedAt: ago(200)},
		{Topic: "deploys", Title: "Deploy finished: api v2.14.0", Message: "3 services rolled, 0 errors.", Priority: 3, Tags: []string{"release"}, State: "acknowledged", StateBy: "pixel-9", CreatedAt: ago(95)},
		{Topic: "agents", Title: "Agent wants approval", Message: "Merge pull request 212 to main? All 148 tests pass.", Priority: 3, Click: "https://github.com/thejdubb02/clrd",
			Actions: []Action{{Label: "Approve", Reply: "yes"}, {Label: "Reject", Reply: "no"}}, CreatedAt: ago(41)},
		{Topic: "home", Title: "Front door open for 10 minutes", Priority: 4, CreatedAt: ago(18)},
		{Topic: "infra", ExternalID: "disk-web-1", Title: "Disk 91% full on web-1", Message: "/var at 91%. Grows about 2% a day.", Priority: 4, Tags: []string{"disk"},
			Actions: []Action{{Label: "Open dashboard", URL: "https://grafana.example.com/d/disk"}, {Label: "Clean /tmp", HTTP: &HTTPAction{URL: "https://ops.example.com/clean?host=web-1"}}, {Label: "Ping", Broadcast: &BroadcastAction{}}}, CreatedAt: ago(9)},
		{Topic: "backups", Title: "Nightly backup failed", Message: "restic exited 1 after 4m 12s.\nRepository is locked by a stale process.", Priority: 5, Tags: []string{"restic"}, CreatedAt: ago(3)},
	} {
		if n.State == "" {
			n.State = "active"
		}
		f.put(n)
	}
	return f
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// put stores n with the next seq.
func (f *Fake) put(n Notification) {
	if n.ID == "" {
		n.ID = newID()
	}
	if n.Tags == nil {
		n.Tags = []string{}
	}
	f.seq++
	n.Seq, n.UpdatedAt = f.seq, time.Now().UTC()
	f.items[n.ID] = &n
}

func (f *Fake) Snapshot(r *http.Request, q Query) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := Snapshot{Seq: f.seq, Counts: map[string]int{}, Items: []Notification{}, Me: "web", MeID: "tok-web", MarkID: "me", SnoozeIntervals: []string{"1h", "4h"}, SnoozeDefault: "1h", TopicKeepClearedDays: map[string]int{"home": 14, "gone": 3}} // gone has no notifications left (#336)
	topics := map[string]int{}
	needle := strings.ToLower(q.Q)
	for _, n := range f.items {
		s.Counts[n.State]++
		if _, ok := topics[n.Topic]; !ok {
			topics[n.Topic] = 0
		}
		if n.State == "active" || n.State == "acknowledged" || n.State == "snoozed" {
			topics[n.Topic]++
		}
		if !slices.Contains(q.States, n.State) || (q.Topic != "" && n.Topic != q.Topic) ||
			(needle != "" && !strings.Contains(strings.ToLower(n.Topic+" "+n.Title+" "+n.Message), needle)) {
			continue
		}
		s.Items = append(s.Items, *n)
	}
	sort.Slice(s.Items, func(i, j int) bool { return s.Items[i].CreatedAt.After(s.Items[j].CreatedAt) })
	for name, open := range topics {
		s.Topics = append(s.Topics, Topic{Name: name, Open: open})
	}
	sort.Slice(s.Topics, func(i, j int) bool { return s.Topics[i].Name < s.Topics[j].Name })
	now := time.Now().UTC()
	day := now.Add(-24 * time.Hour)
	s.Linked = []LinkedDevice{{ID: "tok-laptop", Name: "laptop", Kind: "token", LinkedAt: day, Approve: "denied"},
		{ID: "tok-web", Name: "web", Kind: "browser", Via: "laptop", ViaID: "tok-laptop", Approve: "allowed", LinkedAt: day, LastSeenAt: &now, This: true, CanUnlink: true},
		{ID: "tok-pixel", Name: "pixel-9", Kind: "phone", Via: "web", ViaID: "tok-web", Approve: "ask", LinkedAt: day, CanUnlink: true, Connected: true, CaughtUp: true},
		{ID: "tok-tablet", Name: "tablet", Kind: "phone", Via: "web", ViaID: "tok-web", Approve: "denied", LinkedAt: day, CanUnlink: true},
		{ID: "tok-bot", Name: "build-bot", Kind: "agent", Approve: "denied", LinkedAt: day, CanUnlink: true}}
	s.CanApprove, s.CanInvite = true, q.Devices
	if q.Devices {
		s.Log = []DeviceEvent{{At: day, Action: "linked", Device: "pixel-9", By: "web"}}
	}
	return s, nil
}

func (f *Fake) Act(r *http.Request, id, action string, until *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.items[id]
	if !ok {
		return errors.New("not found")
	}
	m := *n
	m.State = map[string]string{"ack": "acknowledged", "snooze": "snoozed", "dismiss": "dismissed", "reopen": "active"}[action]
	m.StateBy, m.SnoozeUntil, m.Reply = "web", until, ""
	f.put(m)
	return nil
}

// Mark keeps one mark, by "me", like the server's one per author.
func (f *Fake) Mark(r *http.Request, id, mark, note, link string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.items[id]
	if !ok {
		return errors.New("not found")
	}
	m := *n
	m.Marks = nil
	if mark != "" {
		m.Marks = []Mark{{By: "web", ByID: "me", Mark: mark, Note: note, Link: link}}
	}
	f.put(m)
	return nil
}

func (f *Fake) StartSilence(r *http.Request, topic, tag, dur, comment string) error { return nil }
func (f *Fake) EndSilence(r *http.Request, id string) error                         { return nil }

func (f *Fake) FormatIgnore(r *http.Request, topic, rule, senderID string, on bool) error {
	return nil
}

func (f *Fake) SaveHook(*http.Request, string, string, string, []string, *string, string, bool) error {
	return nil
}

func (f *Fake) DeleteHook(*http.Request, string, string) error { return nil }

func (f *Fake) SaveTemplate(*http.Request, Template, string, bool) error       { return nil }
func (f *Fake) SetSenderRules(*http.Request, string, SenderRules, int64) error { return nil }
func (f *Fake) UndoSenderChange(*http.Request, string, int64) error            { return nil }
func (f *Fake) DeleteTemplate(*http.Request, string, string) error             { return nil }

func (f *Fake) Reply(r *http.Request, id, reply, text string, seq int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.items[id]
	if !ok || n.Seq != seq || n.Reply != "" {
		return errors.New("conflict")
	}
	m := *n
	m.State, m.StateBy, m.Reply, m.ReplyText = "acknowledged", "web", reply, strings.TrimSpace(text)
	f.put(m)
	return nil
}
