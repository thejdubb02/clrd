package server

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// nextNotif returns the next stream message that carries a notification.
func nextNotif(t *testing.T, lines chan string) map[string]any {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream closed")
			}
			var m map[string]any
			if !strings.HasPrefix(l, "data: ") || json.Unmarshal([]byte(l[6:]), &m) != nil || m["notification"] == nil {
				continue
			}
			return m
		case <-timeout:
			t.Fatal("no notification event")
		}
	}
}

func notifOf(m map[string]any) (id, state string) {
	n := m["notification"].(map[string]any)
	return n["id"].(string), n["state"].(string)
}

// #767: a device with "clears apply everywhere" off clears for itself alone.
func TestClearsLocal(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	a := mkToken(t, s, "phone-a", []string{ScopeRead}, nil)
	b := mkToken(t, s, "phone-b", []string{ScopeRead}, nil)
	aID := s.lookupToken(ctx, a).ID
	pub := func(msg string) string {
		t.Helper()
		_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": msg})
		return n["id"].(string)
	}
	state := func(tok, id string) string {
		_, n := call(t, ts, "GET", "/api/v1/notifications/"+id, tok, nil)
		return n["state"].(string)
	}
	listed := func(tok, id string) bool {
		_, l := call(t, ts, "GET", "/api/v1/notifications", tok, nil)
		for _, n := range l["notifications"].([]any) {
			if n.(map[string]any)["id"] == id {
				return true
			}
		}
		return false
	}
	active := func(tok string) float64 {
		_, c := call(t, ts, "GET", "/api/v1/counts", tok, nil)
		return c["active"].(float64)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/infra/hooks/gh", admin, map[string]any{"url": "https://hooks.example/x", "events": []string{"dismissed"}}); code != 200 {
		t.Fatalf("hook: %d", code)
	}
	hooks := func() (k int) {
		s.db.QueryRow(`SELECT COUNT(*) FROM hook_deliveries WHERE event = 'dismissed'`).Scan(&k)
		return k
	}

	// Default: on, and a clear is shared as before.
	id0 := pub("zero")
	call(t, ts, "POST", "/api/v1/notifications/"+id0+"/dismiss", a, nil)
	if state(b, id0) != Dismissed || hooks() != 1 {
		t.Fatalf("default clear not shared: %s, hooks %d", state(b, id0), hooks())
	}

	if code, d := call(t, ts, "PUT", "/api/v1/devices/"+aID+"/clears", a, map[string]any{"everywhere": false}); code != 200 || d["clears_everywhere"] != false {
		t.Fatalf("set: %d %v", code, d)
	}
	now := ts.URL + "/api/v1/events/sse?since=" + strconv.FormatInt(s.CurrentSeq(), 10)
	resB, linesB := openStream(t, now, b)
	defer resB.Body.Close()
	resA, linesA := openStream(t, now, a)
	defer resA.Body.Close()

	id1 := pub("one")
	nextNotif(t, linesB)
	nextNotif(t, linesA)
	code, n := call(t, ts, "POST", "/api/v1/notifications/"+id1+"/dismiss", a, nil)
	if code != 200 || n["state"] != Dismissed || n["changed"] != true {
		t.Fatalf("local dismiss: %d %v", code, n)
	}
	if _, n := call(t, ts, "POST", "/api/v1/notifications/"+id1+"/dismiss", a, nil); n["changed"] != false {
		t.Fatalf("repeat local dismiss: %v", n)
	}
	if state(admin, id1) != Active || state(b, id1) != Active || !listed(b, id1) || hooks() != 1 {
		t.Fatalf("local clear leaked: %s %s %v hooks %d", state(admin, id1), state(b, id1), listed(b, id1), hooks())
	}
	if state(a, id1) != Dismissed || listed(a, id1) || active(a) != active(b)-1 {
		t.Fatalf("A still sees it: %s %v %v %v", state(a, id1), listed(a, id1), active(a), active(b))
	}
	if m := nextNotif(t, linesA); m["only"] != aID {
		t.Fatalf("A's event: %v", m)
	} else if gid, st := notifOf(m); gid != id1 || st != Dismissed {
		t.Fatalf("A's event: %v", m)
	}
	// B hears nothing of it: the next thing it gets is the next publish.
	// An edit since reaches B but not A, which keeps it hidden.
	if code, _ := call(t, ts, "PATCH", "/api/v1/notifications/"+id1, admin, map[string]any{"message": "one, edited"}); code != 200 {
		t.Fatalf("edit: %d", code)
	}
	if gid, _ := notifOf(nextNotif(t, linesB)); gid != id1 {
		t.Fatalf("B did not get the edit: %s", gid)
	}
	id2 := pub("two")
	if gid, _ := notifOf(nextNotif(t, linesB)); gid != id2 {
		t.Fatalf("B got %s before the next publish", gid)
	}
	if gid, _ := notifOf(nextNotif(t, linesA)); gid != id2 {
		t.Fatalf("A got %s, its hidden card, before the next publish", gid)
	}
	// A replays from 0: the card comes back cleared, never open.
	resR, linesR := openStream(t, ts.URL+"/api/v1/events/sse?since=0", a)
	got := map[string]string{}
	for {
		gid, st := notifOf(nextNotif(t, linesR))
		got[gid] = st
		if gid == id2 {
			break
		}
	}
	resR.Body.Close()
	if got[id1] != Dismissed {
		t.Fatalf("replay for A: %v", got)
	}
	// The polling form keeps A's event from B too, and topic counts leave it out for A.
	_, poll := call(t, ts, "GET", "/api/v1/events?since=0", b, nil)
	for _, m := range poll["events"].([]any) {
		if m.(map[string]any)["only"] != nil {
			t.Fatalf("B polled A's event: %v", m)
		}
	}
	open := func(tok string) float64 {
		_, l := call(t, ts, "GET", "/api/v1/topics", tok, nil)
		return l["topics"].([]any)[0].(map[string]any)["open"].(float64)
	}
	if open(a) != open(b)-1 {
		t.Fatalf("topic open: A %v, B %v", open(a), open(b))
	}

	// Bulk Clear by A hides only for A.
	cleared := func(tok string) float64 {
		_, c := call(t, ts, "POST", "/api/v1/notifications/clear", tok, map[string]any{"ids": []string{id2}})
		return c["cleared"].(float64)
	}
	if cleared(a) != 1 || state(b, id2) != Active || state(a, id2) != Dismissed {
		t.Fatalf("bulk: %s %s", state(b, id2), state(a, id2))
	}
	nextNotif(t, linesA)

	// Reopen by A brings it back for A only.
	if _, n := call(t, ts, "POST", "/api/v1/notifications/"+id2+"/reopen", a, nil); n["state"] != Active || !listed(a, id2) {
		t.Fatalf("reopen: %v", n)
	}
	if m := nextNotif(t, linesA); m["only"] != aID {
		t.Fatalf("A's reopen event: %v", m)
	}

	// A real clear by B still reaches A.
	call(t, ts, "POST", "/api/v1/notifications/"+id1+"/dismiss", b, nil)
	if gid, st := notifOf(nextNotif(t, linesA)); gid != id1 || st != Dismissed {
		t.Fatalf("B's clear on A: %s %s", gid, st)
	}
	if gid, _ := notifOf(nextNotif(t, linesB)); gid != id1 {
		t.Fatalf("B's own clear: %s", gid)
	}
	if hooks() != 2 {
		t.Fatalf("B's clear fired %d hooks", hooks())
	}
	// B's real clear ended A's hide: A reopening it reopens it for all, and
	// A's own stream shows it again.
	if _, n := call(t, ts, "POST", "/api/v1/notifications/"+id1+"/reopen", a, nil); n["state"] != Active || state(b, id1) != Active {
		t.Fatalf("reopen after a real clear: %v", n)
	}
	if gid, st := notifOf(nextNotif(t, linesA)); gid != id1 || st != Active {
		t.Fatalf("A's stream after reopen: %s %s", gid, st)
	}
}

