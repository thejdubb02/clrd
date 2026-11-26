package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thejdubb02/clrd/server"
)

// TestWebOnRealServer drives the mounted pages the way a browser does:
// sign in with a token, see a notification, dismiss it, see it move.
func TestWebOnRealServer(t *testing.T) {
	s, ts, raw := realServer(t)
	c := browser()
	get := func(path string) (int, string) {
		t.Helper()
		code, body, _ := fetch(t, c, ts.URL+path)
		return code, body
	}
	for _, p := range []string{"/", "/docs/", "/favicon.svg", "/robots.txt", "/sitemap.xml"} {
		if code, _ := get(p); code != 200 {
			t.Fatalf("GET %s: %d", p, code)
		}
	}
	publish(t, ts, raw, "home", "disk full")
	publish(t, ts, raw, "work", "deploy done")
	if code, body := get("/app"); code != 401 || strings.Contains(body, "disk full") {
		t.Fatalf("/app signed out: %d", code)
	}

	// Sign-in mints a token for this browser, named in the form, so that is
	// the name it acts as. Sign in through the form the page renders, first
	// with a bad token.
	_, page := get("/app?topic=home")
	if code, body := submit(t, c, ts.URL, page, "clrd_wrong", "kitchen tablet"); code != 401 || !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `value="/app?topic=home"`) || !strings.Contains(body, `value="kitchen tablet"`) {
		t.Fatalf("bad token: %d, want 401 with the error shown, next kept without error= and the typed name kept", code)
	}
	if _, body := get("/app?topic=home"); strings.Contains(body, `role="alert"`) {
		t.Fatal("error shown before any sign-in attempt")
	}
	if code, body := submit(t, c, ts.URL, page, raw, "kitchen tablet"); code != 200 || !strings.Contains(body, "disk full") || !strings.Contains(body, `data-me="kitchen tablet"`) {
		t.Fatalf("good token: %d, want the inbox with this device's name", code)
	}
	for path, want := range map[string]string{"/app": "disk full", "/app?topic=home": "disk full", "/app?topic=work": "deploy done", "/app?q=deploy": "deploy done"} {
		other := map[string]string{"disk full": "deploy done", "deploy done": "disk full"}[want]
		if _, body := get(path); !strings.Contains(body, want) || (path != "/app" && strings.Contains(body, other)) {
			t.Errorf("%s: want %q and not %q", path, want, other)
		}
	}
	if _, body := get("/app/settings"); !strings.Contains(body, "<span>kitchen tablet</span>") {
		t.Fatal("settings misses the device")
	}
	if _, body := get("/app/cleared"); strings.Contains(body, "disk full") {
		t.Fatal("cleared shows an active notification")
	}
	list, _ := s.List(context.Background(), mustTok(t, s, raw), server.ListQuery{State: "active", Topics: []string{"home"}})
	// The page offers Undo only when the answer says this device cleared it.
	if n := act(t, c, ts.URL, list[0].ID, "dismiss"); n.State != "dismissed" || n.StateBy != "kitchen tablet" {
		t.Fatalf("dismiss answered %+v, want dismissed by this browser", n)
	}
	if _, body := get("/app"); strings.Contains(body, "disk full") {
		t.Fatal("inbox still shows a dismissed notification")
	}
	if _, body := get("/app/cleared"); !strings.Contains(body, "disk full") {
		t.Fatal("cleared misses the dismissed notification")
	}
	// Stats (#120): the server's numbers, with the sender shown by name.
	if code, body := get("/app/stats?days=1"); code != 200 || !strings.Contains(body, "data-total>2</strong>") || !strings.Contains(body, ">phone</td>") {
		t.Fatalf("stats: %d, want both notifications counted and sent by phone", code)
	}
}

