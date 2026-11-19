package web

// Fake is an in-memory stand-in for the clrd server, for the dev server in
// cmd/clrd-web-dev and for tests. It implements API plus just enough of the
// REST and WebSocket endpoints (docs/SPEC.md sections 3 and 5) for the inbox
// to run end to end. Not for production: no auth, no persistence.

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type event struct {
	Seq          int64         `json:"seq"`
	Type         string        `json:"type"`
	Notification *Notification `json:"notification,omitempty"`
}

type Fake struct {
	mu     sync.Mutex
	seq    int64
	items  map[string]*Notification
	events []event
	subs   map[chan event]bool
}

// NewFake returns a Fake seeded with a realistic mix of notifications.
func NewFake() *Fake {
	f := &Fake{items: map[string]*Notification{}, subs: map[chan event]bool{}}
	now := time.Now().UTC().Truncate(time.Minute)
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	until := now.Add(3 * time.Hour)
	for _, n := range []Notification{
		{Topic: "home", Title: "Package delivered", Message: "Left at the front door.", Priority: 3, State: "dismissed", StateBy: "pixel-9", CreatedAt: ago(300)},
		{Topic: "infra", Title: "Load average back to normal", Message: "web-2 load 0.8 for 15 minutes.", Priority: 3, State: "resolved", StateBy: "gatus", CreatedAt: ago(240)},
		{Topic: "infra", Title: "TLS certificate renews in 7 days", Message: "notify.example.com, renewal is automatic.", Priority: 2, State: "snoozed", StateBy: "web", SnoozeUntil: &until, CreatedAt: ago(200)},
		{Topic: "deploys", Title: "Deploy finished: api v2.14.0", Message: "3 services rolled, 0 errors.", Priority: 3, Tags: []string{"release"}, State: "acknowledged", StateBy: "pixel-9", CreatedAt: ago(95)},
		{Topic: "agents", Title: "Agent wants approval", Message: "Merge pull request 212 to main? All 148 tests pass.", Priority: 3, Click: "https://github.com/thejdubb02/clrd", CreatedAt: ago(41)},
		{Topic: "home", Title: "Front door open for 10 minutes", Priority: 4, CreatedAt: ago(18)},
		{Topic: "infra", ExternalID: "disk-web-1", Title: "Disk 91% full on web-1", Message: "/var at 91%. Grows about 2% a day.", Priority: 4, Tags: []string{"disk"},
			Actions: []Action{{Label: "Open dashboard", URL: "https://grafana.example.com/d/disk"}}, CreatedAt: ago(9)},
		{Topic: "backups", Title: "Nightly backup failed", Message: "restic exited 1 after 4m 12s.\nRepository is locked by a stale process.", Priority: 5, Tags: []string{"restic"}, CreatedAt: ago(3)},
	} {
		if n.State == "" {
			n.State = "active"
		}
		f.put(n, "created")
	}
	return f
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// put stores n and appends an event. Caller holds mu (or is NewFake).
func (f *Fake) put(n Notification, typ string) Notification {
	if n.ID == "" {
		n.ID = newID()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	if n.Tags == nil {
		n.Tags = []string{}
	}
	f.seq++
	n.Seq, n.UpdatedAt = f.seq, time.Now().UTC()
	f.items[n.ID] = &n
	e := event{Seq: f.seq, Type: typ, Notification: &n}
	f.events = append(f.events, e)
	for ch := range f.subs {
		select {
		case ch <- e:
		default: // slow client; it catches up on reconnect
		}
	}
	return n
}

func (f *Fake) Snapshot(r *http.Request, q Query) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := Snapshot{Seq: f.seq, Counts: map[string]int{}, Items: []Notification{}}
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
		s.Topics = append(s.Topics, Topic{name, open})
	}
	sort.Slice(s.Topics, func(i, j int) bool { return s.Topics[i].Name < s.Topics[j].Name })
	now := time.Now().UTC()
	s.Devices = []Device{{"pixel-9", now.Add(-time.Minute)}, {"tablet", now.Add(-4 * time.Minute)}, {"web", now}}
	return s, nil
}