// #767: the setting follows the rules of who may approve.
func TestSetClearsGate(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	full := mkToken(t, s, "full", []string{ScopeRead}, nil)
	limited := mkToken(t, s, "limited", []string{ScopeRead}, []string{"infra"})
	_, ask, err := s.CreateToken(ctx, "ask", ApproveAsk, []string{ScopeRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	set := func(tok, raw string) int {
		code, _ := call(t, ts, "PUT", "/api/v1/devices/"+s.lookupToken(ctx, raw).ID+"/clears", tok, map[string]any{"everywhere": false})
		return code
	}
	_, l := call(t, ts, "GET", "/api/v1/devices?all=1", admin, nil)
	for _, d := range l["devices"].([]any) {
		if d.(map[string]any)["clears_everywhere"] != true {
			t.Fatalf("default: %v", d)
		}
	}
	if code := set(ask, ask); code != 403 {
		t.Fatalf("ask device: %d", code)
	}
	if code := set(limited, limited); code != 403 {
		t.Fatalf("limited token: %d", code)
	}
	if code := set(full, full); code != 200 {
		t.Fatalf("allowed full token: %d", code)
	}
	if code := set(admin, ask); code != 200 {
		t.Fatalf("admin on another device: %d", code)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+s.lookupToken(ctx, full).ID+"/clears", full, map[string]any{}); code != 400 {
		t.Fatalf("no everywhere: %d", code)
	}

	// A browser signed in with ask's token inherits its option, cannot turn
	// it back on itself, and keeps it when it signs in again.
	everywhere := func(id string) any {
		_, l := call(t, ts, "GET", "/api/v1/devices?all=1", admin, nil)
		for _, d := range l["devices"].([]any) {
			if d.(map[string]any)["id"] == id {
				return d.(map[string]any)["clears_everywhere"]
			}
		}
		return nil
	}
	b1, c1 := browserIn(t, s, ts, ask, installA)
	if everywhere(b1) != false {
		t.Fatalf("browser did not inherit: %v", everywhere(b1))
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+b1+"/clears", c1.Value, map[string]any{"everywhere": true}); code != 403 {
		t.Fatalf("ask browser turned it back on: %d", code)
	}
	// Token back on: the new row still takes the old row's off.
	call(t, ts, "PUT", "/api/v1/devices/"+s.lookupToken(ctx, ask).ID+"/clears", admin, map[string]any{"everywhere": true})
	b2, _ := browserIn(t, s, ts, ask, installA)
	if exists(s, b1) || everywhere(b2) != false {
		t.Fatalf("relink lost the replaced row's option: %v", everywhere(b2))
	}
}

// TestHubOnlyFiltered: another device's own clears never fill a stream's
// buffer, so a big bulk local clear cannot drop it (#777).
func TestHubOnlyFiltered(t *testing.T) {
	h := &hub{subs: map[chan Event]*stream{}}
	a, _ := h.subscribe("a", false)
	for range 300 {
		h.publish(Event{Notification: &Notification{ID: "n"}, Only: "b"})
	}
	h.publish(Event{Notification: &Notification{ID: "n"}, Only: "a"})
	if len(h.subs) != 1 || len(a) != 1 {
		t.Fatalf("subs %d, buffered %d: want a kept with its own event only", len(h.subs), len(a))
	}
}
