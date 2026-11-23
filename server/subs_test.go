package server

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// frame is one message of a stream run by followSubs.
type frame struct {
	Type         string        `json:"type"`
	Notification *Notification `json:"notification"`
}

// followSubs runs a filtered stream for raw as the WebSocket does, with
// opened and gate (run on each message before it is handed over) as hooks.
func followSubs(t *testing.T, s *Server, raw string, since int64, opened func() error, gate func(frame)) chan frame {
	t.Helper()
	c := *s.lookupToken(context.Background(), raw)
	c.view = &view{}
	ctx, cancel := context.WithCancel(context.Background())
	out, done := make(chan frame, 100), make(chan struct{})
	go func() {
		defer close(done)
		s.followWith(ctx, &c, since, nil, false, false, opened, func(msg []byte, _ int64) error {
			var f frame
			json.Unmarshal(msg, &f)
			if gate != nil {
				gate(f)
			}
			out <- f
			return nil
		}, func() error { return nil })
	}()
	t.Cleanup(func() { cancel(); <-done })
	return out
}

// untilFrame reads fs until match, returning what it read; fails after 5s.
func untilFrame(t *testing.T, fs chan frame, match func(frame) bool) []frame {
	t.Helper()
	var seen []frame
	for {
		select {
		case f := <-fs:
			if seen = append(seen, f); match(f) {
				return seen
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("stream: no match after %v", seen)
		}
	}
}

func onTopic(topic string) func(frame) bool {
	return func(f frame) bool { return f.Notification != nil && f.Notification.Topic == topic }
}

// subsSetup is a server with a filtered browser token and a way to publish
// and to change the browser's set.
func subsSetup(t *testing.T) (*Server, *httptest.Server, string, func(string), func(string, bool)) {
	s, ts, admin := newTest(t)
	browser := childOf(t, s, mkToken(t, s, "script", []string{ScopeRead}, nil))
	bt := s.lookupToken(context.Background(), browser)
	pub := func(topic string) {
		t.Helper()
		call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": topic, "message": "m"})
	}
	set := func(topic string, on bool) {
		t.Helper()
		if _, err := s.SetSubscriptions(context.Background(), bt, nil, map[string]bool{topic: on}); err != nil {
			t.Fatal(err)
		}
	}
	return s, ts, browser, pub, set
}

// A change that lands while a stream connects is in its filter (review 1).
func TestSubsChangeWhileConnecting(t *testing.T) {
	s, _, browser, pub, set := subsSetup(t)
	set("chat", false) // so since 0 is stale and the stream resets
	fs := followSubs(t, s, browser, 0, func() error { set("infra", false); return nil }, nil)
	untilFrame(t, fs, func(f frame) bool { return f.Type == "ready" })
	pub("infra")
	pub("other")
	for _, f := range untilFrame(t, fs, onTopic("other")) {
		if onTopic("infra")(f) {
			t.Fatal("a change made while connecting was missed: infra rang")
		}
	}
}

// A slow stream never applies a later change to an earlier notification (review 2).
func TestSubsSlowStreamLaterChange(t *testing.T) {
	s, _, browser, pub, set := subsSetup(t)
	set("chat", false)
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fs := followSubs(t, s, browser, s.CurrentSeq(), nil, func(f frame) {
		if f.Type == EventSubscriptions {
			once.Do(func() { close(blocked); <-release })
		}
	})
	untilFrame(t, fs, func(f frame) bool { return f.Type == "ready" })
	set("infra", false)
	<-blocked // the stream holds the infra change while chat is published and turned back on
	pub("chat")
	set("chat", true)
	close(release)
	seen := untilFrame(t, fs, func(f frame) bool { return f.Type == "ready" }) // before other, or the reload may cover it
	pub("other")
	for _, f := range append(seen, untilFrame(t, fs, onTopic("other"))...) {
		if onTopic("chat")(f) {
			t.Fatal("chat, published while unsubscribed, rang after a later resubscribe")
		}
	}
}

// The subscriptions frame on SSE carries no id, so a reconnect cannot skip the reset (review 4).
func TestSubsSSENoID(t *testing.T) {
	s, ts, browser, _, set := subsSetup(t)
	res, lines := openStream(t, fmt.Sprintf("%s/api/v1/events/sse?since=%d", ts.URL, s.CurrentSeq()), browser)
	defer res.Body.Close()
	next := func() string {
		t.Helper()
		select {
		case l := <-lines:
			return l
		case <-time.After(5 * time.Second):
			t.Fatal("sse: nothing")
		}
		return ""
	}
	for !strings.Contains(next(), `"type":"ready"`) {
	}
	set("chat", false)
	prev := ""
	for l := next(); !strings.Contains(l, `"type":"reset"`); prev, l = l, next() {
		if strings.Contains(l, `"type":"subscriptions"`) && strings.HasPrefix(prev, "id:") {
			t.Fatalf("subscriptions frame has an SSE id: %q", prev)
		}
	}
}

// Detaching moves a token to a set with a marker, so a stale since resets,
// and a failed attach writes no event (review 3).
func TestSubsDetachResets(t *testing.T) {
	s, ts, admin := newTest(t)
	laptop := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	id := s.lookupToken(context.Background(), laptop).ID
	before := s.CurrentSeq()
	if code, _ := call(t, ts, "PUT", "/api/v1/tokens/"+id+"/user", admin, map[string]any{"user": "nobody"}); code != 400 || s.CurrentSeq() != before {
		t.Fatalf("failed attach: %d, seq %d to %d", code, before, s.CurrentSeq())
	}
	_, u := call(t, ts, "POST", "/api/v1/users", admin, map[string]any{"name": "ann"})
	call(t, ts, "PUT", "/api/v1/tokens/"+id+"/user", admin, map[string]any{"user": u["id"]})
	stale := s.CurrentSeq()
	call(t, ts, "PUT", "/api/v1/tokens/"+id+"/user", admin, map[string]any{"user": ""})
	if code, _ := call(t, ts, "GET", fmt.Sprintf("/api/v1/events?since=%d", stale), laptop, nil); code != 410 {
		t.Fatalf("stale since after detach: %d", code)
	}
}

// ?for=<user> reads with what any of the user's tokens may read (review 6),
// and a null choice is refused, not read as false (review 8).
func TestSubsForUserAndNull(t *testing.T) {
	s, ts, admin := newTest(t)
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "chat", "message": "m"})
	_, u := call(t, ts, "POST", "/api/v1/users", admin, map[string]any{"name": "ann"})
	uid := u["id"].(string)
	for _, tk := range []struct {
		scopes, topics []string
	}{{[]string{ScopePublish}, nil}, {[]string{ScopeRead}, []string{"chat"}}} { // the oldest only publishes
		id := s.lookupToken(context.Background(), mkToken(t, s, "t", tk.scopes, tk.topics)).ID
		call(t, ts, "PUT", "/api/v1/tokens/"+id+"/user", admin, map[string]any{"user": uid})
	}
	if code, out := call(t, ts, "GET", "/api/v1/subscriptions?for="+uid, admin, nil); code != 200 || subOf(out, "chat") != "true" {
		t.Fatalf("for user: %d %v", code, out)
	}
	if code, out := call(t, ts, "PUT", "/api/v1/subscriptions?for="+uid, admin, map[string]any{"topics": map[string]any{"chat": nil}}); code != 400 {
		t.Fatalf("null: %d %v", code, out)
	}
	if code, out := call(t, ts, "PUT", "/api/v1/subscriptions?for="+uid, admin, map[string]any{"topics": map[string]any{"chat": false}}); code != 200 || subOf(out, "chat") != "false" {
		t.Fatalf("for user put: %d %v", code, out)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/subscriptions?for="+uid, admin, map[string]any{"topics": map[string]any{"infra": false}}); code != 403 {
		t.Fatalf("a topic none of the user's tokens reads: %d", code)
	}
}

