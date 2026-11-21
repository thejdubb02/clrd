package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func gotifyPost(t *testing.T, target, ctype, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", target, strings.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	code, b := send(t, req)
	out := map[string]any{}
	json.Unmarshal([]byte(b), &out)
	return code, out
}

func latest(t *testing.T, ts string, admin, topic string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", ts+"/api/v1/notifications?topic="+topic, nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	_, b := send(t, req)
	var out struct{ Notifications []map[string]any }
	json.Unmarshal([]byte(b), &out)
	if len(out.Notifications) == 0 {
		return nil
	}
	return out.Notifications[0]
}

func TestGotifyPublish(t *testing.T) {
	s, ts, admin := newTest(t)
	app := mkToken(t, s, "Sonarr TV", []string{ScopePublish}, []string{"tv"})

	// JSON with the header, the way the gotify CLI and Apprise send it.
	body := `{"title":"Grabbed","message":"**S01E02**","priority":8,"extras":{"client::display":{"contentType":"text/markdown"},"client::notification":{"click":{"url":"https://example.com/ep"},"bigImageUrl":"https://example.com/p.png"}}}`
	code, m := gotifyPost(t, ts.URL+"/message", "application/json", body, map[string]string{"X-Gotify-Key": app})
	if code != 200 || m["title"] != "Grabbed" || m["priority"] != 8.0 || m["id"] == nil || m["date"] == nil || m["extras"] == nil {
		t.Fatalf("json publish: %d %v", code, m)
	}
	n := latest(t, ts.URL, admin, "tv")
	att, _ := n["attachment"].(map[string]any)
	if n["title"] != "Grabbed" || n["priority"] != 4.0 || n["format"] != "markdown" || n["click"] != "https://example.com/ep" || att["url"] != "https://example.com/p.png" {
		t.Fatalf("stored: %v", n)
	}

	// A form with ?token= and no title: plain text, titled with the app name.
	code, m = gotifyPost(t, ts.URL+"/message?token="+app, "application/x-www-form-urlencoded", url.Values{"message": {"*hi*"}, "priority": {"0"}}.Encode(), nil)
	if code != 200 || m["title"] != "Sonarr TV" {
		t.Fatalf("form publish: %d %v", code, m)
	}
	if n = latest(t, ts.URL, admin, "tv"); n["message"] != "*hi*" || n["format"] != "text" || n["priority"] != 1.0 {
		t.Fatalf("form stored: %v", n)
	}

	// Multipart, as curl -F sends it.
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	mw.WriteField("message", "from curl -F")
	mw.WriteField("title", "mp")
	mw.Close()
	if code, m = gotifyPost(t, ts.URL+"/message", mw.FormDataContentType(), mp.String(), map[string]string{"X-Gotify-Key": app}); code != 200 || m["title"] != "mp" {
		t.Fatalf("multipart publish: %d %v", code, m)
	}

	if code, m = gotifyPost(t, ts.URL+"/message", "application/json", `{"title":"x"}`, map[string]string{"X-Gotify-Key": app}); code != 400 || m["errorCode"] != 400.0 {
		t.Fatalf("no message: %d %v", code, m)
	}
}

// appTokenChecks: an app-style compat route refuses a read-only token (403)
// and a token that does not name exactly one topic (400).
func appTokenChecks(t *testing.T, s *Server, admin string, post func(string) (int, map[string]any)) {
	t.Helper()
	if code, _ := post(mkToken(t, s, "reader", []string{ScopeRead}, []string{"tv"})); code != 403 {
		t.Fatalf("read-only token: %d", code)
	}
	if code, _ := post(admin); code != 400 {
		t.Fatalf("token for every topic: %d", code)
	}
	if code, _ := post(mkToken(t, s, "two", []string{ScopePublish}, []string{"a", "b"})); code != 400 {
		t.Fatalf("token for two topics: %d", code)
	}
}

