package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #119: every publisher shows up as a sender on its first message, with
// counts per topic, its template and hints; anonymous senders split by
// address; only a full admin may list them.
func TestSendersDiscovered(t *testing.T) {
	s, ts, admin := newTest(t)
	svc := mkToken(t, s, "gatus", []string{ScopePublish}, nil)
	for _, m := range []map[string]any{{"topic": "up", "external_id": "k", "message": "a"}, {"topic": "up", "external_id": "k", "message": "b"},
		{"topic": "down", "message": "c"}} {
		if code, _ := call(t, ts, "POST", "/api/v1/notifications", svc, m); code != 201 && code != 200 {
			t.Fatalf("publish: %d", code)
		}
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/up/templates/mine", admin, map[string]any{"title": "{{get . \"t\"}}"}); code != 200 {
		t.Fatalf("save template: %d", code)
	}
	if code, out := raw(t, "POST", ts.URL+"/up?template=mine", svc, `{"t":"hi"}`, map[string]string{"User-Agent": "Gatus/5"}); code != 200 {
		t.Fatalf("webhook: %d %s", code, out)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/ntfy/anonymous", admin, map[string]any{"grants": []Grant{{Topic: "*", Scopes: []string{ScopePublish}}}}); code != 200 {
		t.Fatalf("anonymous: %d", code)
	}
	for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.1"} {
		if code, out := raw(t, "POST", ts.URL+"/open", "", "x", map[string]string{"X-Forwarded-For": ip}); code != 200 {
			t.Fatalf("anonymous publish: %d %s", code, out)
		}
	}
	code, out := call(t, ts, "GET", "/api/v1/senders", admin, nil)
	if code != 200 {
		t.Fatalf("senders: %d", code)
	}
	list := out["senders"].([]any)
	if len(list) != 3 { // gatus and two addresses; the admin only saved a template
		t.Fatalf("senders: %v", list)
	}
	var g map[string]any
	anon := map[string]float64{}
	for _, x := range list {
		sd := x.(map[string]any)
		switch sd["kind"] {
		case "token":
			g = sd
		case "anonymous":
			anon[sd["address"].(string)] = sd["count"].(float64)
		}
	}
	if g == nil || g["name"] != "gatus" || g["count"] != 4.0 || g["template"] != "mine" || g["user_agent"] != "Gatus/5" {
		t.Fatalf("gatus sender: %v", g)
	}
	if tp := g["topics"].(map[string]any); tp["up"] != 3.0 || tp["down"] != 1.0 {
		t.Fatalf("gatus topics: %v", tp)
	}
	if anon["203.0.113.1"] != 2 || anon["203.0.113.2"] != 1 {
		t.Fatalf("anonymous senders: %v", anon)
	}
	code, one := call(t, ts, "GET", "/api/v1/senders/"+g["id"].(string), admin, nil)
	if rec, _ := one["recent"].([]any); code != 200 || len(rec) != 3 || rec[0].(map[string]any)["title"] != "hi" {
		t.Fatalf("sender: %d %v", code, one)
	}
	limited := mkToken(t, s, "limited", []string{ScopeAdmin}, []string{"up"})
	if code, _ := call(t, ts, "GET", "/api/v1/senders", limited, nil); code != 403 {
		t.Fatalf("topic-limited admin: %d", code)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/senders/nope", admin, nil); code != 404 {
		t.Fatalf("unknown sender: %d", code)
	}
	// Another token takes over gatus's external_id row: it leaves gatus's detail.
	other := mkToken(t, s, "other", []string{ScopePublish}, nil)
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", other, map[string]any{"topic": "up", "external_id": "k", "message": "z"}); code != 201 && code != 200 {
		t.Fatalf("other publish: %d", code)
	}
	if _, one := call(t, ts, "GET", "/api/v1/senders/"+g["id"].(string), admin, nil); len(one["recent"].([]any)) != 2 {
		t.Fatalf("taken-over row still listed: %v", one["recent"])
	}
	// Ten newer ones pruned away: the two older ones still stored still show (#530).
	for i := range senderRecent {
		if code, _ := call(t, ts, "POST", "/api/v1/notifications", svc, map[string]any{"topic": "gone", "message": fmt.Sprint(i)}); code != 201 && code != 200 {
			t.Fatalf("publish %d: %d", i, code)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM notifications WHERE topic = 'gone'`); err != nil {
		t.Fatal(err)
	}
	_, one = call(t, ts, "GET", "/api/v1/senders/"+g["id"].(string), admin, nil)
	if rec, _ := one["recent"].([]any); len(rec) != 2 {
		t.Fatalf("older stored ones missing: %v", one["recent"])
	}
	// An anonymous sender with a change log outlives the address cap (#537).
	var anonID string
	s.db.QueryRow(`SELECT id FROM senders WHERE address = '203.0.113.2'`).Scan(&anonID)
	for _, p := range []bool{true, false} {
		if code, _ := call(t, ts, "PUT", "/api/v1/senders/"+anonID, admin, map[string]any{"paused": p}); code != 200 {
			t.Fatalf("pause %v: %d", p, code)
		}
	}
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
		INSERT INTO senders (id, first_seen, last_seen, count, topics, recent) SELECT 'filler-'||i, 1, 9e15, 1, '{}', '[]' FROM n`, senderAnonLimit); err != nil {
		t.Fatal(err)
	}
	if code, _ := raw(t, "POST", ts.URL+"/open", "", "x", map[string]string{"X-Forwarded-For": "203.0.113.9"}); code != 200 {
		t.Fatalf("anonymous publish: %d", code)
	}
	if _, ch := call(t, ts, "GET", "/api/v1/senders/"+anonID+"/changes", admin, nil); len(ch["changes"].([]any)) != 2 {
		t.Fatalf("change log pruned with its sender: %v", ch)
	}
}