// topicsOf lists the topics of a notification list, in order.
func topicsOf(out map[string]any) string {
	var ts []string
	for _, n := range out["notifications"].([]any) {
		ts = append(ts, n.(map[string]any)["topic"].(string))
	}
	return strings.Join(ts, ",")
}

// subOf is whether GET /api/v1/subscriptions marks topic subscribed ("" for absent).
func subOf(out map[string]any, topic string) string {
	ts, _ := out["topics"].([]any)
	for _, t := range ts {
		if m := t.(map[string]any); m["topic"] == topic {
			return fmt.Sprint(m["subscribed"])
		}
	}
	return ""
}

// TestSubscriptions is #617 on the server: a device's view follows its
// owner's set, nobody else's changes, and access never widens.
func TestSubscriptions(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	pub := func(topic string) {
		t.Helper()
		if code, out := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": topic, "message": "m"}); code != 200 && code != 201 {
			t.Fatalf("publish: %d %v", code, out)
		}
	}
	pub("chat")
	pub("infra")

	// An unattached typed-in token is never filtered and has nowhere to store a choice.
	script := mkToken(t, s, "script", []string{ScopeRead}, nil)
	if code, out := call(t, ts, "GET", "/api/v1/subscriptions", script, nil); code != 200 || out["filtered"] != false || len(out["topics"].([]any)) != 0 || out["new_topics"] != nil {
		t.Fatalf("unattached get: %d %v", code, out)
	}
	if code, out := call(t, ts, "PUT", "/api/v1/subscriptions", script, map[string]any{"topics": map[string]any{"chat": false}}); code != 409 {
		t.Fatalf("unattached put: %d %v", code, out)
	}

	// A browser signed in with it shares the tree's set and is filtered.
	browser := childOf(t, s, script)
	code, out := call(t, ts, "GET", "/api/v1/subscriptions", browser, nil)
	if code != 200 || out["filtered"] != true || out["new_topics"] != true || subOf(out, "chat") != "true" {
		t.Fatalf("browser get: %d %v", code, out)
	}
	for _, bad := range []map[string]any{{}, {"topics": map[string]any{"no spaces": false}}} {
		if code, out := call(t, ts, "PUT", "/api/v1/subscriptions", browser, bad); code != 400 {
			t.Fatalf("bad put %v: %d %v", bad, code, out)
		}
	}
	narrow := childOf(t, s, mkToken(t, s, "narrow", []string{ScopeRead}, []string{"infra"}))
	if code, out := call(t, ts, "PUT", "/api/v1/subscriptions", narrow, map[string]any{"topics": map[string]any{"infra": false, "chat": true}}); code != 403 {
		t.Fatalf("unreadable topic: %d %v", code, out)
	}
	if _, out := call(t, ts, "GET", "/api/v1/subscriptions", narrow, nil); subOf(out, "infra") != "true" || subOf(out, "chat") != "" {
		t.Fatalf("a failed put changed something, or showed an unreadable topic: %v", out)
	}

	other := mkToken(t, s, "other", []string{ScopeRead}, nil)
	ws := dialWS(t, ts.URL, browser, s.CurrentSeq())
	ws.until(func(m msg) bool { return m.Type == "ready" })
	otherWS := dialWS(t, ts.URL, childOf(t, s, other), s.CurrentSeq())
	otherWS.until(func(m msg) bool { return m.Type == "ready" })
	before := s.CurrentSeq()

	if code, out := call(t, ts, "PUT", "/api/v1/subscriptions", browser, map[string]any{"topics": map[string]any{"chat": false}}); code != 200 || subOf(out, "chat") != "false" {
		t.Fatalf("unsubscribe: %d %v", code, out)
	}
	// The owner's stream reloads; another owner's never sees the event.
	ws.until(func(m msg) bool { return m.Type == "reset" })
	ws.until(func(m msg) bool { return m.Type == "ready" })
	ws.seen = nil
	pub("chat")
	pub("infra")
	ws.until(func(m msg) bool { return m.Notification != nil && m.Notification.Topic == "infra" })
	for _, m := range ws.seen {
		if m.Notification != nil && m.Notification.Topic == "chat" {
			t.Fatalf("live stream sent an unsubscribed topic: %v", m)
		}
	}
	otherWS.until(func(m msg) bool { return m.Notification != nil && m.Notification.Topic == "infra" })
	for _, m := range otherWS.seen {
		if m.Type == EventSubscriptions {
			t.Fatalf("other owner's stream got %v", m)
		}
	}
	// script shares the browser's owner but is not filtered: no reset (#635).
	for _, tok := range []string{other, admin, script} {
		code, ev := call(t, ts, "GET", fmt.Sprintf("/api/v1/events?since=%d", before), tok, nil)
		if code != 200 || ev["seq"].(float64) < float64(before+2) || strings.Contains(fmt.Sprint(ev["events"]), EventSubscriptions) {
			t.Fatalf("events for another owner: %d %v", code, ev)
		}
	}
	// A device that missed the change starts over.
	if code, _ := call(t, ts, "GET", fmt.Sprintf("/api/v1/events?since=%d", before), browser, nil); code != 410 {
		t.Fatalf("stale since: %d", code)
	}

	// The browser's views leave chat out unless it asks for everything; nobody else's change.
	if _, out := call(t, ts, "GET", "/api/v1/notifications", browser, nil); topicsOf(out) != "infra,infra" {
		t.Fatalf("browser list: %v", topicsOf(out))
	}
	if _, out := call(t, ts, "GET", "/api/v1/notifications?subscribed=all&topic=chat", browser, nil); topicsOf(out) != "chat,chat" {
		t.Fatalf("browse unsubscribed: %v", topicsOf(out))
	}
	if _, out := call(t, ts, "GET", "/api/v1/counts", browser, nil); out["inbox"] != 2.0 {
		t.Fatalf("browser counts: %v", out)
	}
	if _, out := call(t, ts, "GET", "/api/v1/counts?subscribed=all", browser, nil); out["inbox"] != 4.0 {
		t.Fatalf("counts all: %v", out)
	}
	if _, out := call(t, ts, "GET", "/api/v1/topics?subscribed=all", browser, nil); subOf(out, "chat") != "false" || subOf(out, "infra") != "true" {
		t.Fatalf("topics all: %v", out)
	}
	if _, out := call(t, ts, "GET", "/api/v1/topics", browser, nil); subOf(out, "chat") != "" {
		t.Fatalf("topics: %v", out)
	}
	for _, tok := range []string{script, other} {
		if _, out := call(t, ts, "GET", "/api/v1/notifications", tok, nil); topicsOf(out) != "infra,chat,infra,chat" {
			t.Fatalf("unfiltered list: %v", topicsOf(out))
		}
	}
	// ntfy subscribers see every topic, as before.
	if _, body := raw(t, "GET", ts.URL+"/chat/json?poll=1", browser, "", nil); !strings.Contains(body, `"topic":"chat"`) {
		t.Fatalf("ntfy poll: %s", body)
	}

	// new_topics false keeps every topic there is, and only later ones start off.
	if code, out := call(t, ts, "PUT", "/api/v1/subscriptions", browser, map[string]any{"new_topics": false}); code != 200 || subOf(out, "infra") != "true" || subOf(out, "chat") != "false" {
		t.Fatalf("new_topics false: %d %v", code, out)
	}
	pub("later")
	if _, out := call(t, ts, "PUT", "/api/v1/subscriptions", browser, map[string]any{"new_topics": false}); subOf(out, "later") != "false" {
		t.Fatalf("a second false backfilled a later topic: %v", out)
	}
	if _, out := call(t, ts, "GET", "/api/v1/settings", browser, nil); fmt.Sprint(out["subscriptions"]) != "map[filtered:true new_topics:false topics:map[chat:false infra:true]]" {
		t.Fatalf("settings: %v", out["subscriptions"])
	}

	// A full admin sets another owner's set with for=.
	id := s.lookupToken(ctx, browser).ID
	if code, out := call(t, ts, "PUT", "/api/v1/subscriptions?for="+id, admin, map[string]any{"topics": map[string]any{"later": true}}); code != 200 || subOf(out, "later") != "true" {
		t.Fatalf("for: %d %v", code, out)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/subscriptions?for="+id, browser, nil); code != 403 {
		t.Fatalf("for without full admin: %d", code)
	}
}

