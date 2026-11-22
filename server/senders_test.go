package server

import (
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