// #119: a sender's rules route, mute, clamp and pause what it publishes;
// every change is kept and can be undone; no rules is today's behaviour.
// A sender's icon (#119): set, refused, used before the topic's when a
// publish gives none, on every path, logged and undone like the other rules.
func TestSenderIcon(t *testing.T) {
	s, ts, admin := newTest(t)
	svc := mkToken(t, s, "svc", []string{ScopePublish}, []string{"up"})
	id := s.lookupToken(context.Background(), svc).ID
	const own, mine, topic = "https://img.example/own.png", "https://img.example/svc.png", "https://img.example/up.png"
	icon := func(body map[string]any) any {
		t.Helper()
		body["topic"] = "up"
		code, n := call(t, ts, "POST", "/api/v1/notifications", svc, body)
		if code != 200 && code != 201 {
			t.Fatalf("publish %v: %d %v", body, code, n)
		}
		return n["icon"]
	}
	set := func(v string) (int, map[string]any) {
		return call(t, ts, "PUT", "/api/v1/senders/"+id, admin, map[string]any{"icon": v})
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/up", admin, map[string]any{"icon": topic}); code != 200 {
		t.Fatalf("topic icon: %d", code)
	}
	if got := icon(map[string]any{"message": "a"}); got != topic {
		t.Fatalf("no sender icon: %v, want the topic's", got)
	}
	if code, sd := set(mine); code != 200 || sd["rules"].(map[string]any)["icon"] != mine {
		t.Fatalf("set icon: %d %v", code, sd)
	}
	for _, bad := range []string{"ftp://img.example/a.png", "/a.png", "https://"} {
		if code, _ := set(bad); code != 400 {
			t.Errorf("icon %q: %d, want 400", bad, code)
		}
	}
	if _, sd := call(t, ts, "GET", "/api/v1/senders/"+id, admin, nil); sd["rules"].(map[string]any)["icon"] != mine {
		t.Fatalf("a refused icon changed the rules: %v", sd["rules"])
	}
	if got := icon(map[string]any{"message": "b"}); got != mine {
		t.Errorf("sender icon: %v, want %s before the topic's", got, mine)
	}
	if got := icon(map[string]any{"message": "c", "icon": own}); got != own {
		t.Errorf("publish icon: %v, want its own", got)
	}
	if got := icon(map[string]any{"message": "d", "live": true}); got != mine {
		t.Errorf("live publish: %v, want the sender icon", got)
	}
	if _, out := raw(t, "POST", ts.URL+"/up", svc, "e", nil); ntfyDecode(t, out).Icon != mine {
		t.Errorf("ntfy publish: %q, want the sender icon", out)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/senders/"+id, admin, map[string]any{"routes": map[string]any{"up": []string{"infra"}}}); code != 200 {
		t.Fatal("route")
	}
	if _, l := call(t, ts, "POST", "/api/v1/notifications", svc, map[string]any{"topic": "up", "message": "f"}); l["topic"] != "infra" || l["icon"] != mine {
		t.Errorf("routed publish: %v, want the sender icon in infra", l)
	}
	call(t, ts, "PUT", "/api/v1/senders/"+id, admin, map[string]any{"routes": map[string]any{}})
	if code, sd := set(""); code != 200 || sd["rules"].(map[string]any)["icon"] != nil {
		t.Fatalf("clear icon: %d %v", code, sd)
	}
	if got := icon(map[string]any{"message": "g"}); got != topic {
		t.Errorf("after clear: %v, want the topic's again", got)
	}
	_, ch := call(t, ts, "GET", "/api/v1/senders/"+id+"/changes", admin, nil)
	last := ch["changes"].([]any)[0].(map[string]any)
	if last["before"].(map[string]any)["icon"] != mine {
		t.Fatalf("the clear is not logged with the icon before: %v", last)
	}
	if code, sd := call(t, ts, "POST", fmt.Sprintf("/api/v1/senders/%s/changes/%v/undo", id, last["seq"]), admin, nil); code != 200 || sd["rules"].(map[string]any)["icon"] != mine {
		t.Fatalf("undo the clear: %d %v", code, sd)
	}
}

