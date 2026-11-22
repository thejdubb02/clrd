package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// #401: ntfy's http and broadcast buttons, behind an admin-only topic switch.
func TestDeviceActions(t *testing.T) {
	s, ts, admin := newTest(t)
	pub := mkToken(t, s, "pub", []string{ScopePublish, ScopeRead}, nil)
	hook := map[string]any{"label": "Off", "http": map[string]any{"method": "put", "url": "https://example.com/off",
		"headers": map[string]string{"Authorization": "Bearer x"}, "body": `{"on":false}`}, "clear": true}
	native := func(tok string, acts ...any) (int, map[string]any) {
		return call(t, ts, "POST", "/api/v1/notifications", tok, map[string]any{"topic": "home", "title": "t", "actions": acts})
	}
	pollNtfy := func() []ntfyMsg {
		_, out := raw(t, "GET", ts.URL+"/home/json?poll=1", admin, "", nil)
		var ms []ntfyMsg
		for _, l := range lines(out) {
			if l != "" {
				ms = append(ms, ntfyDecode(t, l))
			}
		}
		return ms
	}

	// Off by default: the native API refuses, a ntfy publish drops them.
	if code, out := native(admin, hook); code != 403 || !strings.Contains(out["error"].(string), "device_actions") {
		t.Fatalf("native publish with the switch off: %d %v", code, out)
	}
	code, out := raw(t, "PUT", ts.URL+"/home", pub, "x", map[string]string{"Actions": "view, Open, https://example.com/o; http, Off, https://example.com/off"})
	if m := ntfyDecode(t, out); code != 200 || len(m.Actions) != 1 || m.Actions[0].Action != "view" {
		t.Fatalf("ntfy publish with the switch off keeps only view: %d %s", code, out)
	}

	// Only an admin turns it on; a mixed body from a publisher changes nothing.
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/home", pub, map[string]any{"device_actions": true, "icon": "https://example.com/i.png"}); code != 403 {
		t.Fatalf("publisher set device_actions: %d", code)
	}
	var rows int
	if s.db.QueryRow(`SELECT COUNT(*) FROM topics WHERE topic = 'home'`).Scan(&rows); rows != 0 {
		t.Fatal("a refused PUT wrote the topic row")
	}
	if code, out := call(t, ts, "PUT", "/api/v1/topics/home", admin, map[string]any{"device_actions": true}); code != 200 || out["device_actions"] != true {
		t.Fatalf("admin turns device_actions on: %d %v", code, out)
	}

	// On: the native API stores them; a ntfy subscriber gets them whole.
	if code, out := native(pub, hook, map[string]any{"label": "Ping", "broadcast": map[string]any{"intent": "io.x.PING", "extras": map[string]string{"k": "v"}}}); code != 200 {
		t.Fatalf("native publish with the switch on: %d %v", code, out)
	}
	raw(t, "PUT", ts.URL+"/home", pub, "y", map[string]string{"Actions": `http, Off, https://example.com/off, method=PUT, headers.Authorization=Bearer x, body='{"on":false}', clear=true`})
	raw(t, "POST", ts.URL+"/", pub, `{"topic":"home","message":"z","actions":[{"action":"http","label":"Off","url":"https://example.com/off","method":"PUT","headers":{"Authorization":"Bearer x"},"body":"{\"on\":false}","clear":true}]}`, nil)
	ms := pollNtfy()
	if len(ms) != 4 {
		t.Fatalf("want 4 messages, got %d", len(ms))
	}
	for i, m := range ms[1:] {
		a := m.Actions[0]
		if a.Action != "http" || a.Method != "PUT" || a.URL != "https://example.com/off" || a.Headers["Authorization"] != "Bearer x" ||
			a.Body != `{"on":false}` || !a.Clear {
			t.Fatalf("message %d http action: %+v", i+1, a)
		}
	}
	if b := ms[1].Actions[1]; b.Action != "broadcast" || b.Intent != "io.x.PING" || b.Extras["k"] != "v" {
		t.Fatalf("broadcast: %+v", b)
	}

	// Clearing the icon keeps the switch; turning it off keeps what was stored.
	call(t, ts, "PUT", "/api/v1/topics/home", admin, map[string]any{"icon": ""})
	if code, _ := native(pub, hook); code != 200 {
		t.Fatalf("clearing the icon turned device_actions off: %d", code)
	}
	call(t, ts, "PUT", "/api/v1/topics/home", admin, map[string]any{"device_actions": false})
	if code, _ := native(pub, hook); code != 403 {
		t.Fatalf("switch off still takes http: %d", code)
	}
	if ms := pollNtfy(); len(ms) != 5 || len(ms[4].Actions) != 1 || ms[4].Actions[0].Action != "http" {
		t.Fatalf("turning the switch off dropped stored actions: %+v", ms)
	}
}

