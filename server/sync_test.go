package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type msg struct {
	Type         string        `json:"type"`
	Seq          int64         `json:"seq"`
	Notification *Notification `json:"notification"`
}

type wsClient struct {
	t    *testing.T
	c    *websocket.Conn
	seen []msg
}

func dialWS(t *testing.T, base, tok string, since int64) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := fmt.Sprintf("ws%s/api/v1/ws?since=%d", strings.TrimPrefix(base, "http"), since)
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return &wsClient{t: t, c: c}
}

func (w *wsClient) next() msg {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := w.c.Read(ctx)
	if err != nil {
		w.t.Fatalf("ws read: %v", err)
	}
	var m msg
	if err := json.Unmarshal(b, &m); err != nil {
		w.t.Fatal(err)
	}
	w.seen = append(w.seen, m)
	return m
}

// until reads messages until one matches, failing after 5s.
func (w *wsClient) until(match func(msg) bool) msg {
	w.t.Helper()
	for {
		if m := w.next(); match(m) {
			return m
		}
	}
}

func (w *wsClient) lastSeq() int64 {
	var s int64
	for _, m := range w.seen {
		if m.Type == "ready" || m.Notification != nil {
			s = max(s, m.Seq)
		}
	}
	return s
}

func isState(id, state string) func(msg) bool {
	return func(m msg) bool {
		return m.Notification != nil && m.Notification.ID == id && m.Notification.State == state
	}
}

// TestSyncProof is issue #1 on the server side: a dismiss on one device
// reaches the other live, and a device that was offline catches up from its
// last seq, with no gaps, while other traffic is being published.
func TestSyncProof(t *testing.T) {
	s, ts, admin := newTest(t)
	phoneA := mkToken(t, s, "phone-a", []string{ScopeRead}, nil)
	phoneB := mkToken(t, s, "phone-b", []string{ScopeRead}, nil)

	// Background noise on another topic for the whole test.
	stop := make(chan struct{})
	var noise sync.WaitGroup
	noise.Add(1)
	go func() {
		defer noise.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			s.Publish(context.Background(), &Token{Name: "noise", Scopes: []string{ScopeAdmin}}, PublishInput{Topic: "noise", Message: fmt.Sprint(i)})
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() { close(stop); noise.Wait() }()

	a, b := dialWS(t, ts.URL, phoneA, 0), dialWS(t, ts.URL, phoneB, 0)
	a.until(func(m msg) bool { return m.Type == "ready" })
	b.until(func(m msg) bool { return m.Type == "ready" })

	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "title": "Disk 91%"})
	id := n["id"].(string)
	a.until(isState(id, Active))
	b.until(isState(id, Active))

	// A dismisses; B sees it live, attributed to A.
	if code, _ := call(t, ts, "POST", "/api/v1/notifications/"+id+"/dismiss", phoneA, nil); code != 200 {
		t.Fatalf("dismiss: %d", code)
	}
	got := b.until(isState(id, Dismissed))
	if got.Type != EventState || got.Notification.StateBy != "phone-a" {
		t.Fatalf("B got %+v", got)
	}
	a.until(isState(id, Dismissed))

	// B goes offline. While it is away: a new alert, which A acks and the sender resolves.
	b.c.Close(websocket.StatusNormalClosure, "")
	since := b.lastSeq()
	_, n2 := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "external_id": "cpu", "title": "CPU"})
	id2 := n2["id"].(string)
	call(t, ts, "POST", "/api/v1/notifications/"+id2+"/ack", phoneA, nil)
	call(t, ts, "POST", "/infra/cpu/resolve", admin, nil)

	// B reconnects from its last seq and replays exactly what it missed, in order, no gaps.
	b2 := dialWS(t, ts.URL, phoneB, since)
	ready := b2.until(func(m msg) bool { return m.Type == "ready" })
	want := since + 1
	var states []string
	for _, m := range b2.seen {
		if m.Notification == nil {
			continue
		}
		if m.Seq != want {
			t.Fatalf("replay gap: want seq %d, got %d", want, m.Seq)
		}
		want++
		if m.Notification.ID == id2 {
			states = append(states, m.Type+":"+m.Notification.State)
		}
	}
	if strings.Join(states, ",") != "created:active,state:acknowledged,state:resolved" {
		t.Fatalf("B replayed %v", states)
	}
	if ready.Seq != want-1 {
		t.Fatalf("ready seq %d, want the last replayed %d", ready.Seq, want-1)
	}

	// And it is live again after the replay.
	call(t, ts, "POST", "/api/v1/notifications/"+id+"/reopen", phoneA, nil)
	b2.until(isState(id, Active))
}

