package server

import (
	"context"
	"fmt"
	"testing"
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
}

// #119: a sender's rules route, mute, clamp and pause what it publishes;
// every change is kept and can be undone; no rules is today's behaviour.
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
	pub(map[string]any{"topic": "up", "external_id": "k", "message": "routed"})
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
	// Paused: every dialect gets a 2xx and nothing is stored, not even the upload.
	if code, _ := set(map[string]any{"paused": true}); code != 200 {
		t.Fatalf("pause: %d", code)
	}
	pub(map[string]any{"topic": "up", "message": "dropped"})
	if code, out := raw(t, "POST", ts.URL+"/up", svc, "dropped", nil); code != 200 {
		t.Fatalf("ntfy publish while paused: %d %s", code, out)
	}
	if code, _ := upload(t, ts, svc, map[string]any{"topic": "up", "message": "dropped"}, "b.txt", []byte("bye"), false); code != 200 {
		t.Fatalf("upload while paused: %d", code)
	}
	if len(in("infra")) != 2 || len(in("ops")) != 2 || len(in("up")) != 1 || len(files(t, s)) != 2 {
		t.Fatalf("paused publish stored: %v %v", in("infra"), files(t, s))
	}
	if _, one := call(t, ts, "GET", "/api/v1/senders/"+id, admin, nil); one["count"] != 6.0 { // plain, routed, upload and three paused; a resolve is no new message
		t.Fatalf("paused publishes still count: %v", one["count"])
	}
	// Changes, newest first; undoing the first puts back no rules at all.
	_, ch := call(t, ts, "GET", "/api/v1/senders/"+id+"/changes", admin, nil)
	list := ch["changes"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["after"].(map[string]any)["paused"] != true {
		t.Fatalf("changes: %v", list)
	}
	firstSeq := list[1].(map[string]any)["seq"].(float64)
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
}
