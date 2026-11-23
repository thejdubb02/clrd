package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// A notification sent to chosen devices (#82) reaches every reader, but
// only the chosen ones alert: the rest get it silenced, live, on replay and
// on a poll. The stored row and later state changes keep the list; an update
// without one sends to everyone again.
func TestDevicesTarget(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	phone, rawPhone, _ := s.CreateToken(ctx, "Phone", "allowed", []string{ScopeRead}, nil)
	_, rawTab, _ := s.CreateToken(ctx, "tablet", "allowed", []string{ScopeRead}, nil)
	mkToken(t, s, "sender", []string{ScopePublish}, nil) // may not read, so it cannot be chosen
	wp, wt := dialWS(t, ts.URL, rawPhone, 0), dialWS(t, ts.URL, rawTab, 0)
	for _, c := range []*wsClient{wp, wt} {
		c.until(func(m msg) bool { return m.Type == "ready" })
	}
	silent := func(c *wsClient, seq int64) bool {
		t.Helper()
		return c.until(func(m msg) bool { return m.Notification != nil && m.Notification.Seq == seq }).Notification.Silenced
	}

	code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "hi", "external_id": "k", "devices": []string{"PHONE"}})
	if code != 200 || fmt.Sprint(n["devices"]) != "["+phone.ID+"]" {
		t.Fatalf("publish: %d %v", code, n)
	}
	id, seq := n["id"].(string), int64(n["seq"].(float64))
	if silent(wp, seq) || !silent(wt, seq) {
		t.Fatal("live: the phone must alert and the tablet must not")
	}
	poll := func(tok string) bool {
		t.Helper()
		_, out := call(t, ts, "GET", "/api/v1/events?since=0", tok, nil)
		evs := out["events"].([]any)
		return evs[len(evs)-1].(map[string]any)["notification"].(map[string]any)["silenced"].(bool)
	}
	if poll(rawPhone) || !poll(rawTab) {
		t.Fatal("poll: the phone must alert and the tablet must not")
	}
	replay := dialWS(t, ts.URL, rawTab, 0)
	if !silent(replay, seq) {
		t.Fatal("replay: the tablet must not alert")
	}
	// The list and a fetch, which a client rebuilds from after a reset,
	// answer as the stream does (#588); the stored row is unchanged.
	listed := func(tok string) (bool, bool) {
		t.Helper()
		_, l := call(t, ts, "GET", "/api/v1/notifications?state=open", tok, nil)
		_, one := call(t, ts, "GET", "/api/v1/notifications/"+id, tok, nil)
		return l["notifications"].([]any)[0].(map[string]any)["silenced"].(bool), one["silenced"].(bool)
	}
	if l, one := listed(rawPhone); l || one {
		t.Fatal("list: the phone must alert")
	}
	if l, one := listed(rawTab); !l || !one {
		t.Fatal("list: the tablet must not alert")
	}
	// So does the reply to an action, which a client caches as the row.
	for tok, want := range map[string]bool{rawPhone: false, rawTab: true} {
		if code, n := call(t, ts, "POST", "/api/v1/notifications/"+id+"/mark", tok, map[string]any{"mark": "seen"}); code != 200 || n["silenced"] != want {
			t.Fatalf("mark reply: want silenced %v, got %d %v", want, code, n)
		}
	}
	if row, err := getNotification(ctx, s.db, `id = ?`, id); err != nil || row.Silenced {
		t.Fatalf("the stored row changed: %v %v", row, err)
	}

	// A state change loads the row back: the list stays.
	_, n = call(t, ts, "POST", "/api/v1/notifications/"+id+"/ack", admin, map[string]any{})
	if seq = int64(n["seq"].(float64)); !silent(wt, seq) || silent(wp, seq) {
		t.Fatalf("ack: the list was lost: %v", n)
	}
	// An update without devices goes to everyone; an id works as well as a name.
	_, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "all", "external_id": "k"})
	if seq = int64(n["seq"].(float64)); silent(wt, seq) || fmt.Sprint(n["devices"]) != "[]" {
		t.Fatalf("update: %v", n)
	}
	// A device named like another's id is not picked by that id.
	s.CreateToken(ctx, phone.ID, "allowed", []string{ScopeRead}, nil)
	if code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "id", "devices": []string{phone.ID}}); code != 200 || fmt.Sprint(n["devices"]) != "["+phone.ID+"]" {
		t.Fatalf("by id: %d %v", code, n)
	}

	// Live (ntfy stream): the tablet gets the lowest priority, which no ntfy app sounds.
	req, _ := http.NewRequest("GET", ts.URL+"/t/json", nil)
	req.SetBasicAuth("", rawTab)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Scan() // open
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "now", "priority": 5, "live": true, "devices": []string{"phone"}})
	if sc.Scan(); ntfyDecode(t, sc.Text()).Priority != 1 {
		t.Fatalf("live to the tablet: %s", sc.Text())
	}

	many := make([]string, 21)
	for i := range many {
		many[i] = "phone"
	}
	for _, bad := range [][]string{{"nope"}, {"sender"}, {""}, many} {
		if code, e := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t", "message": "x", "devices": bad}); code != 400 {
			b, _ := json.Marshal(e)
			t.Fatalf("devices %q: %d %s", bad, code, b)
		}
	}
}