func TestSenderRules(t *testing.T) {
	s, ts, admin := newTest(t)
	svc := mkToken(t, s, "svc", []string{ScopePublish}, []string{"up"})
	in := func(topic string) []map[string]any {
		_, l := call(t, ts, "GET", "/api/v1/notifications?state=all&topic="+topic, admin, nil)
		out := []map[string]any{}
		for _, x := range l["notifications"].([]any) {
			out = append(out, x.(map[string]any))
		}
		return out
	}
	pub := func(m map[string]any) {
		t.Helper()
		if code, out := call(t, ts, "POST", "/api/v1/notifications", svc, m); code != 201 && code != 200 {
			t.Fatalf("publish %v: %d %v", m, code, out)
		}
	}
	pub(map[string]any{"topic": "up", "message": "plain"})
	if n := in("up"); len(n) != 1 || n[0]["requested_topic"] != nil || n[0]["silenced"] != false || n[0]["priority"] != 3.0 {
		t.Fatalf("no rules: %v", n)
	}
	var id string
	_, l := call(t, ts, "GET", "/api/v1/senders", admin, nil)
	id = l["senders"].([]any)[0].(map[string]any)["id"].(string)
	set := func(body map[string]any) (int, map[string]any) {
		return call(t, ts, "PUT", "/api/v1/senders/"+id, admin, body)
	}
	limited := mkToken(t, s, "limited", []string{ScopeAdmin}, []string{"up"})
	if code, _ := call(t, ts, "PUT", "/api/v1/senders/"+id, limited, map[string]any{"paused": true}); code != 403 {
		t.Fatalf("topic-limited admin: %d", code)
	}
	if code, _ := set(map[string]any{"priority_min": 4, "priority_max": 2}); code != 400 {
		t.Fatalf("min over max: %d", code)
	}
	if code, _ := set(map[string]any{"routes": map[string]any{"up": []string{"bad topic"}}}); code != 400 {
		t.Fatalf("bad target: %d", code)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/senders/nope", admin, map[string]any{"muted": true}); code != 404 {
		t.Fatalf("unknown sender: %d", code)
	}
	// Route up to two topics the token may not publish to, muted, priority at least 4.
	if code, out := set(map[string]any{"routes": map[string]any{"up": []string{"infra", "ops"}}, "muted": true, "priority_min": 4}); code != 200 ||
		out["rules"].(map[string]any)["muted"] != true {
		t.Fatalf("set rules: %d %v", code, out)
	}
	if _, out := call(t, ts, "POST", "/api/v1/notifications", svc, map[string]any{"topic": "up", "external_id": "k", "message": "routed"}); out["requested_topic"] != "up" {
		t.Fatalf("the sender's reply names its own topic: %v", out)
	}
	for _, tp := range []string{"infra", "ops"} {
		n := in(tp)
		if len(n) != 1 || n[0]["requested_topic"] != "up" || n[0]["silenced"] != true || n[0]["priority"] != 4.0 || n[0]["message"] != "routed" {
			t.Fatalf("%s: %v", tp, n)
		}
	}
	if len(in("up")) != 1 {
		t.Fatal("routed publish also landed in up")
	}
	_, one := call(t, ts, "GET", "/api/v1/senders/"+id, admin, nil)
	if one["count"] != 2.0 || one["topics"].(map[string]any)["up"] != 2.0 {
		t.Fatalf("a fan-out counts once, under the topic named: %v", one)
	}
	pub(map[string]any{"topic": "up", "external_id": "k", "status": "resolved"})
	for _, tp := range []string{"infra", "ops"} {
		if n := in(tp); n[0]["state"] != "resolved" {
			t.Fatalf("resolve in %s: %v", tp, n)
		}
	}
	// A routed upload: each target owns its file.
	if code, out := upload(t, ts, svc, map[string]any{"topic": "up", "message": "file"}, "a.txt", []byte("hello"), false); code != 200 {
		t.Fatalf("upload: %d %v", code, out)
	}
	a, b := attachment(t, in("infra")[0]), attachment(t, in("ops")[0])
	if a["url"] == b["url"] || len(files(t, s)) != 2 {
		t.Fatalf("routed upload: %v %v %v", a, b, files(t, s))
	}
	// A replace of the first copy's row right after it commits deletes the
	// source file; the later copies were already linked (#536).
	routeGap = func(f string) { os.Remove(filepath.Join(s.opt.Attachments, f)) }
	upload(t, ts, svc, map[string]any{"topic": "up", "message": "gap"}, "c.txt", []byte("hi"), false)
	routeGap = nil
	if len(in("ops")) != 3 || len(files(t, s)) != 3 {
		t.Fatalf("copy lost after the source went: ops %d, files %v", len(in("ops")), files(t, s))
	}
	// Paused: every dialect gets a 2xx and nothing is stored, not even the upload.
	if code, _ := set(map[string]any{"paused": true}); code != 200 {
		t.Fatalf("pause: %d", code)
	}
	pub(map[string]any{"topic": "up", "message": "dropped"})
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", svc, map[string]any{"topic": "up", "message": "bad", "priority": 9}); code != 400 {
		t.Fatalf("a bad publish while paused: %d", code)
	}
	if code, out := raw(t, "POST", ts.URL+"/up", svc, "dropped", nil); code != 200 {
		t.Fatalf("ntfy publish while paused: %d %s", code, out)
	}
	if code, _ := upload(t, ts, svc, map[string]any{"topic": "up", "message": "dropped"}, "b.txt", []byte("bye"), false); code != 200 {
		t.Fatalf("upload while paused: %d", code)
	}
	if len(in("infra")) != 3 || len(in("ops")) != 3 || len(in("up")) != 1 || len(files(t, s)) != 3 {
		t.Fatalf("paused publish stored: %v %v", in("infra"), files(t, s))
	}
	if _, one := call(t, ts, "GET", "/api/v1/senders/"+id, admin, nil); one["count"] != 7.0 { // plain, routed, two uploads and three paused; a resolve is no new message
		t.Fatalf("paused publishes still count: %v", one["count"])
	}
	// Changes, newest first; undoing the first puts back no rules at all.
	_, ch := call(t, ts, "GET", "/api/v1/senders/"+id+"/changes", admin, nil)
	list := ch["changes"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["after"].(map[string]any)["paused"] != true {
		t.Fatalf("changes: %v", list)
	}
	firstSeq := list[1].(map[string]any)["seq"].(float64)
	// base_seq (#543): a save based on an older change is refused, unlogged.
	if code, _ := set(map[string]any{"muted": false, "base_seq": firstSeq}); code != 409 {
		t.Fatalf("stale base_seq: %d", code)
	}
	if _, ch := call(t, ts, "GET", "/api/v1/senders/"+id+"/changes", admin, nil); len(ch["changes"].([]any)) != 2 {
		t.Fatalf("a refused save was logged: %v", ch)
	}
	if code, out := set(map[string]any{"paused": true, "base_seq": list[0].(map[string]any)["seq"]}); code != 200 {
		t.Fatalf("current base_seq: %d %v", code, out)
	}
	if code, out := call(t, ts, "POST", fmt.Sprintf("/api/v1/senders/%s/changes/%.0f/undo", id, firstSeq), admin, nil); code != 200 ||
		out["rules"].(map[string]any)["paused"] != false || out["rules"].(map[string]any)["routes"] != nil {
		t.Fatalf("undo: %d %v", code, out)
	}
	pub(map[string]any{"topic": "up", "message": "back"})
	if n := in("up"); len(n) != 2 || n[0]["silenced"] != false {
		t.Fatalf("after undo: %v", n)
	}
	// A token that has not published yet can be set up ahead of its first message.
	ft, fresh, err := s.CreateToken(context.Background(), "fresh", "allowed", []string{ScopePublish}, nil)
	if err != nil {
		t.Fatal(err)
	}
	freshID := ft.ID
	if code, out := call(t, ts, "PUT", "/api/v1/senders/"+freshID, admin, map[string]any{"routes": map[string]any{"*": []string{}}}); code != 200 || out["count"] != 0.0 {
		t.Fatalf("set ahead: %d %v", code, out)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", fresh, map[string]any{"topic": "any", "message": "x"}); code != 201 && code != 200 {
		t.Fatalf("dropped by route: %d", code)
	}
	if len(in("any")) != 0 {
		t.Fatal("an empty route stored the publish")
	}
	// A route target still spends its own topic's allowance.
	if code, _ := call(t, ts, "PUT", "/api/v1/senders/"+freshID, admin, map[string]any{"routes": map[string]any{"*": []string{"r1", "r2"}}}); code != 200 {
		t.Fatalf("route: %d", code)
	}
	s.topicLimit = newLimiter(1, time.Hour)
	s.topicLimit.take("r2", time.Now())
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", fresh, map[string]any{"topic": "any", "message": "y"}); code != 201 && code != 200 {
		t.Fatalf("routed publish: %d", code)
	}
	if len(in("r1")) != 1 || len(in("r2")) != 0 {
		t.Fatalf("full topic took a routed copy: %v", in("r2"))
	}
}

