package server

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNtfyAnonymous(t *testing.T) {
	s, ts, admin := newTest(t)
	set := func(tok string, grants any) (int, map[string]any) {
		return call(t, ts, "PUT", "/api/v1/ntfy/anonymous", tok, map[string]any{"grants": grants})
	}
	if c, m := call(t, ts, "GET", "/api/v1/ntfy/anonymous", admin, nil); c != 200 || m["enabled"] != false {
		t.Fatalf("default: %d %v", c, m)
	}
	if c, _ := bearer(t, "POST", ts.URL+"/up1", "", "hi"); c == 200 {
		t.Fatalf("anonymous publish while off: %d", c)
	}
	if c, _ := set(mkToken(t, s, "p", []string{ScopePublish, ScopeRead}, nil), []Grant{{Topic: "*", Scopes: []string{ScopePublish}}}); c != 403 {
		t.Fatalf("set without admin: %d", c)
	}
	if c, _ := set(admin, []Grant{{Topic: "*", Scopes: []string{ScopeAdmin}}}); c != 400 {
		t.Fatalf("admin grant: %d", c)
	}

	// UnifiedPush style: anyone may publish to up*, nothing else.
	if c, m := set(admin, []Grant{{Topic: "up*", Scopes: []string{ScopePublish}}}); c != 200 || m["enabled"] != true {
		t.Fatalf("set: %d %v", c, m)
	}
	if c, _ := bearer(t, "POST", ts.URL+"/up1", "", "hi"); c != 200 {
		t.Errorf("anonymous publish up1: %d", c)
	}
	if c, _ := bearer(t, "POST", ts.URL+"/other", "", "hi"); c != 403 {
		t.Errorf("anonymous publish other: %d", c)
	}
	if c, _ := bearer(t, "GET", ts.URL+"/up1/json?poll=1", "", ""); c != 403 {
		t.Errorf("anonymous read up1: %d", c)
	}
	if c, _ := bearer(t, "GET", ts.URL+"/api/v1/notifications", "", ""); c != 401 {
		t.Errorf("anonymous native: %d", c)
	}

	// A revoke, then on again: the new rules work on a stream, and a change
	// closes it so it reconnects under them.
	if c, _ := call(t, ts, "PUT", "/api/v1/ntfy/anonymous", admin, map[string]any{"grant": []Grant{}}); c != 400 {
		t.Fatalf("typo in grants: %d", c)
	}
	_, m := call(t, ts, "GET", "/api/v1/ntfy/anonymous", admin, nil)
	if m["enabled"] != true {
		t.Fatalf("typo turned it off: %v", m)
	}
	if c, _ := call(t, ts, "DELETE", "/api/v1/tokens/"+m["token_id"].(string), admin, nil); c != 204 {
		t.Fatalf("revoke: %d", c)
	}
	if c, _ := set(admin, []Grant{{Topic: "pub", Scopes: []string{ScopeRead}}}); c != 200 {
		t.Fatalf("set again: %d", c)
	}
	res, err := http.Get(ts.URL + "/pub/json")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("anonymous stream: %v %v", err, res)
	}
	defer res.Body.Close()
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	next := func() (string, bool) {
		select {
		case l, ok := <-lines:
			return l, ok
		case <-time.After(5 * time.Second):
			t.Fatal("stream: timed out")
			return "", false
		}
	}
	next() // open
	if c, _ := bearer(t, "POST", ts.URL+"/pub", admin, "hello"); c != 200 {
		t.Fatalf("publish: %d", c)
	}
	if l, _ := next(); !strings.Contains(l, "hello") {
		t.Fatalf("stream after re-enable: %q", l)
	}
	if c, m := set(admin, []Grant{}); c != 200 || m["enabled"] != false {
		t.Fatalf("off: %d %v", c, m)
	}
	for l, ok := next(); ok; l, ok = next() {
		if strings.Contains(l, `"message"`) {
			t.Fatalf("stream still open after off: %q", l)
		}
	}
	if c, _ := bearer(t, "POST", ts.URL+"/up1", "", "hi"); c == 200 {
		t.Errorf("anonymous publish after off: %d", c)
	}
}
