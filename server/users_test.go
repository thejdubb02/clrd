package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Issue #143: a database from before users, with an imported ntfy user,
// upgrades on start and the ntfy password still signs in.
func TestUsersUpgrade(t *testing.T) {
	ctx := context.Background()
	open := reopener(t)
	s := open()
	tok, _, err := s.CreateToken(ctx, "phone", ApproveDenied, []string{ScopeRead, ScopePublish}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("phonepass"), bcrypt.MinCost)
	for _, q := range []string{`ALTER TABLE tokens DROP COLUMN user_id`, `DROP TABLE people`} { // as on main before #143
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO users (name, hash, token_id) VALUES ('phone', ?, ?)`, string(hash), tok.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = open()
	srv := httptest.NewServer(s)
	defer func() { srv.Close(); s.Close() }()
	if c := basic(t, "GET", srv.URL+"/alerts/json?poll=1", "phone", "phonepass", ""); c != 200 {
		t.Fatalf("ntfy password after upgrade: %d", c)
	}
	admin := mkToken(t, s, "admin", []string{ScopeAdmin}, nil)
	_, u := call(t, srv, "POST", "/api/v1/users", admin, map[string]any{"name": "Justin"})
	if c, out := call(t, srv, "PUT", "/api/v1/tokens/"+tok.ID+"/user", admin, map[string]any{"user": u["id"]}); c != 200 {
		t.Fatalf("attach after upgrade: %d %v", c, out)
	}
	if c := basic(t, "GET", srv.URL+"/alerts/json?poll=1", "phone", "phonepass", ""); c != 200 {
		t.Fatalf("ntfy password once attached: %d", c)
	}
}

// Access follows the user at every request: a browser already signed in
// sees a change to its user's grants, and its open stream closes (#143).
func TestUserGrantsFollowDevices(t *testing.T) {
	s, srv, admin := newTest(t)
	s.tokLimit, s.topicLimit = nil, nil
	_, u := call(t, srv, "POST", "/api/v1/users", admin, map[string]any{"name": "Justin",
		"grants": []Grant{{Topic: "home-*", Scopes: []string{ScopeRead, ScopePublish}}}})
	uid, _ := u["id"].(string)
	c, out := call(t, srv, "POST", "/api/v1/tokens", admin, map[string]any{"name": "laptop", "scopes": []string{"read", "publish"}, "user": uid})
	if c != 200 || out["user"] != uid {
		t.Fatalf("create with user: %d %v", c, out)
	}
	laptop := out["token"].(string)
	browser := childOf(t, s, laptop)
	read := func(tok, topic string) int {
		c, _ := call(t, srv, "GET", "/api/v1/notifications?topic="+topic, tok, nil)
		return c
	}
	if read(browser, "home-door") != 200 || read(browser, "work") != 403 || read(laptop, "work") != 403 {
		t.Fatalf("user grants: home %d, work %d", read(browser, "home-door"), read(browser, "work"))
	}

	res, lines := openStream(t, srv.URL+"/api/v1/events/sse", browser)
	defer res.Body.Close()
	if c, out := call(t, srv, "PUT", "/api/v1/users/"+uid, admin, map[string]any{"grants": []Grant{{Topic: "work", Scopes: []string{ScopeRead}}}}); c != 200 {
		t.Fatalf("set grants: %d %v", c, out)
	}
	closed := time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-lines:
		case <-closed:
			t.Fatal("stream still open after a grant change")
		}
	}
	if read(browser, "home-door") != 403 || read(browser, "work") != 200 {
		t.Fatalf("after change: home %d, work %d", read(browser, "home-door"), read(browser, "work"))
	}
	if c, _ := call(t, srv, "POST", "/api/v1/notifications", browser, map[string]any{"topic": "work", "message": "x"}); c != 403 {
		t.Fatalf("read-only grant published: %d", c)
	}
	res2, _ := openStream(t, srv.URL+"/api/v1/events/sse", browser) // reconnects under the new rules
	res2.Body.Close()

	// An empty list is no topic; null limits nothing, so devices keep their own.
	call(t, srv, "PUT", "/api/v1/users/"+uid, admin, map[string]any{"grants": []Grant{}})
	if read(browser, "work") != 403 {
		t.Fatal("empty grants still let a topic through")
	}
	call(t, srv, "PUT", "/api/v1/users/"+uid, admin, map[string]any{"grants": nil})
	if read(browser, "anything") != 200 {
		t.Fatal("null grants did not hand back the device's own access")
	}
	if c, _ := call(t, srv, "PUT", "/api/v1/users/"+uid, admin, map[string]any{}); c != 400 {
		t.Fatalf("missing grants: %d, want 400", c)
	}
	// Fail closed: a user that cannot be read is no access, not the token's own.
	s.db.Exec(`UPDATE people SET grants = 'bad' WHERE id = ?`, uid)
	if read(browser, "anything") != 401 {
		t.Fatal("bad user grants let a device through")
	}
	// Detaching the laptop closes its browser's stream and hands back its own access.
	lid := out["id"].(string)
	call(t, srv, "PUT", "/api/v1/users/"+uid, admin, map[string]any{"grants": []Grant{{Topic: "work", Scopes: []string{ScopeRead}}}})
	res3, lines3 := openStream(t, srv.URL+"/api/v1/events/sse", browser)
	defer res3.Body.Close()
	if c, out := call(t, srv, "PUT", "/api/v1/tokens/"+lid+"/user", admin, map[string]any{"user": ""}); c != 200 {
		t.Fatalf("detach: %d %v", c, out)
	}
	closed = time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-lines3:
		case <-closed:
			t.Fatal("stream still open after detach")
		}
	}
	if read(browser, "anything") != 200 {
		t.Fatal("detached device did not get its own access back")
	}
}

// #428: a grant change that lands after a stream signed in but before it
// subscribed drops nothing, so the stream reads access again once subscribed.
func TestStreamMidStartGrantChange(t *testing.T) {
	s, srv, admin := newTest(t)
	_, u := call(t, srv, "POST", "/api/v1/users", admin, map[string]any{"name": "ann",
		"grants": []Grant{{Topic: "home", Scopes: []string{ScopeRead}}}})
	uid, _ := u["id"].(string)
	_, out := call(t, srv, "POST", "/api/v1/tokens", admin, map[string]any{"name": "phone", "scopes": []string{"read"}, "user": uid})
	call(t, srv, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "home", "message": "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stale := s.lookupToken(ctx, out["token"].(string)) // signed in under the old grants
	if c, _ := call(t, srv, "PUT", "/api/v1/users/"+uid, admin, map[string]any{"grants": []Grant{{Topic: "work", Scopes: []string{ScopeRead}}}}); c != 200 {
		t.Fatal("set grants")
	}
	var got []string
	s.follow(ctx, stale, 0, nil, "", nil, func(msg []byte, seq int64) error {
		if got = append(got, string(msg)); strings.Contains(string(msg), `"type":"ready"`) {
			cancel()
		}
		return nil
	}, func() error { return nil })
	if strings.Contains(strings.Join(got, ""), "secret") {
		t.Fatal("a stream mid-start kept the grant taken away")
	}

	// A change mid-replay stops the replay at once: nothing more goes out.
	call(t, srv, "PUT", "/api/v1/users/"+uid, admin, map[string]any{"grants": []Grant{{Topic: "home", Scopes: []string{ScopeRead}}}})
	call(t, srv, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "home", "message": "later"})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	got = nil
	err := s.follow(ctx2, s.lookupToken(ctx2, out["token"].(string)), 0, nil, "", nil, func(msg []byte, seq int64) error {
		if got = append(got, string(msg)); strings.Contains(string(msg), "secret") {
			call(t, srv, "PUT", "/api/v1/users/"+uid, admin, map[string]any{"grants": []Grant{{Topic: "work", Scopes: []string{ScopeRead}}}})
		}
		return nil
	}, func() error { return nil })
	if err == nil || strings.Contains(strings.Join(got, ""), "later") {
		t.Fatalf("a replay went on after the grant was taken away: %v", err)
	}
}

// Sharing is a grant on the other user; scopes stay the token's ceiling,
// a grant never gives admin, and a token with no user is unchanged.
func TestUsersShareAndCeiling(t *testing.T) {
	s, srv, admin := newTest(t)
	mkUser := func(name string, g []Grant) string {
		t.Helper()
		c, u := call(t, srv, "POST", "/api/v1/users", admin, map[string]any{"name": name, "grants": g})
		if c != 200 {
			t.Fatalf("create user %s: %d %v", name, c, u)
		}
		return u["id"].(string)
	}
	ann := mkUser("Ann", []Grant{{Topic: "family", Scopes: []string{ScopeRead, ScopePublish}}})
	bob := mkUser("Bob", []Grant{{Topic: "family", Scopes: []string{ScopeRead}}})
	attach := func(tok, user string) int {
		t.Helper()
		var id string
		s.db.QueryRow(`SELECT id FROM tokens WHERE hash = ?`, hashToken(tok)).Scan(&id)
		c, _ := call(t, srv, "PUT", "/api/v1/tokens/"+id+"/user", admin, map[string]any{"user": user})
		return c
	}
	annPhone := mkToken(t, s, "ann-phone", []string{"read", "publish"}, nil)
	bobPhone := mkToken(t, s, "bob-phone", []string{"read", "publish"}, nil)
	readOnly := mkToken(t, s, "ann-tablet", []string{"read"}, nil)
	plain := mkToken(t, s, "plain", []string{"read", "publish"}, []string{"x"})
	for _, a := range [][2]string{{annPhone, ann}, {bobPhone, bob}, {readOnly, ann}} {
		if c := attach(a[0], a[1]); c != 200 {
			t.Fatalf("attach: %d", c)
		}
	}
	pub := func(tok string) int {
		c, _ := call(t, srv, "POST", "/api/v1/notifications", tok, map[string]any{"topic": "family", "message": "dinner"})
		return c
	}
	if pub(annPhone) != 200 || pub(bobPhone) != 403 || pub(readOnly) != 403 {
		t.Fatalf("publish: ann %d, bob %d, read-only %d", pub(annPhone), pub(bobPhone), pub(readOnly))
	}
	if c, out := call(t, srv, "GET", "/api/v1/notifications?topic=family", bobPhone, nil); c != 200 || len(out["notifications"].([]any)) != 1 {
		t.Fatalf("bob reads the shared topic: %d %v", c, out)
	}
	if c, _ := call(t, srv, "GET", "/api/v1/notifications?topic=x", plain, nil); c != 200 {
		t.Fatalf("token with no user: %d", c)
	}
	if c, _ := call(t, srv, "GET", "/api/v1/notifications?topic=y", plain, nil); c != 403 {
		t.Fatalf("token with no user widened: %d", c)
	}
	if c, _ := call(t, srv, "PUT", "/api/v1/users/"+ann, admin, map[string]any{"grants": []Grant{{Topic: "*", Scopes: []string{ScopeAdmin}}}}); c != 400 {
		t.Fatalf("admin grant: %d, want 400", c)
	}
	// An admin token keeps full admin whatever its user's grants.
	if c := attach(admin, ann); c != 200 {
		t.Fatalf("attach admin: %d", c)
	}
	if c, _ := call(t, srv, "GET", "/api/v1/tokens", admin, nil); c != 200 {
		t.Fatalf("admin lost admin to its user's grants: %d", c)
	}
	if c, _ := call(t, srv, "DELETE", "/api/v1/users/"+bob, admin, nil); c != 409 {
		t.Fatalf("delete user with tokens: %d, want 409", c)
	}
	attach(bobPhone, "")
	if c, _ := call(t, srv, "DELETE", "/api/v1/users/"+bob, admin, nil); c != 204 {
		t.Fatalf("delete user: %d", c)
	}
}

// Only a full admin uses the user routes.
func TestUsersNeedFullAdmin(t *testing.T) {
	s, srv, admin := newTest(t)
	_, u := call(t, srv, "POST", "/api/v1/users", admin, map[string]any{"name": "Justin"})
	uid := u["id"].(string)
	reader := mkToken(t, s, "reader", []string{"read", "publish"}, nil)
	limited := mkToken(t, s, "limited-admin", []string{"admin"}, []string{"x"})
	var rid string
	s.db.QueryRow(`SELECT id FROM tokens WHERE hash = ?`, hashToken(reader)).Scan(&rid)
	for _, tok := range []string{reader, limited, ""} {
		for _, r := range [][2]string{{"GET", "/api/v1/users"}, {"POST", "/api/v1/users"}, {"PUT", "/api/v1/users/" + uid},
			{"DELETE", "/api/v1/users/" + uid}, {"PUT", "/api/v1/tokens/" + rid + "/user"}} {
			if c, _ := call(t, srv, r[0], r[1], tok, map[string]any{"name": "x", "grants": nil, "user": uid}); c != 403 && c != 401 {
				t.Errorf("%s %s by a non-admin: %d", r[0], r[1], c)
			}
		}
		if c, _ := call(t, srv, "POST", "/api/v1/tokens", tok, map[string]any{"name": "x", "scopes": []string{"read"}, "user": uid}); c != 403 && c != 401 {
			t.Errorf("token create with user by a non-admin: %d", c)
		}
	}
	if c, _ := call(t, srv, "GET", "/api/v1/users", admin, nil); c != 200 {
		t.Fatalf("admin lists users: %d", c)
	}
}

// A Gotify app keeps its one topic: a user's grants must never replace it.
func TestUsersSkipAppTokens(t *testing.T) {
	_, srv, admin := newTest(t)
	_, u := call(t, srv, "POST", "/api/v1/users", admin, map[string]any{"name": "Justin"})
	_, app := call(t, srv, "POST", "/api/v1/gotify/apps", admin, map[string]any{"name": "Watchtower", "topic": "tv"})
	if c, _ := call(t, srv, "PUT", "/api/v1/tokens/"+app["token_id"].(string)+"/user", admin, map[string]any{"user": u["id"]}); c != 404 {
		t.Fatalf("attach a Gotify app to a user: %d, want 404", c)
	}
}

// #406 with users: two typed-in tokens of one person both count as "mine",
// so each sees the other's sender name; another person does not.
func TestUsersFormatCheckOwner(t *testing.T) {
	s, srv, admin := newTest(t)
	_, u := call(t, srv, "POST", "/api/v1/users", admin, map[string]any{"name": "Justin"})
	uid := u["id"].(string)
	laptop := mkToken(t, s, "my-laptop", []string{"read", "publish"}, []string{"jobs"})
	phone := mkToken(t, s, "my-phone", []string{"read", "publish"}, []string{"jobs"})
	other := mkToken(t, s, "someone", []string{"read", "publish"}, []string{"jobs"})
	for _, tok := range []string{laptop, phone} {
		var id string
		s.db.QueryRow(`SELECT id FROM tokens WHERE hash = ?`, hashToken(tok)).Scan(&id)
		if c, _ := call(t, srv, "PUT", "/api/v1/tokens/"+id+"/user", admin, map[string]any{"user": uid}); c != 200 {
			t.Fatalf("attach: %d", c)
		}
	}
	if c, _ := call(t, srv, "POST", "/api/v1/notifications", laptop, map[string]any{"topic": "jobs", "message": "no title"}); c != 200 {
		t.Fatalf("publish: %d", c)
	}
	names := func(tok string) string {
		_, out := call(t, srv, "GET", "/api/v1/format-check?by=sender", tok, nil)
		got := []string{}
		for _, g := range out["topics"].([]any) {
			n, _ := g.(map[string]any)["sender"].(string)
			got = append(got, n)
		}
		return strings.Join(got, ",")
	}
	if n := names(phone); n != "my-laptop" {
		t.Fatalf("own user's sender name hidden: %q", n)
	}
	if n := names(other); n != "" {
		t.Fatalf("another person sees the name: %q", n)
	}
	// And the device list holds both of the user's tokens.
	_, out := call(t, srv, "GET", "/api/v1/devices?all=1", phone, nil)
	if n := len(out["devices"].([]any)); n != 2 {
		t.Fatalf("device list of a two-token user: %d devices", n)
	}
	// So does the device log: a sign-in on the laptop shows on the phone.
	childOf(t, s, laptop)
	if _, out := call(t, srv, "GET", "/api/v1/devices/log", phone, nil); len(out["events"].([]any)) != 1 {
		t.Fatalf("device log of a two-token user: %v", out)
	}
	// own_request (#187) stays the token tree: a person answers their own agent.
	_, n := call(t, srv, "POST", "/api/v1/notifications", laptop, map[string]any{"topic": "jobs", "message": "merge?",
		"actions": []map[string]any{{"label": "Allow", "reply": "allow"}}})
	if c, out := call(t, srv, "POST", "/api/v1/notifications/"+n["id"].(string)+"/reply", phone, map[string]any{"reply": "allow"}); c != 200 {
		t.Fatalf("same user, other token tree, answers: %d %v", c, out)
	}
}

// childOf mints a browser session from tok, as POST /api/v1/session does.
func childOf(t *testing.T, s *Server, raw string) string {
	t.Helper()
	parent := s.lookupToken(context.Background(), raw)
	if parent == nil {
		t.Fatal("sign in: bad token")
	}
	_, child, err := s.childToken(context.Background(), parent, parent.ID, "browser", false)
	if err != nil {
		t.Fatal(err)
	}
	return child
}

// openStream opens an event stream with tok and returns its lines.
func openStream(t *testing.T, url, tok string) (*http.Response, chan string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("stream: %v %v", err, res)
	}
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	return res, lines
}