// #119 part 3: a sender with expect set raises one urgent alarm when it goes
// quiet that long; its next publish, or a change to the rule, resolves it.
func TestSenderQuiet(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	svc := mkToken(t, s, "svc", []string{ScopePublish}, []string{"up"})
	pub := func() {
		t.Helper()
		if code, out := call(t, ts, "POST", "/api/v1/notifications", svc, map[string]any{"topic": "up", "message": "hi"}); code != 201 && code != 200 {
			t.Fatalf("publish: %d %v", code, out)
		}
	}
	pub()
	_, l := call(t, ts, "GET", "/api/v1/senders", admin, nil)
	id := l["senders"].([]any)[0].(map[string]any)["id"].(string)
	set := func(body map[string]any) map[string]any {
		t.Helper()
		code, out := call(t, ts, "PUT", "/api/v1/senders/"+id, admin, body)
		if code != 200 {
			t.Fatalf("set %v: %d %v", body, code, out)
		}
		return out
	}
	for _, bad := range []map[string]any{{"expect": "10s"}, {"expect": "daily"}, {"expect": "9000h"}, {"alert_topic": "bad topic"}} {
		if code, out := call(t, ts, "PUT", "/api/v1/senders/"+id, admin, bad); code != 400 {
			t.Errorf("%v: %d %v", bad, code, out)
		}
	}
	sweep := func() {
		t.Helper()
		if err := s.checkins(ctx); err != nil {
			t.Fatal(err)
		}
	}
	quiet := func() { // last publish and last rule change both 2h ago
		t.Helper()
		old := ms(time.Now().Add(-2 * time.Hour))
		if _, err := s.db.Exec(`UPDATE senders SET last_seen = ?, alarm_from = ? WHERE id = ?`, old, old, id); err != nil {
			t.Fatal(err)
		}
	}
	alarms := func(topic string) (open []map[string]any) {
		t.Helper()
		_, out := call(t, ts, "GET", "/api/v1/notifications?state=all&topic="+topic, admin, nil)
		for _, n := range out["notifications"].([]any) {
			if n := n.(map[string]any); n["external_id"] == quietPrefix+id && n["state"] != "resolved" {
				open = append(open, n)
			}
		}
		return open
	}

	// No expect: quiet for hours, nothing.
	quiet()
	sweep()
	if a := alarms("up"); len(a) != 0 {
		t.Fatalf("alarm without expect: %v", a)
	}
	if out := set(map[string]any{"expect": "1h"}); out["rules"].(map[string]any)["expect"] != "1h" {
		t.Fatalf("expect not kept: %v", out)
	}
	sweep() // the rule is new: the wait starts now
	if a := alarms("up"); len(a) != 0 {
		t.Fatalf("alarm right after setting expect: %v", a)
	}
	if _, err := s.db.Exec(`UPDATE senders SET last_topic = '' WHERE id = ?`, id); err != nil { // as if last heard before the upgrade
		t.Fatal(err)
	}
	quiet()
	sweep()
	a := alarms("up")
	if len(a) != 1 || a[0]["priority"] != 5.0 || !strings.HasSuffix(a[0]["title"].(string), "svc went quiet") {
		t.Fatalf("quiet sender raised: %v", a)
	}
	before := s.seq.Load()
	sweep() // one alarm at a time
	if s.seq.Load() != before {
		t.Fatalf("second sweep changed something")
	}
	if _, out := call(t, ts, "GET", "/api/v1/senders/"+id, admin, nil); out["alarm"] != "up" {
		t.Fatalf("sender alarm: %v", out["alarm"])
	}
	if code, out := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "up", "external_id": quietPrefix + "x"}); code != 400 {
		t.Fatalf("reserved external_id: %d %v", code, out)
	}
	// A dropped publish counts as speaking too.
	set(map[string]any{"paused": true})
	if a := alarms("up"); len(a) != 1 {
		t.Fatalf("pause alone resolved the alarm: %v", a)
	}
	pub()
	if a := alarms("up"); len(a) != 0 {
		t.Fatalf("publish did not resolve the alarm: %v", a)
	}
	if _, out := call(t, ts, "GET", "/api/v1/senders/"+id, admin, nil); out["alarm"] != nil {
		t.Fatalf("sender alarm after publish: %v", out["alarm"])
	}
	// Quiet again; moving the alarm to another topic resolves it and waits anew.
	set(map[string]any{"paused": false})
	quiet()
	sweep()
	if len(alarms("up")) != 1 {
		t.Fatalf("not raised again")
	}
	set(map[string]any{"alert_topic": "ops"})
	if a := alarms("up"); len(a) != 0 {
		t.Fatalf("rule change left the alarm open: %v", a)
	}
	sweep()
	if a := alarms("ops"); len(a) != 0 {
		t.Fatalf("raised before a full wait after the change: %v", a)
	}
	quiet()
	sweep()
	if a := alarms("ops"); len(a) != 1 {
		t.Fatalf("alert_topic ignored: %v", a)
	}
	// A revoked sender never speaks again: its alarm ends with it (#558).
	if err := s.DeleteToken(ctx, id); err != nil {
		t.Fatal(err)
	}
	if a := alarms("ops"); len(a) != 0 {
		t.Fatalf("revoked sender left its alarm open: %v", a)
	}
}