func TestDeviceActionChecks(t *testing.T) {
	ok := Action{Label: "x", HTTP: &HTTPAction{URL: "https://example.com"}}
	if err := checkDeviceAction(ok); err != nil || ok.HTTP.Method != "POST" {
		t.Fatalf("empty method is POST: %v %+v", err, ok.HTTP)
	}
	for name, a := range map[string]Action{
		"method":       {HTTP: &HTTPAction{Method: "CONNECT", URL: "https://example.com"}},
		"scheme":       {HTTP: &HTTPAction{URL: "file:///etc/passwd"}},
		"no host":      {HTTP: &HTTPAction{URL: "https://:443/run"}},
		"get body":     {Label: "x", HTTP: &HTTPAction{Method: "GET", URL: "https://example.com", Body: "b"}},
		"no label":     {Broadcast: &BroadcastAction{}},
		"header name":  {HTTP: &HTTPAction{URL: "https://example.com", Headers: map[string]string{"X Bad": "v"}}},
		"header value": {HTTP: &HTTPAction{URL: "https://example.com", Headers: map[string]string{"X-A": "v\r\nX-B: w"}}},
		"body":         {HTTP: &HTTPAction{URL: "https://example.com", Body: strings.Repeat("b", 4097)}},
		"intent":       {Broadcast: &BroadcastAction{Intent: "a b"}},
		"extra":        {Broadcast: &BroadcastAction{Extras: map[string]string{"a b": "v"}}},
	} {
		if checkDeviceAction(a) == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// Exactly one kind per action.
	if err := checkActions([]Action{{Label: "x", URL: "https://example.com", HTTP: ok.HTTP}}); err == nil {
		t.Error("url and http together: want an error")
	}
	four := Action{Label: "b", Broadcast: &BroadcastAction{}}
	if err := checkActions([]Action{four, four, four}); err != nil {
		t.Errorf("3 broadcasts: %v", err)
	}
	if err := checkActions([]Action{four, four, four, four}); err == nil {
		t.Error("4 broadcasts: want an error")
	}
	// view and reply JSON stays as it was before #401.
	b, _ := json.Marshal([]Action{{Label: "Open", URL: "https://example.com"}, {Label: "Yes", Reply: "yes"}})
	if string(b) != `[{"label":"Open","url":"https://example.com"},{"label":"Yes","url":"","reply":"yes"}]` {
		t.Fatalf("view/reply JSON changed: %s", b)
	}
}

func TestNtfyShortActions(t *testing.T) {
	as, err := ntfyActions(`view, Don't, https://example.com/a; broadcast, Ping, extras.cmd=go, intent=io.x.P; copy, C, x`)
	if err != nil || len(as) != 2 || as[0].Label != "Don't" || as[1].Broadcast == nil || as[1].Broadcast.Extras["cmd"] != "go" ||
		as[1].Broadcast.Intent != "io.x.P" {
		t.Fatalf("short form: %v %+v", err, as)
	}
	// A named field keeps the rest at their own index; a quote may follow "= ".
	as, _ = ntfyActions(`action=http, Close door, https://example.com/c, body= 'a,b'`)
	if len(as) != 1 || as[0].Label != "Close door" || as[0].HTTP.URL != "https://example.com/c" || as[0].HTTP.Body != "a,b" {
		t.Fatalf("named then positional: %+v", as)
	}
	// #446: \" inside quotes is a quote, and quoted spaces stay; unquoted ones go.
	as, _ = ntfyActions(`http, " Open " , https://example.com/o, body="a\",b" , headers.X = ' v ', method= POST `)
	if h := as[0].HTTP; len(as) != 1 || as[0].Label != " Open " || h.Body != `a",b` || h.Headers["X"] != " v " || h.Method != "POST" {
		t.Fatalf("escaped quote and quoted spaces: %+v %+v", as, as[0].HTTP)
	}
	if as, _ = ntfyActions(`view, 'it\'s', https://example.com, clear=\x`); as[0].Label != "it's" {
		t.Fatalf("escaped single quote: %+v", as)
	}
	// Only the first "=" is the key's; a value or label may hold more.
	as, _ = ntfyActions(`http, "Show x=y", https://example.com/q?a=b, body=t= v`)
	if len(as) != 1 || as[0].Label != "Show x=y" || as[0].HTTP.URL != "https://example.com/q?a=b" || as[0].HTTP.Body != "t= v" {
		t.Fatalf("= inside values: %+v", as)
	}
	if _, err := ntfyActions(`http, X, https://example.com, body='open`); err == nil {
		t.Fatal("unclosed quote: want an error")
	}
}