// TestSubscriptionsUser: a token attached to a user follows that user's set
// itself; another user and ntfy are untouched; detaching ends the filter.
func TestSubscriptionsUser(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "chat", "message": "m"})
	attach := func(raw, user string) {
		t.Helper()
		id := s.lookupToken(ctx, raw).ID
		if code, out := call(t, ts, "PUT", "/api/v1/tokens/"+id+"/user", admin, map[string]any{"user": user}); code != 200 {
			t.Fatalf("attach: %d %v", code, out)
		}
	}
	user := func(name string) string {
		_, u := call(t, ts, "POST", "/api/v1/users", admin, map[string]any{"name": name})
		return u["id"].(string)
	}
	ann, bob := user("ann"), user("bob")
	laptop, bobs := mkToken(t, s, "laptop", []string{ScopeRead}, nil), mkToken(t, s, "bobs", []string{ScopeRead}, nil)
	attach(laptop, ann)
	attach(bobs, bob)
	seq := s.CurrentSeq()
	if code, out := call(t, ts, "PUT", "/api/v1/subscriptions?for="+ann, admin, map[string]any{"topics": map[string]any{"chat": false}}); code != 200 || out["filtered"] != true || subOf(out, "chat") != "false" {
		t.Fatalf("for user: %d %v", code, out)
	}
	if _, out := call(t, ts, "GET", "/api/v1/notifications", laptop, nil); topicsOf(out) != "" {
		t.Fatalf("attached token list: %v", topicsOf(out))
	}
	if code, _ := call(t, ts, "GET", fmt.Sprintf("/api/v1/events?since=%d", seq), laptop, nil); code != 410 {
		t.Fatalf("stale since after the change: %d", code)
	}
	if _, out := call(t, ts, "GET", "/api/v1/notifications", bobs, nil); topicsOf(out) != "chat" {
		t.Fatalf("other user list: %v", topicsOf(out))
	}
	if _, body := raw(t, "GET", ts.URL+"/chat/json?poll=1", laptop, "", nil); !strings.Contains(body, `"topic":"chat"`) {
		t.Fatalf("ntfy poll on an attached token: %s", body)
	}
	attach(laptop, "")
	if _, out := call(t, ts, "GET", "/api/v1/subscriptions", laptop, nil); out["filtered"] != false {
		t.Fatalf("detached: %v", out)
	}
	if _, out := call(t, ts, "GET", "/api/v1/notifications", laptop, nil); topicsOf(out) != "chat" {
		t.Fatalf("detached list: %v", topicsOf(out))
	}
}