func TestStreamResetAndPoll(t *testing.T) {
	s, ts, admin := newTest(t)
	for i := 0; i < 3; i++ {
		call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": fmt.Sprint(i)})
	}

	// since ahead of the server (a wiped server): reset, then ready, then live.
	c := dialWS(t, ts.URL, admin, 999)
	if m := c.next(); m.Type != "reset" {
		t.Fatalf("want reset, got %+v", m)
	}
	if m := c.next(); m.Type != "ready" || m.Seq != 3 {
		t.Fatalf("want ready 3, got %+v", m)
	}

	_, ev := call(t, ts, "GET", "/api/v1/events?since=1", admin, nil)
	if len(ev["events"].([]any)) != 2 || ev["seq"] != 3.0 {
		t.Fatalf("poll: %v", ev)
	}
	_, ev = call(t, ts, "GET", "/api/v1/events?since=0&limit=2", admin, nil)
	if len(ev["events"].([]any)) != 2 || ev["seq"] != 2.0 {
		t.Fatalf("poll page: %v", ev)
	}
	if code, out := call(t, ts, "GET", "/api/v1/events?since=50", admin, nil); code != 410 || out["code"] != "reset" {
		t.Fatalf("poll ahead: %d %v", code, out)
	}

	// Prune everything: since=0 is now older than the log.
	s.opt.Retention = -time.Hour
	if err := s.prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/events?since=0", admin, nil); code != 410 {
		t.Fatalf("poll after prune: want 410, got %d", code)
	}
	if _, ev = call(t, ts, "GET", "/api/v1/events?since=3", admin, nil); ev["seq"] != 3.0 {
		t.Fatalf("poll at head after prune: %v", ev)
	}

	// SSE: Last-Event-ID wins over since, events carry id: lines.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/events/sse?since=0", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Last-Event-ID", "3")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Scan()
	if sc.Text() != `data: {"type":"ready","seq":3}` {
		t.Fatalf("sse first line: %q", sc.Text())
	}
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "live"})
	for sc.Scan() && sc.Text() == "" {
	}
	if sc.Text() != "id: 4" {
		t.Fatalf("sse live: %q", sc.Text())
	}
}

func TestSnoozeWakes(t *testing.T) {
	s, ts, admin := newTest(t)
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "later"})
	id := n["id"].(string)
	until := time.Now().Add(50 * time.Millisecond).Format(time.RFC3339Nano)
	if code, n := call(t, ts, "POST", "/api/v1/notifications/"+id+"/snooze", admin, map[string]string{"until": until}); code != 200 || n["state"] != Snoozed {
		t.Fatalf("snooze: %d %v", code, n)
	}
	time.Sleep(60 * time.Millisecond)
	if err := s.wakeSnoozed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, n := call(t, ts, "GET", "/api/v1/notifications/"+id, admin, nil); n["state"] != Active || n["state_by"] != "server" || n["snooze_until"] != nil {
		t.Fatalf("after wake: %v", n)
	}
}

func TestRevokeAndPruneRace(t *testing.T) {
	s, ts, admin := newTest(t)

	// A topic-limited admin cannot manage tokens (it could mint an unlimited one).
	home := mkToken(t, s, "home-admin", []string{ScopeAdmin}, []string{"home"})
	if code, _ := call(t, ts, "POST", "/api/v1/tokens", home, map[string]any{"name": "x", "scopes": []string{"admin"}}); code != 403 {
		t.Fatalf("limited admin minting: want 403, got %d", code)
	}

	// Revoking a token cuts its open stream before the next event, not 30s later.
	phone := mkToken(t, s, "phone", []string{ScopeRead}, nil)
	c := dialWS(t, ts.URL, phone, 0)
	c.until(func(m msg) bool { return m.Type == "ready" })
	_, toks := call(t, ts, "GET", "/api/v1/tokens", admin, nil)
	for _, tk := range toks["tokens"].([]any) {
		if tk.(map[string]any)["name"] == "phone" {
			call(t, ts, "DELETE", "/api/v1/tokens/"+tk.(map[string]any)["id"].(string), admin, nil)
		}
	}
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, b, err := c.c.Read(ctx); err == nil {
		t.Fatalf("revoked stream still got %s", b)
	} else if websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
		t.Fatalf("revoked stream: %v", err)
	}

	// A prune that lands between the cursor check and the query is a reset, not a skip.
	for i := 0; i < 3; i++ {
		call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": fmt.Sprint(i)})
	}
	if _, err := s.db.Exec(`DELETE FROM events WHERE seq <= 2`); err != nil { // s.pruned not moved yet
		t.Fatal(err)
	}
	if code, out := call(t, ts, "GET", "/api/v1/events?since=1", admin, nil); code != 410 || out["code"] != "reset" {
		t.Fatalf("poll across a prune: %d %v", code, out)
	}
	if _, err := s.db.Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/events?since=2", admin, nil); code != 410 {
		t.Fatalf("poll with every event pruned: want 410, got %d", code)
	}
}