// Register adds the fake REST and WebSocket routes to mux. The device that
// acts is taken from an X-Device header (default "web"), so curl can play a phone.
func (f *Fake) Register(mux *http.ServeMux) {
	device := func(r *http.Request) string {
		if d := r.Header.Get("X-Device"); d != "" {
			return d
		}
		return "web"
	}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /api/v1/notifications", func(w http.ResponseWriter, r *http.Request) {
		var n Notification
		if json.NewDecoder(r.Body).Decode(&n) != nil || n.Topic == "" {
			http.Error(w, `{"error":"topic required","code":"bad_request"}`, http.StatusBadRequest)
			return
		}
		if n.Priority == 0 {
			n.Priority = 3
		}
		n.ID, n.State, n.StateBy, n.CreatedAt = "", "active", "", time.Time{}
		f.mu.Lock()
		n = f.put(n, "created")
		f.mu.Unlock()
		writeJSON(w, n)
	})
	mux.HandleFunc("POST /api/v1/notifications/clear", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Topic string   `json:"topic"`
			IDs   []string `json:"ids"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		cleared := 0
		for _, n := range f.items {
			open := n.State == "active" || n.State == "acknowledged"
			if open && (slices.Contains(body.IDs, n.ID) || (body.Topic != "" && n.Topic == body.Topic)) {
				c := *n
				c.State, c.StateBy = "dismissed", device(r)
				f.put(c, "state")
				cleared++
			}
		}
		f.mu.Unlock()
		writeJSON(w, map[string]int{"cleared": cleared})
	})
	mux.HandleFunc("POST /api/v1/notifications/{id}/{act}", func(w http.ResponseWriter, r *http.Request) {
		state := map[string]string{"ack": "acknowledged", "snooze": "snoozed", "dismiss": "dismissed", "resolve": "resolved", "reopen": "active"}[r.PathValue("act")]
		if state == "" {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		p, ok := f.items[r.PathValue("id")]
		if !ok {
			http.Error(w, `{"error":"not found","code":"not_found"}`, http.StatusNotFound)
			return
		}
		n := *p
		n.State, n.StateBy, n.SnoozeUntil = state, device(r), nil
		if state == "snoozed" {
			t := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
			n.SnoozeUntil = &t
		}
		writeJSON(w, f.put(n, "state"))
	})
	mux.HandleFunc("GET /api/v1/ws", f.serveWS)
}

// serveWS speaks the server side of RFC 6455, send-only: replay, ready, live.
func (f *Fake) serveWS(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket only", http.StatusBadRequest)
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")

	ch := make(chan event, 64)
	f.mu.Lock()
	var backlog []any
	if since > f.seq { // the client is ahead of us, so we restarted: start over
		backlog = append(backlog, map[string]string{"type": "reset"})
	} else {
		for _, e := range f.events {
			if e.Seq > since {
				backlog = append(backlog, e)
			}
		}
		backlog = append(backlog, map[string]any{"type": "ready", "seq": f.seq})
		f.subs[ch] = true
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); delete(f.subs, ch); f.mu.Unlock() }()

	for _, m := range backlog {
		if wsSend(rw.Writer, m) != nil {
			return
		}
	}
	closed := make(chan struct{})
	go func() { io.Copy(io.Discard, rw); close(closed) }() // ponytail: client frames ignored, close is noticed by EOF
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case e := <-ch:
			if wsSend(rw.Writer, e) != nil {
				return
			}
		case <-ping.C:
			if wsFrame(rw.Writer, 0x89, nil) != nil {
				return
			}
		case <-closed:
			return
		}
	}
}

func wsSend(w *bufio.Writer, v any) error {
	b, _ := json.Marshal(v)
	return wsFrame(w, 0x81, b)
}

func wsFrame(w *bufio.Writer, op byte, b []byte) error {
	hdr := []byte{op}
	switch n := len(b); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 1<<16:
		hdr = binary.BigEndian.AppendUint16(append(hdr, 126), uint16(n))
	default:
		hdr = binary.BigEndian.AppendUint64(append(hdr, 127), uint64(n))
	}
	w.Write(hdr)
	w.Write(b)
	return w.Flush()
}