// TestSubscriptionsSkipWebPush: an unsubscribed topic sends no Web Push.
func TestSubscriptionsSkipWebPush(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	got := make(chan struct{}, 10)
	svc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		got <- struct{}{}
		w.WriteHeader(201)
	}))
	defer svc.Close()
	s.pushHTTP = svc.Client()
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	phone := childOf(t, s, mkToken(t, s, "phone", []string{ScopeRead}, nil))
	sub := map[string]any{"endpoint": svc.URL + "/sub/1", "keys": map[string]any{"p256dh": b64.EncodeToString(ua.PublicKey().Bytes()), "auth": b64.EncodeToString(auth)}}
	if code, out := call(t, ts, "PUT", "/api/v1/push", phone, sub); code != 204 {
		t.Fatalf("push subscribe: %d %v", code, out)
	}
	if code, out := call(t, ts, "PUT", "/api/v1/subscriptions", phone, map[string]any{"topics": map[string]any{"chat": false}}); code != 200 {
		t.Fatalf("unsubscribe: %d %v", code, out)
	}
	pushed := func(topic string) int {
		t.Helper()
		call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": topic, "message": "m"})
		for len(s.pushq) > 0 {
			s.sendPushes(ctx, <-s.pushq)
		}
		n := len(got)
		for len(got) > 0 {
			<-got
		}
		return n
	}
	if n := pushed("chat"); n != 0 {
		t.Fatalf("pushed an unsubscribed topic: %d", n)
	}
	if n := pushed("infra"); n != 1 {
		t.Fatalf("subscribed topic pushes: %d", n)
	}
}