// The link shows the card where no list does (#822): past the newest 500 of
// its state in its topic, and cleared on this device alone (#767).
func TestNotificationLinkUnlisted(t *testing.T) {
	s, ts, raw := realServer(t)
	ctx, tok := context.Background(), mustTok(t, s, raw)
	get := func(path string) (int, string) { // as this token, whose clears stay here
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		code, body, _ := send(t, http.DefaultClient, req)
		return code, body
	}
	ids := make([]string, 502)
	for i := range ids {
		n, err := s.Publish(ctx, tok, server.PublishInput{Topic: "home", Message: "m"})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = n.ID
	}
	if _, err := s.SetClears(ctx, tok, tok.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Act(ctx, tok, ids[501], "dismiss", "api", nil); err != nil {
		t.Fatal(err)
	}
	for _, k := range []struct{ id, list, state string }{{ids[0], "/app", "active"}, {ids[501], "/app/cleared", "dismissed"}} {
		if _, body := get(k.list + "?topic=home"); strings.Contains(body, `id="t-`+k.id+`"`) {
			t.Fatalf("%s lists %s, so this test proves nothing", k.list, k.state)
		}
		code, body := get("/app/n/" + k.id)
		if code != 200 || !strings.Contains(body, `id="t-`+k.id+`"`) || !strings.Contains(body, `href="`+k.list+`?topic=home#t-`+k.id+`" data-back`) {
			t.Errorf("%s: %d, want its card linking back to %s", k.state, code, k.list)
		}
	}
}

// The sidebar and the API count every row, past the 500 the page lists.
func TestExactCounts(t *testing.T) {
	s, ts, raw := realServer(t)
	ctx, tok := context.Background(), mustTok(t, s, raw)
	for i := range 503 {
		n, err := s.Publish(ctx, tok, server.PublishInput{Topic: "home", Message: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if i > 1 {
			if _, err := s.Act(ctx, tok, n.ID, "dismiss", "api", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, body, _ := fetch(t, browser(), ts.URL+"/app/cleared?auth="+base64.RawURLEncoding.EncodeToString([]byte("Bearer "+raw)))
	for _, want := range []string{`Inbox <span class="count">2</span>`, `Cleared <span class="count">501</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("page misses %s", want)
		}
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/counts", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var c map[string]int
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil || c["dismissed"] != 501 || c["cleared"] != 501 || c["inbox"] != 2 || c["active"] != 2 {
		t.Fatalf("api counts: %v %v", err, c)
	}
}

func mustTok(t *testing.T, s *server.Server, raw string) *server.Token {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	tok := s.Authenticate(r)
	if tok == nil {
		t.Fatal("token not accepted")
	}
	return tok
}

// A link with ?auth= signs the browser in and lands on the same page without
// the token; the cookie it leaves can act, which ?auth= alone could not.
func TestSignInLink(t *testing.T) {
	_, ts, raw := realServer(t)
	publish(t, ts, raw, "home", "disk full")
	enc := base64.RawURLEncoding.EncodeToString
	for name, auth := range map[string]string{"bearer": enc([]byte("Bearer " + raw)), "basic": enc([]byte("Basic " + base64.StdEncoding.EncodeToString([]byte(":"+raw))))} {
		c := browser()
		code, body, u := fetch(t, c, ts.URL+"/app/?topic=home&auth="+auth)
		if code != 200 || u.RequestURI() != "/app/?topic=home" || !strings.Contains(body, "disk full") {
			t.Fatalf("%s: %d at %s, want the inbox at /app/?topic=home", name, code, u.RequestURI())
		}
		id := regexp.MustCompile(`data-id="([^"]+)"`).FindStringSubmatch(body)[1]
		if n := act(t, c, ts.URL, id, "ack"); n.State != "acknowledged" {
			t.Fatalf("%s: cookie from the link cannot act: %+v", name, n)
		}
	}
	// From another site the link only drops the token: no session, sign-in page.
	req, _ := http.NewRequest("GET", ts.URL+"/app?topic=home&auth="+enc([]byte("Bearer "+raw)), nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	c := browser()
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 || res.Request.URL.RequestURI() != "/app?topic=home" {
		t.Errorf("cross-site link: %d at %s, want the sign-in page without the token", res.StatusCode, res.Request.URL.RequestURI())
	}
	for _, auth := range []string{enc([]byte("Bearer clrd_wrong")), "not base64!"} {
		code, body, u := fetch(t, browser(), ts.URL+"/app?auth="+url.QueryEscape(auth))
		if code != 401 || u.RequestURI() != "/app?error=token" || !strings.Contains(body, `role="alert"`) {
			t.Errorf("auth=%q: %d at %s, want the sign-in error without the token in the URL", auth, code, u.RequestURI())
		}
	}
}

// TestDeviceActionsShown checks that http and broadcast actions reach the card
// through the real server as text, without headers or body (#450).
func TestDeviceActionsShown(t *testing.T) {
	_, ts, raw := realServer(t)
	for _, r := range [][3]string{
		{"PUT", "/api/v1/topics/ops", `{"device_actions":true}`},
		{"POST", "/api/v1/notifications", `{"topic":"ops","message":"m","actions":[{"label":"Off","http":{"method":"PUT","url":"https://lamp.example.com/off","headers":{"X-Key":"hidden-head"},"body":"hidden-body"}},{"label":"Ping","broadcast":{"intent":"io.example.PING"}}]}`},
	} {
		req, _ := http.NewRequest(r[0], ts.URL+r[1], strings.NewReader(r[2]))
		req.Header.Set("Authorization", "Bearer "+raw)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s %s: %d", r[0], r[1], res.StatusCode)
		}
	}
	_, body, _ := fetch(t, browser(), ts.URL+"/app?auth="+base64.RawURLEncoding.EncodeToString([]byte("Bearer "+raw)))
	for _, want := range []string{"Off</span> runs on a phone: PUT https://lamp.example.com/off<", "Ping</span> runs on a phone: Android broadcast io.example.PING<"} {
		if !strings.Contains(body, want) {
			t.Errorf("page misses %q", want)
		}
	}
	if strings.Contains(body, "hidden-") {
		t.Error("page shows the request's headers or body")
	}
}

// A signed-in browser sets, changes and removes its own mark with the card's
// plain form (#451). Its session token is a child, so its marks are the top
// token's: the page must know that to offer Remove, and only for its own.
func TestMarkForm(t *testing.T) {
	s, ts, raw := realServer(t)
	_, stew, err := s.CreateToken(context.Background(), "stew", "", []string{"admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := func(tok, method, path, body string) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s %s: %d", method, path, res.StatusCode)
		}
	}
	api(raw, "POST", "/api/v1/notifications", `{"topic":"ops","message":"disk full"}`)
	c := browser()
	_, page, _ := fetch(t, c, ts.URL+"/app")
	_, page = submit(t, c, ts.URL, page, raw, "")
	m := regexp.MustCompile(`data-id="([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no card")
	}
	api(stew, "POST", "/api/v1/notifications/"+m[1]+"/mark", `{"mark":"seen"}`)
	post := func(c *http.Client, v url.Values) (int, string) {
		t.Helper()
		v.Set("id", m[1])
		code, body, _ := postForm(t, c, ts.URL, "/app/mark", v)
		return code, body
	}
	if code, body := post(c, url.Values{"mark": {"actioned"}, "link": {"javascript:alert(1)"}}); code != 400 || !strings.Contains(body, "Not marked: link") {
		t.Errorf("a script link: %d %q, want 400 with the reason", code, body)
	}
	if code, page := post(c, url.Values{"mark": {"actioned"}, "note": {" filed #1 "}, "link": {"https://x.example/1"}, "next": {"/app"}}); code != 200 ||
		!strings.Contains(page, `data-mark="actioned">phone actioned: filed #1 <a`) || !strings.Contains(page, `data-mark="seen">stew seen<`) ||
		!strings.Contains(page, `Marked actioned`) || !strings.Contains(page, `name="note" maxlength="200" placeholder="Note (optional)" value="filed #1"`) ||
		strings.Count(page, "Remove my mark") != 1 {
		t.Errorf("after marking: %d, page has no own mark with Remove", code)
	}
	other := browser()
	_, page, _ = fetch(t, other, ts.URL+"/app")
	_, page = submit(t, other, ts.URL, page, stew, "")
	if !strings.Contains(page, "phone actioned: filed #1") || !strings.Contains(page, "Marked seen") || strings.Count(page, "Remove my mark") != 1 || strings.Contains(page, `value="filed #1"`) {
		t.Error("another reader sees the mark but may only remove its own")
	}
	if code, _ := post(c, url.Values{"note": {"x"}}); code != 400 {
		t.Errorf("a post with no mark field: %d, want 400 and the mark kept", code)
	}
	if code, _ := post(c, url.Values{"mark": {""}, "link": {"javascript:x"}, "note": {strings.Repeat("n", 999)}}); code != 200 {
		t.Errorf("Remove with a bad draft link and note: %d, want it removed", code)
	}
	n, _ := s.Get(context.Background(), mustTok(t, s, raw), m[1])
	if len(n.Marks) != 1 || n.Marks[0].By != "stew" {
		t.Errorf("after Remove: %+v, want only stew's mark", n.Marks)
	}
}

// TestSenderTokenForms posts the sender page's rename, rotate and revoke
// forms the way a browser without JavaScript does (#119).
func TestSenderTokenForms(t *testing.T) {
	s, ts, raw := realServer(t)
	ctx := context.Background()
	cam, camRaw, err := s.CreateToken(ctx, "camera", "", []string{"publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publish(t, ts, camRaw, "home", "motion")
	api := func(tok, method, path, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	signedIn := func(tok string) *http.Client {
		c := browser()
		_, page, _ := fetch(t, c, ts.URL+"/app")
		submit(t, c, ts.URL, page, tok, "")
		return c
	}
	post := func(c *http.Client, id string, v url.Values) (int, string, string) {
		t.Helper()
		code, body, res := postForm(t, c, ts.URL, "/app/senders/"+id, v)
		return code, body, res.Request.URL.Path
	}
	c := signedIn(raw)
	if _, page, _ := fetch(t, c, ts.URL+"/app/senders/"+cam.ID); !strings.Contains(page, `name="token_name"`) || !strings.Contains(page, "Rotate secret") || !strings.Contains(page, `name="revoke"`) {
		t.Fatal("a token sender's page has no rename, rotate or revoke")
	}

	// Someone who is no full admin changes nothing.
	_, reader, _ := s.CreateToken(ctx, "reader", "", []string{"read"}, nil)
	other := signedIn(reader)
	for _, v := range []url.Values{{"token_name": {"x"}}, {"rotate": {"1"}}, {"revoke": {"1"}}} {
		if code, _, _ := post(other, cam.ID, v); code == 200 {
			t.Errorf("%v by a reader: 200", v)
		}
	}
	if api(camRaw, "POST", "/api/v1/notifications", `{"topic":"home","message":"still here"}`) != 200 {
		t.Fatal("a reader's posts changed the camera token")
	}

	if code, page, at := post(c, cam.ID, url.Values{"token_name": {"porch camera"}}); code != 200 || at != "/app/senders/"+cam.ID || !strings.Contains(page, `value="porch camera"`) {
		t.Fatalf("rename: %d at %s", code, at)
	}
	code, page, res := postForm(t, c, ts.URL, "/app/senders/"+cam.ID, url.Values{"rotate": {"1"}})
	m := regexp.MustCompile(`<code data-secret>(clrd_[A-Za-z0-9]+)</code>`).FindStringSubmatch(page)
	if code != 200 || m == nil {
		t.Fatalf("rotate: %d, no new secret on the page", code)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("the page with the new secret: Cache-Control %q, want no-store", cc)
	}
	if api(camRaw, "POST", "/api/v1/notifications", `{"topic":"home","message":"x"}`) != 401 || api(m[1], "POST", "/api/v1/notifications", `{"topic":"home","message":"x"}`) != 200 {
		t.Fatal("after rotate the old secret should be 401 and the new one work")
	}
	// Never the token this browser is signed in under: the new secret would be lost.
	me, _ := s.Tokens(ctx, time.Time{})
	if code, _, _ := post(c, me[0].ID, url.Values{"rotate": {"1"}}); code != 400 || api(raw, "GET", "/api/v1/topics", "") != 200 {
		t.Fatalf("rotate own token from the web: %d, want 400 and the token kept", code)
	}
	if code, _, at := post(c, cam.ID, url.Values{"revoke": {"1"}}); code != 200 || at != "/app/senders" {
		t.Fatalf("revoke: %d at %s, want the senders list", code, at)
	}
	if api(raw, "GET", "/api/v1/senders/"+cam.ID, "") != 404 || api(m[1], "POST", "/api/v1/notifications", `{"topic":"home","message":"x"}`) != 401 {
		t.Fatal("after revoke the sender and its token should be gone")
	}

	// An anonymous sender has no token to manage.
	if api(raw, "PUT", "/api/v1/ntfy/anonymous", `{"grants":[{"topic":"up*","scopes":["publish"]}]}`) != 200 || api("", "POST", "/up1", "hi") != 200 {
		t.Fatal("anonymous publish")
	}
	ss, _ := s.Senders(ctx, mustTok(t, s, raw))
	if len(ss) != 1 || ss[0].TokenID != "" {
		t.Fatalf("senders: %+v, want the anonymous one alone", ss)
	}
	for _, sd := range ss {
		if sd.TokenID == "" {
			if _, page, _ := fetch(t, c, ts.URL+"/app/senders/"+sd.ID); !strings.Contains(page, "Rules") || strings.Contains(page, `name="token_name"`) || strings.Contains(page, `name="revoke"`) {
				t.Error("an anonymous sender's page shows token forms")
			}
		}
	}
}

func realServer(t *testing.T) (*server.Server, *httptest.Server, string) {
	t.Helper()
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RateToken: -1, RateTopic: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mountWeb(s, "", false)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	_, raw, err := s.CreateToken(context.Background(), "phone", "allowed", []string{"admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, ts, raw
}

func browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// fetch follows redirects like a browser and returns where it landed.
func fetch(t *testing.T, c *http.Client, u string) (int, string, *url.URL) {
	t.Helper()
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Request.URL
}

// postForm posts v to base+path as a same-origin browser form, following
// redirects, and returns the answer it landed on.
func postForm(t *testing.T, c *http.Client, base, path string, v url.Values) (int, string, *http.Response) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	return send(t, c, req)
}

// send runs req on c and returns the status, body and the (read) answer.
func send(t *testing.T, c *http.Client, req *http.Request) (int, string, *http.Response) {
	t.Helper()
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res
}

// submit posts the page's sign-in form to its own action, as a browser would.
// name goes in the page's device name field, blank or not, as a browser sends it.
func submit(t *testing.T, c *http.Client, base, page, token, name string) (int, string) {
	t.Helper()
	action := regexp.MustCompile(`<form class="signin-form" method="post" action="([^"]+)"`).FindStringSubmatch(page)
	next := regexp.MustCompile(`name="next" value="([^"]*)"`).FindStringSubmatch(page)
	if action == nil || next == nil {
		t.Fatal("no sign-in form on the page")
	}
	unesc := strings.NewReplacer("&amp;", "&", "&#43;", "+")
	v := url.Values{"token": {token}, "next": {unesc.Replace(next[1])}}
	if strings.Contains(page, `<input id="device" name="name"`) {
		v.Set("name", name) // a browser sends the field even when blank
	} else if name != "" {
		t.Fatal("no device name field on the sign-in form")
	}
	res, err := c.PostForm(base+action[1], v)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func publish(t *testing.T, ts *httptest.Server, raw, topic, msg string) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/notifications", strings.NewReader(`{"topic":"`+topic+`","message":"`+msg+`"}`))
	req.Header.Set("Authorization", "Bearer "+raw)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("publish: %v %v", err, res)
	}
}

// TestActForm posts the card form the way a browser without JavaScript does.
func TestActForm(t *testing.T) {
	s, ts, raw := realServer(t)
	c := browser()
	publish(t, ts, raw, "home", "disk full")
	_, page, _ := fetch(t, c, ts.URL+"/app")
	submit(t, c, ts.URL, page, raw, "")
	list, _ := s.List(context.Background(), mustTok(t, s, raw), server.ListQuery{State: "active"})
	id := list[0].ID
	form := func(origin string, v url.Values) (int, string) {
		t.Helper()
		v.Set("id", id)
		req, _ := http.NewRequest("POST", ts.URL+"/app/act", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode, res.Request.URL.RequestURI()
	}
	state := func() *server.Notification {
		n, err := s.Get(context.Background(), mustTok(t, s, raw), id)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if code, at := form("https://evil.example", url.Values{"act": {"dismiss"}}); state().State != "active" {
		t.Fatalf("cross-origin form post changed it (%d at %s)", code, at)
	}
	if code, at := form(ts.URL, url.Values{"act": {"ack"}, "next": {"/app?topic=home"}}); code != 200 || at != "/app?topic=home" || state().State != "acknowledged" || state().StateBy != "Web browser" {
		t.Fatalf("ack: %d at %s, state %s by %s", code, at, state().State, state().StateBy)
	}
	if code, _ := form(ts.URL, url.Values{"snooze": {"1h"}}); code != 200 || state().State != "snoozed" {
		t.Fatalf("snooze: %d, state %s", code, state().State)
	}
}

// TestReplyForm taps a reply button without JavaScript: the answer is stored
// for whoever waits on it, by this browser, and a stale card cannot answer.
func TestReplyForm(t *testing.T) {
	s, ts, raw := realServer(t)
	_, agent, err := s.CreateToken(context.Background(), "agent", "", []string{"publish"}, nil) // not the browser's own token (#187)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/notifications", strings.NewReader(`{"topic":"agents","message":"merge?","actions":[{"label":"Yes","reply":"yes"},{"label":"No","reply":"no"}]}`))
	req.Header.Set("Authorization", "Bearer "+agent)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("publish: %v %v", err, res)
	}
	c := browser()
	_, page, _ := fetch(t, c, ts.URL+"/app")
	_, page = submit(t, c, ts.URL, page, raw, "")
	m := regexp.MustCompile(`data-id="([^"]+)"[^>]*data-seq="(\d+)"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, `name="reply" value="yes"`) || !strings.Contains(page, `name="seq" value="`+m[2]+`"`) {
		t.Fatal("card has no reply buttons with its seq")
	}
	post := func(reply, seq string) int {
		t.Helper()
		v := url.Values{"id": {m[1]}, "reply": {reply}, "seq": {seq}}
		req, _ := http.NewRequest("POST", ts.URL+"/app/act", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", ts.URL)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, seq := range []string{"", "0", "x"} {
		if code := post("no", seq); code != 400 {
			t.Errorf("reply with seq %q: %d, want 400 (no seq would skip the stale check)", seq, code)
		}
	}
	post("yes", m[2])
	n, _ := s.Get(context.Background(), mustTok(t, s, raw), m[1])
	if n.Reply != "yes" || n.State != "acknowledged" || n.StateBy != "Web browser" {
		t.Fatalf("after tapping Yes: reply %q, %s by %s", n.Reply, n.State, n.StateBy)
	}
	post("no", m[2])
	if n, _ := s.Get(context.Background(), mustTok(t, s, raw), m[1]); n.Reply != "yes" {
		t.Fatalf("a second tap changed the answer to %q", n.Reply)
	}
}

// act posts an action with only the session cookie, the way the inbox does.
func act(t *testing.T, c *http.Client, base, id, action string) server.Notification {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/v1/notifications/"+id+"/"+action, nil)
	req.Header.Set("Origin", base)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var n server.Notification
	json.NewDecoder(res.Body).Decode(&n)
	if res.StatusCode != 200 {
		t.Fatalf("%s %s: %d", action, id, res.StatusCode)
	}
	return n
}

// TestDeviceFamily: a browser signed in with a read,publish token sees itself
// in sync and that token in Settings, never another person's devices (issue #245).
func TestDeviceFamily(t *testing.T) {
	s, ts, _ := realServer(t)
	ctx := context.Background()
	_, mine, err := s.CreateToken(ctx, "my phone", "allowed", []string{"read", "publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, theirs, err := s.CreateToken(ctx, "their phone", "allowed", []string{"read", "publish"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publish(t, ts, mine, "home", "from mine")
	publish(t, ts, theirs, "work", "from theirs")
	c := browser()
	_, page, _ := fetch(t, c, ts.URL+"/app")
	if code, _ := submit(t, c, ts.URL, page, mine, "laptop"); code != 200 {
		t.Fatalf("sign in: %d", code)
	}
	_, body, _ := fetch(t, c, ts.URL+"/app/settings")
	if !strings.Contains(body, "In sync on 1 device<") || strings.Contains(body, `aria-label="my phone`) || !strings.Contains(body, "<span>laptop</span>") || !strings.Contains(body, "<span>my phone</span>") {
		t.Errorf("want only this browser live (a token not streaming is no dot, #295), got header %q", regexp.MustCompile(`(?s)data-live="devices">.*?</a>`).FindString(body))
	}
	if strings.Contains(body, "their phone") {
		t.Error("a non-admin browser sees another person's device")
	}
}

// TestFormatCheckSettings: Settings lists a topic with format problems, the
// count per rule and its fix prompt, and shows nothing while there are none (#308).
func TestFormatCheckSettings(t *testing.T) {
	s, ts, raw := realServer(t)
	c := browser()
	_, page, _ := fetch(t, c, ts.URL+"/app")
	if code, _ := submit(t, c, ts.URL, page, raw, "laptop"); code != 200 {
		t.Fatalf("sign in: %d", code)
	}
	defer func() { // #391: a group with no known sender keeps its label and prompt
		db, err := sql.Open("sqlite", s.DBPath())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`UPDATE notifications SET sender = '' WHERE message = 'build failed'`); err != nil {
			t.Fatal(err)
		}
		_, body, _ := fetch(t, c, ts.URL+"/app/settings")
		if !strings.Contains(body, "Unknown sender</span>") || strings.Count(body, "data-format-prompt") != 5 { // ci and the three topics below
			t.Errorf("unknown sender group: want its label and a prompt per group")
		}
	}()
	if code, body, _ := fetch(t, c, ts.URL+"/app/settings"); code != 200 || !strings.Contains(body, "Linked devices") || strings.Contains(body, "data-format-check") {
		t.Errorf("settings with no messages: %d, want the page without the format section", code)
	}
	publish(t, ts, raw, "ci", "build failed")
	publish(t, ts, raw, "ci", "build passed")
	_, body, _ := fetch(t, c, ts.URL+"/app/settings")
	for _, want := range []string{"data-format-check", `data-format-card="ci" open`, "1 finding<", "2 of the last 2 messages", ">No title</span><span class=\"badge\" data-variant=\"outline\">2</span>", `topic &#34;ci&#34;`, "Dismiss for this sender", "Stop checking ci"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page lacks %q", want)
		}
	}
	// Past three topics every card starts collapsed (#618).
	for _, topic := range []string{"t1", "t2", "t3"} {
		publish(t, ts, raw, topic, "x")
	}
	_, cards, _ := fetch(t, c, ts.URL+"/app/settings")
	for _, topic := range []string{"ci", "t1", "t2", "t3"} {
		if !strings.Contains(cards, `data-format-card="`+topic+`"`) || strings.Contains(cards, `data-format-card="`+topic+`" open`) {
			t.Errorf("four topics: want card %s shown and collapsed", topic)
		}
	}
}

// TestHidePublicPages: CLRD_PUBLIC_PAGES=off keeps a private server's
// address from showing the landing page or docs, or being indexed.
func TestHidePublicPages(t *testing.T) {
	t.Setenv("CLRD_PUBLIC_PAGES", "maybe")
	if _, err := server.ConfigFromEnv(); err == nil {
		t.Fatal("CLRD_PUBLIC_PAGES=maybe accepted")
	}
	t.Setenv("CLRD_PUBLIC_PAGES", "off")
	opt, err := server.ConfigFromEnv()
	if err != nil || !opt.HidePublicPages {
		t.Fatalf("off: %v %v", opt.HidePublicPages, err)
	}
	s, err := server.New(server.Options{DB: filepath.Join(t.TempDir(), "clrd.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mountWeb(s, "", opt.HidePublicPages)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for path, want := range map[string]int{"/": 302, "/docs": 404, "/docs/spec": 404, "/sitemap.xml": 404, "/robots.txt": 200, "/app": 401, "/static/app.css": 200} {
		res, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want || res.Header.Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Errorf("%s: %d %q, want %d and X-Robots-Tag", path, res.StatusCode, res.Header.Get("X-Robots-Tag"), want)
		}
		if path == "/" && res.Header.Get("Location") != "/app" {
			t.Errorf("/ goes to %q", res.Header.Get("Location"))
		}
		if path == "/robots.txt" && !strings.Contains(string(body), "Disallow: /\n") {
			t.Errorf("robots.txt: %q", body)
		}
	}
}

// Topic subscriptions (#617) through the real store: a signed-in browser
// unsubscribes from a topic, the inbox drops it, its own page still shows it
// with the banner, Settings lists it switched off, and Subscribe brings it back.
func TestSubscriptionsOnRealServer(t *testing.T) {
	_, ts, raw := realServer(t)
	c := browser()
	publish(t, ts, raw, "home", "disk full")
	publish(t, ts, raw, "work", "deploy done")
	_, page, _ := fetch(t, c, ts.URL+"/app")
	if code, _ := submit(t, c, ts.URL, page, raw, "laptop"); code != 200 {
		t.Fatalf("sign in: %d", code)
	}
	post := func(v url.Values) {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+"/app/subscriptions", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", ts.URL)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("post %v: %d %s", v, res.StatusCode, b)
		}
	}
	get := func(path string) string {
		t.Helper()
		_, body, _ := fetch(t, c, ts.URL+path)
		return body
	}
	post(url.Values{"topic": {"work"}, "subscribe": {"0"}, "next": {"/app"}})
	if body := get("/app"); strings.Contains(body, "deploy done") || !strings.Contains(body, "disk full") {
		t.Error("inbox: want home only after unsubscribing from work")
	}
	if body := get("/app?topic=work"); !strings.Contains(body, "deploy done") || !strings.Contains(body, "data-unsub-banner") {
		t.Error("work page: want the card and the not-subscribed banner")
	}
	if body := get("/app?topic=home"); strings.Contains(body, "data-unsub-banner") {
		t.Error("home page: banner on a subscribed topic")
	}
	if body := get("/app/settings"); !strings.Contains(body, `value="0:work"`) || !strings.Contains(body, `value="1:home"`) {
		t.Error("settings: want work off and home on")
	}
	// A question marks its topic (#623): its card warns before Unsubscribe.
	publish(t, ts, raw, "deploys", `ship?","actions":[{"label":"Yes","reply":"yes"}],"title":"Ship`)
	if body := get("/app"); !strings.Contains(body, `data-unsub="deploys" data-approvals`) || strings.Contains(body, `data-unsub="home" data-approvals`) {
		t.Error("inbox: want the warning on deploys only")
	}
	// The Settings list sends only what changed: home stays on.
	post(url.Values{"was": {"1:home", "0:work"}, "on": {"home", "work"}})
	if body := get("/app"); !strings.Contains(body, "deploy done") {
		t.Error("inbox: work missing after subscribing again")
	}
}

// A GitHub issue links one notification at /app/n/{id} (#814): a page with
// its card, linking back to the view its state is in now, after a clear or
// snooze too (#822); signed
// out it is the sign-in page, and gone or in a topic the token cannot read it
// is the same not found page.
func TestNotificationLink(t *testing.T) {
	s, ts, raw := realServer(t)
	ctx := context.Background()
	c := browser()
	publish(t, ts, raw, "home", "disk full")
	tok := mustTok(t, s, raw)
	list, _ := s.List(ctx, tok, server.ListQuery{State: "active"})
	id := list[0].ID
	code, page, _ := fetch(t, c, ts.URL+"/app/n/"+id)
	if code != 401 || strings.Contains(page, "disk full") || !strings.Contains(page, `value="/app/n/`+id+`"`) {
		t.Fatalf("signed out: %d, want the sign-in page coming back here", code)
	}
	if code, body := submit(t, c, ts.URL, page, raw, ""); code != 200 || !strings.Contains(body, `id="t-`+id+`"`) {
		t.Fatalf("sign in from the link: %d, want its card", code)
	}
	soon := time.Now().Add(time.Hour)
	for _, step := range []struct {
		act, path string
		until     *time.Time
	}{{"", "/app", nil}, {"ack", "/app", nil}, {"snooze", "/app/snoozed", &soon}, {"dismiss", "/app/cleared", nil}, {"reopen", "/app", nil}, {"resolve", "/app/cleared", nil}} {
		if step.act != "" {
			if _, err := s.Act(ctx, tok, id, step.act, "api", step.until); err != nil {
				t.Fatal(step.act, err)
			}
		}
		code, body, at := fetch(t, c, ts.URL+"/app/n/"+id)
		if code != 200 || at.Path != "/app/n/"+id || !strings.Contains(body, `id="t-`+id+`"`) || !strings.Contains(body, `href="`+step.path+`?topic=home#t-`+id+`" data-back`) {
			t.Errorf("after %q: %d at %s, want its card linking back to %s?topic=home", step.act, code, at, step.path)
		}
	}
	// A token for another topic, and an id that is not there, both get the
	// not found page, never the notification.
	_, other, err := s.CreateToken(ctx, "work only", "allowed", []string{"read"}, []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{id, "nope"} {
		req, _ := http.NewRequest("GET", ts.URL+"/app/n/"+u, nil)
		req.Header.Set("Authorization", "Bearer "+other)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 404 || strings.Contains(string(b), "disk full") || !strings.Contains(string(b), "This notification is gone") {
			t.Errorf("%s by a work-only token: %d, want the not found page", u, res.StatusCode)
		}
	}
}