// The token picks the topic, and every check the native API makes still holds.
func TestGotifyAuth(t *testing.T) {
	s, ts, admin := newTest(t)
	before := s.seq.Load()
	post := func(key string) (int, map[string]any) {
		return gotifyPost(t, ts.URL+"/message", "application/json", `{"message":"m"}`, map[string]string{"X-Gotify-Key": key})
	}
	if code, m := post("Abadtoken"); code != 401 || m["error"] != "Unauthorized" || m["errorCode"] != 401.0 {
		t.Fatalf("bad key: %d %v", code, m)
	}
	appTokenChecks(t, s, admin, post)
	_, wild, err := s.createToken(context.Background(), &Token{Name: "g", Scopes: []string{ScopeRead, ScopePublish}, Grants: []Grant{{Topic: "*", Scopes: []string{ScopeRead, ScopePublish}}}})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := post(wild); code != 400 {
		t.Fatalf("wildcard grant: %d", code)
	}
	// An empty key is a wrong key, not the ntfy path, even where anonymous
	// senders may publish to "message".
	anon := &Token{ID: anonymousID, Name: "anonymous", Scopes: []string{ScopePublish}, Grants: []Grant{{Topic: "message", Scopes: []string{ScopePublish}}}, CreatedAt: now()}
	if err := insertToken(context.Background(), s.db, anon, hashToken(tokenPrefix+randomString(32))); err != nil {
		t.Fatal(err)
	}
	if code, _ := gotifyPost(t, ts.URL+"/message?token=", "application/json", `{"message":"m"}`, nil); code != 401 {
		t.Fatalf("empty ?token=: %d", code)
	}
	if s.seq.Load() != before {
		t.Fatal("a refused Gotify publish stored something")
	}

	// A rate-limited sender is told when to retry.
	s.tokLimit = newLimiter(1, time.Hour)
	tv := mkToken(t, s, "tv", []string{ScopePublish}, []string{"tv"})
	post(tv)
	req, _ := http.NewRequest("POST", ts.URL+"/message?token="+tv, strings.NewReader(`{"message":"m"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatalf("rate limited: %d Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	s.tokLimit = nil

	// One publish grant among read grants: that topic.
	_, one, err := s.createToken(context.Background(), &Token{Name: "g", Scopes: []string{ScopeRead, ScopePublish}, Grants: []Grant{{Topic: "logs", Scopes: []string{ScopeRead}}, {Topic: "tv", Scopes: []string{ScopePublish}}}})
	if err != nil {
		t.Fatal(err)
	}
	if code, m := post(one); code != 200 || latest(t, ts.URL, admin, "tv") == nil {
		t.Fatalf("one publish grant: %d %v", code, m)
	}

	// Without a Gotify key, POST /message is still ntfy's topic "message".
	code, b := raw(t, "POST", ts.URL+"/message", admin, "plain ntfy", nil)
	if code != 200 || ntfyDecode(t, b).Topic != "message" {
		t.Fatalf("ntfy fallthrough: %d %s", code, b)
	}
}

// A Gotify app key: Gotify's shape, one topic, only on POST /message.
func TestGotifyApps(t *testing.T) {
	s, ts, admin := newTest(t)
	code, app := call(t, ts, "POST", "/api/v1/gotify/apps", admin, map[string]any{"name": "Watchtower", "topic": "tv", "priority": 8})
	key, _ := app["token"].(string)
	if code != 200 || len(key) != 15 || key[0] != 'A' || app["id"] == nil || app["token_id"] == nil {
		t.Fatalf("create: %d %v", code, app)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/gotify/apps", mkToken(t, s, "p", []string{ScopePublish}, nil), map[string]any{"name": "x", "topic": "tv"}); code != 403 {
		t.Fatalf("create without admin: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/gotify/apps", admin, map[string]any{"name": "x", "topic": "a*"}); code != 400 {
		t.Fatalf("wildcard topic: %d", code)
	}

	// No priority in the message: the app's default. The appid is the app's.
	code, m := gotifyPost(t, ts.URL+"/message", "application/json", `{"message":"updated"}`, map[string]string{"X-Gotify-Key": key})
	if code != 200 || m["appid"] != app["id"] || m["priority"] != 8.0 || m["title"] != "Watchtower" {
		t.Fatalf("publish: %d %v", code, m)
	}
	if n := latest(t, ts.URL, admin, "tv"); n["priority"] != 4.0 {
		t.Fatalf("stored: %v", n)
	}
	// Bearer, as some Gotify senders send it.
	if code, m = gotifyPost(t, ts.URL+"/message", "application/json", `{"message":"b","priority":2}`, map[string]string{"Authorization": "Bearer " + key}); code != 200 || m["priority"] != 2.0 {
		t.Fatalf("bearer: %d %v", code, m)
	}
	if code, _ := call(t, ts, "POST", "/api/v1/gotify/apps", admin, map[string]any{"name": "x", "topic": "tv", "priority": 11}); code != 400 {
		t.Fatalf("priority 11: %d", code)
	}
	if code, _ = gotifyPost(t, ts.URL+"/message", "application/json", `{"message":"b"}`, map[string]string{"Authorization": "bearer  " + key}); code != 200 {
		t.Fatalf("lower-case bearer: %d", code)
	}
	if code, m = gotifyPost(t, ts.URL+"/message", "application/json", `{"message":"b"}`, map[string]string{"Authorization": "Bearer Awrongwrongwron"}); code != 401 || m["errorCode"] != 401.0 {
		t.Fatalf("wrong bearer: %d %v", code, m)
	}
	// An app with no default leaves clrd's.
	_, two := call(t, ts, "POST", "/api/v1/gotify/apps", admin, map[string]any{"name": "Two", "topic": "two"})
	if two["id"] == app["id"] {
		t.Fatalf("same appid: %v %v", app, two)
	}
	gotifyPost(t, ts.URL+"/message?token="+two["token"].(string), "application/json", `{"message":"x"}`, nil)
	if n := latest(t, ts.URL, admin, "two"); n["priority"] != 3.0 {
		t.Fatalf("no default: %v", n)
	}

	// A clrd token as Bearer, spaced oddly, is still the ntfy path.
	if code, m = gotifyPost(t, ts.URL+"/message", "text/plain", "plain", map[string]string{"Authorization": "Bearer  " + admin}); code != 200 || m["topic"] != "message" {
		t.Fatalf("clrd bearer: %d %v", code, m)
	}

	// The key works nowhere else.
	if code, _ := call(t, ts, "GET", "/api/v1/notifications?topic=tv", key, nil); code != 401 {
		t.Fatalf("native API: %d", code)
	}
	if code, _ := raw(t, "PUT", ts.URL+"/tv", key, "x", nil); code != 401 {
		t.Fatalf("ntfy publish: %d", code)
	}
	// Revoking the token kills the key.
	if code, _ := call(t, ts, "DELETE", "/api/v1/tokens/"+app["token_id"].(string), admin, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := gotifyPost(t, ts.URL+"/message", "application/json", `{"message":"m"}`, map[string]string{"X-Gotify-Key": key}); code != 401 {
		t.Fatalf("revoked key: %d", code)
	}
}

func TestGotifyPriority(t *testing.T) {
	for in, want := range map[int]int{-1: 1, 0: 1, 1: 2, 3: 2, 4: 3, 5: 3, 7: 3, 8: 4, 10: 4, 99: 4} {
		if got := gotifyPriority(in); got != want {
			t.Errorf("gotifyPriority(%d) = %d, want %d", in, got, want)
		}
	}
}
