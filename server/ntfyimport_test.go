package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// importFixture imports testdata/ntfy (written by the real ntfy v2.38.1, see
// make-fixture.sh) into a fresh server. It returns the server, its URL and
// ntfy's own tk_ tokens by label ("" for the unlabelled one).
func importFixture(t *testing.T, in NtfyImport, before ...func(*Server, string, string)) (*Server, string, map[string]string, string) {
	t.Helper()
	dir := t.TempDir() // a copy: opening a WAL database writes beside it
	for _, f := range []string{"user.db", "cache.db", "attachments/WkNQimnAbetP"} {
		b, err := os.ReadFile(filepath.Join("testdata/ntfy", f))
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src, err := sql.Open("sqlite", filepath.Join(dir, "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	toks := map[string]string{}
	rows, err := src.Query(`SELECT label, token FROM user_token`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var l, tk string
		rows.Scan(&l, &tk)
		toks[l] = tk
	}
	rows.Close()
	src.Close()
	s, ts, admin := newTest(t)
	for _, f := range before {
		f(s, ts.URL, admin)
	}
	in.AuthFile, in.CacheFile, in.AttachmentDir = filepath.Join(dir, "user.db"), filepath.Join(dir, "cache.db"), filepath.Join(dir, "attachments")
	if in.DefaultAccess == "" {
		in.DefaultAccess = "deny-all" // what make-fixture.sh ran ntfy with
	}
	var rep strings.Builder
	if err := s.ImportNtfy(context.Background(), in, &rep); err != nil {
		t.Fatalf("import: %v\n%s", err, rep.String())
	}
	return s, ts.URL, toks, rep.String()
}

func bearer(t *testing.T, method, url, tok, body string) (int, string) {
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return send(t, req)
}

func basic(t *testing.T, method, url, user, pass, body string) int {
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.SetBasicAuth(user, pass)
	code, _ := send(t, req)
	return code
}

func send(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestNtfyImportDryRun(t *testing.T) {
	s, _, _, rep := importFixture(t, NtfyImport{DryRun: true})
	for _, want := range []string{"Dry run", "token pixel (user phone): kept", "not imported: 1 expiring", "web push"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	var n int
	s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM tokens) + (SELECT COUNT(*) FROM events) + (SELECT COUNT(*) FROM meta WHERE key = 'ntfy_imported')`).Scan(&n)
	if n != 1 { // the admin token newTest made
		t.Fatalf("dry run wrote %d rows", n)
	}
	if fs, _ := os.ReadDir(s.opt.Attachments); len(fs) != 0 {
		t.Fatalf("dry run copied %d files", len(fs))
	}
}

func TestNtfyImport(t *testing.T) {
	s, u, toks, _ := importFixture(t, NtfyImport{})
	boss, pixel, web := toks[""], toks["pixel"], toks["web"]

	// A second import is refused.
	if err := s.ImportNtfy(context.Background(), NtfyImport{DefaultAccess: "deny-all"}, io.Discard); err == nil {
		t.Fatal("second import ran")
	}

	// ntfy's tk_ tokens keep working, Bearer and ?auth=, native API too.
	if c, b := bearer(t, "GET", u+"/alerts/json?poll=1", boss, ""); c != 200 || !strings.Contains(b, "Job finished") {
		t.Fatalf("boss poll: %d %s", c, b)
	}
	if c, _ := raw(t, "GET", u+"/alerts/json?poll=1&auth="+basicAuthParam(pixel), "", "", nil); c != 200 {
		t.Fatalf("pixel ?auth=: %d", c)
	}
	if c, _ := bearer(t, "GET", u+"/api/v1/notifications", boss, ""); c != 200 {
		t.Fatalf("boss native: %d", c)
	}
	if c, _ := bearer(t, "GET", u+"/alerts/json?poll=1", web, ""); c != 401 {
		t.Fatalf("expiring web token: %d, want 401", c)
	}

	// Write-only drop*: publish works, nothing reads it back.
	if c, b := bearer(t, "POST", u+"/drop-box", pixel, "secret drop"); c != 200 {
		t.Fatalf("publish drop-box: %d %s", c, b)
	}
	var id string
	s.db.QueryRow(`SELECT id FROM notifications WHERE topic = 'drop-box'`).Scan(&id)
	if id == "" {
		t.Fatal("drop-box not stored")
	}
	for _, p := range []string{
		"/drop-box/json?poll=1", "/drop-box/sse?poll=1", "/drop-box/raw?poll=1", "/DROP-box/json?poll=1", "/alerts,drop-box/json?poll=1",
		"/api/v1/notifications?topic=drop-box", "/api/v1/notifications/" + id,
	} {
		if c, b := bearer(t, "GET", u+p, pixel, ""); c != 403 || strings.Contains(b, "secret drop") {
			t.Errorf("pixel read %s: %d %s", p, c, b)
		}
	}
	for _, p := range []string{"/api/v1/notifications", "/api/v1/events", "/api/v1/topics", "/alerts/json?poll=1&since=all"} {
		if c, b := bearer(t, "GET", u+p, pixel, ""); c != 200 || strings.Contains(b, "secret drop") || strings.Contains(b, "drop-box") {
			t.Errorf("pixel %s leaks drop-box: %d %s", p, c, b)
		}
	}
	if c, _ := bearer(t, "POST", u+"/api/v1/notifications/"+id+"/dismiss", pixel, ""); c != 403 {
		t.Errorf("pixel dismissed a write-only notification: %d", c)
	}
	// Acting on or overwriting someone else's message by its sequence id
	// must not hand that message back to a write-only token.
	if c, _ := raw(t, "POST", u+"/drop-box", boss, "boss secret", map[string]string{"X-Sequence-ID": "d2"}); c != 200 {
		t.Fatalf("boss publish drop-box: %d", c)
	}
	for _, p := range []string{"/drop-box/d2/clear", "/drop-box/d2/ack"} {
		m := map[bool]string{true: "PUT", false: "POST"}[strings.HasSuffix(p, "clear")]
		if c, b := bearer(t, m, u+p, pixel, ""); c != 200 || strings.Contains(b, "boss secret") {
			t.Errorf("pixel %s leaks: %d %s", p, c, b)
		}
	}
	req, _ := http.NewRequest("POST", u+"/drop-box", strings.NewReader("mine"))
	req.Header.Set("Authorization", "Bearer "+pixel)
	req.Header.Set("X-Sequence-ID", "d2")
	if c, b := send(t, req); c != 200 || strings.Contains(b, "acknowledged") || strings.Contains(b, "boss") {
		t.Errorf("pixel overwrite d2 leaks state: %d %s", c, b)
	}
	// A malformed ?auth= is a wrong credential, not anonymous.
	if c, _ := raw(t, "GET", u+"/public/json?poll=1&auth=!!!", "", "", nil); c != 401 {
		t.Errorf("bad ?auth= fell back to anonymous: %d", c)
	}

	// Deny beats the default, in any case; read-only cannot publish; _ is literal.
	for _, p := range []string{"/secret", "/SECRET", "/news", "/announce", "/myXtopic"} {
		if c, _ := bearer(t, "POST", u+p, pixel, "x"); c != 403 {
			t.Errorf("pixel publish %s: %d, want 403", p, c)
		}
	}
	for p, want := range map[string]int{"/secret": 403, "/Secret": 403, "/news": 200, "/my_topic": 200, "/myXtopic": 403, "/public": 200, "/other": 403} {
		if c, _ := bearer(t, "GET", u+p+"/json?poll=1", pixel, ""); c != want {
			t.Errorf("pixel read %s: %d, want %d", p, c, want)
		}
	}

	// Passwords: ntfy routes only, never the native API.
	if c := basic(t, "GET", u+"/alerts/json?poll=1", "phone", "phonepass", ""); c != 200 {
		t.Errorf("phone password on ntfy route: %d", c)
	}
	if c := basic(t, "POST", u+"/drop-pw", "phone", "phonepass", "x"); c != 200 {
		t.Errorf("phone password publish: %d", c)
	}
	if c := basic(t, "GET", u+"/drop-pw/json?poll=1", "phone", "phonepass", ""); c != 403 {
		t.Errorf("phone password read write-only: %d", c)
	}
	if c := basic(t, "GET", u+"/api/v1/notifications", "boss", "adminpass", ""); c != 401 {
		t.Errorf("password on native API: %d, want 401", c)
	}
	if c := basic(t, "GET", u+"/alerts/json?poll=1", "phone", "wrong", ""); c != 401 {
		t.Errorf("wrong password: %d, want 401", c)
	}

	// /v1/account: what ntfy's app and scripts use to check a login.
	acct := func(c int, b string) string { return fmt.Sprint(c, " ", strings.TrimSpace(b)) }
	if got := acct(bearer(t, "GET", u+"/v1/account", boss, "")); !strings.Contains(got, `"role":"admin"`) || !strings.HasPrefix(got, "200") {
		t.Errorf("account, admin token: %s", got)
	}
	req, _ = http.NewRequest("GET", u+"/v1/account", nil)
	req.SetBasicAuth("phone", "phonepass")
	if got := acct(send(t, req)); got != `200 {"role":"user","username":"phone"}` {
		t.Errorf("account, password: %s", got)
	}
	if got := acct(bearer(t, "GET", u+"/v1/account", "", "")); got != `200 {"role":"anonymous","username":"*"}` {
		t.Errorf("account, no credentials: %s", got)
	}
	if c := basic(t, "GET", u+"/v1/account", "phone", "wrong", ""); c != 401 {
		t.Errorf("account, wrong password: %d", c)
	}

	// Anonymous: ntfy's * rules, no credentials, ntfy routes only.
	if c, b := bearer(t, "GET", u+"/public/json?poll=1", "", ""); c != 200 || !strings.Contains(b, "Hello anyone") {
		t.Errorf("anonymous read public: %d %s", c, b)
	}
	if c, _ := bearer(t, "POST", u+"/public", "", "hi"); c != 200 {
		t.Errorf("anonymous publish public: %d", c)
	}
	if c, _ := bearer(t, "POST", u+"/announce", "", "hi"); c != 403 {
		t.Errorf("anonymous publish announce: %d", c)
	}
	if c, _ := bearer(t, "GET", u+"/alerts/json?poll=1", "", ""); c != 403 {
		t.Errorf("anonymous read alerts: %d", c)
	}
	if c, _ := bearer(t, "GET", u+"/api/v1/notifications", "", ""); c != 401 {
		t.Errorf("anonymous native: %d", c)
	}

	// History: sequence updates merge, a delete dismisses, a delay snoozes.
	state := func(msg string) string {
		var st string
		s.db.QueryRow(`SELECT state FROM notifications WHERE message = ?`, msg).Scan(&st)
		return st
	}
	if st := state("Job finished"); st != "active" {
		t.Errorf("job-1: %q", st)
	}
	if st := state("Job started"); st != "" {
		t.Errorf("job-1 kept two rows")
	}
	if st := state("Temporary"); st != "dismissed" {
		t.Errorf("tmp-1: %q", st)
	}
	if st := state("Later"); st != "snoozed" {
		t.Errorf("delayed: %q", st)
	}
	var md, plain string
	s.db.QueryRow(`SELECT format FROM notifications WHERE message = '**bold** news'`).Scan(&md)
	s.db.QueryRow(`SELECT format FROM notifications WHERE message = 'Hello anyone'`).Scan(&plain)
	if md != FormatMarkdown || plain != FormatText {
		t.Errorf("formats: markdown %q, plain %q", md, plain)
	}

	// The uploaded file is copied and served; the external link is kept.
	var list struct{ Notifications []Notification }
	for _, topic := range []string{"alerts", "news"} {
		var l struct{ Notifications []Notification }
		_, b := bearer(t, "GET", u+"/api/v1/notifications?topic="+topic, boss, "")
		json.Unmarshal([]byte(b), &l)
		list.Notifications = append(list.Notifications, l.Notifications...)
	}
	var got, ext bool
	for _, n := range list.Notifications {
		if n.Attachment == nil {
			continue
		}
		if n.Attachment.Name == "report.txt" {
			if c, body := bearer(t, "GET", u+n.Attachment.URL, boss, ""); c != 200 || body != "hello from ntfy" {
				t.Errorf("file: %d %q", c, body)
			}
			got = true
		}
		ext = ext || n.Attachment.URL == "https://example.com/big.zip"
	}
	if !got || !ext {
		t.Errorf("attachments: file %v, external %v", got, ext)
	}

	// since=<ntfy message id> carries on after that message.
	_, b := bearer(t, "GET", u+"/alerts/json?poll=1&since=LVug8WgNyNyo", boss, "")
	if strings.Contains(b, "Disk is at 95%") || !strings.Contains(b, "Job finished") {
		t.Errorf("since=mid: %s", b)
	}
}

func TestNtfyImportClearsOld(t *testing.T) {
	s, _, _, _ := importFixture(t, NtfyImport{ClearOlderThan: 1})
	var open int
	s.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE state = 'active'`).Scan(&open)
	if open != 0 {
		t.Fatalf("%d still active", open)
	}
}

// An import replaces anonymous access set before it, so revoking the one
// the admin API shows leaves no other anonymous token live.
func TestNtfyImportReplacesAnonymous(t *testing.T) {
	var old string
	s, u, _, _ := importFixture(t, NtfyImport{}, func(s *Server, u, admin string) {
		req, _ := http.NewRequest("PUT", u+"/api/v1/ntfy/anonymous", strings.NewReader(`{"grants":[{"topic":"up*","scopes":["publish"]}]}`))
		req.Header.Set("Authorization", "Bearer "+admin)
		if c, b := send(t, req); c != 200 {
			t.Fatalf("set: %d %s", c, b)
		}
		s.db.QueryRow(`SELECT id FROM tokens WHERE ` + anonymousWhere).Scan(&old)
	})
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE id = ?`, old).Scan(&n)
	if old == "" || n != 0 {
		t.Fatalf("anonymous token %q set before the import is still there", old)
	}
	if _, ok := s.revoked.Load(old); !ok {
		t.Errorf("old anonymous token %q not revoked", old)
	}
	if c, _ := bearer(t, "POST", u+"/up1", "", "hi"); c == 200 {
		t.Errorf("old anonymous grant still publishes")
	}
}

func TestGrantsRejectBadPatterns(t *testing.T) {
	if _, _, err := ntfyGrants([]ntfyRule{{topic: "a/b", read: true}}, nil, nil); err == nil {
		t.Fatal("bad pattern imported")
	}
}

func TestNtfyIgnoredLog(t *testing.T) {
	s, ts, admin := newTest(t)
	pub := mkToken(t, s, "pub", []string{ScopePublish, ScopeRead}, nil)
	raw(t, "POST", ts.URL+"/alerts", pub, "hi", map[string]string{"Markdown": "yes", "X-Email": "a@example.com"})
	raw(t, "POST", ts.URL+"/alerts?md=1", pub, "hi", nil)
	raw(t, "POST", ts.URL+"/", pub, `{"topic":"alerts","message":"hi","markdown":true,"call":"yes","<b>":1}`, nil)
	c, b := bearer(t, "GET", ts.URL+"/api/v1/ntfy/ignored", admin, "")
	for _, want := range []string{`"name":"Email","count":1`, `"json:call"`, `"json:(other)"`} {
		if c != 200 || !strings.Contains(b, want) {
			t.Errorf("lacks %s: %d %s", want, c, b)
		}
	}
	if c, _ := bearer(t, "GET", ts.URL+"/api/v1/ntfy/ignored", pub, ""); c != 403 {
		t.Errorf("non-admin: %d", c)
	}
}

// TestNtfyImportRealCLI drives the real ntfy v2.38.1 client against the
// imported server with ntfy's old credentials. NTFY_BIN points at it; CI
// downloads it, so there a missing binary fails instead of skipping.
func TestNtfyImportRealCLI(t *testing.T) {
	bin := os.Getenv("NTFY_BIN")
	if bin == "" {
		if os.Getenv("CI") == "true" {
			t.Fatal("NTFY_BIN not set in CI")
		}
		t.Skip("NTFY_BIN not set")
	}
	_, u, toks, _ := importFixture(t, NtfyImport{})
	home := t.TempDir()
	ntfy := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = []string{"HOME=" + home} // no client.yml from the machine running the test
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := ntfy("publish", "--token", toks["pixel"], u+"/alerts", "from the cli"); err != nil {
		t.Fatalf("publish with tk_ token: %v %s", err, out)
	}
	if out, err := ntfy("subscribe", "--poll", "-u", "phone:phonepass", u+"/alerts"); err != nil || !strings.Contains(out, "from the cli") {
		t.Fatalf("subscribe with password: %v %s", err, out)
	}
	if out, err := ntfy("publish", "-u", "phone:phonepass", u+"/drop-cli", "write only"); err != nil {
		t.Fatalf("publish write-only: %v %s", err, out)
	}
	if out, err := ntfy("subscribe", "--poll", "--token", toks["pixel"], u+"/drop-cli"); err == nil || strings.Contains(out, "write only") {
		t.Fatalf("read a write-only topic: %v %s", err, out)
	}
	if out, err := ntfy("publish", u+"/public", "anyone"); err != nil {
		t.Fatalf("anonymous publish: %v %s", err, out)
	}
	if out, err := ntfy("subscribe", "--poll", u+"/public"); err != nil || !strings.Contains(out, "anyone") {
		t.Fatalf("anonymous subscribe: %v %s", err, out)
	}
	if out, err := ntfy("subscribe", "--poll", "-u", "phone:wrong", u+"/alerts"); err == nil {
		t.Fatalf("wrong password accepted: %s", out)
	}
}

func TestNtfyMIDRejectsPaths(t *testing.T) {
	for _, mid := range []string{"../user.db", "a/b", "", "..", "WkNQimnAbetP/"} {
		if ntfyMID.MatchString(mid) {
			t.Errorf("%q taken as a message id", mid)
		}
	}
	if !ntfyMID.MatchString("WkNQimnAbetP") {
		t.Error("real id refused")
	}
}

// Imported events carry old times; after live ones a prune would drop the
// live ones too (#388), so a used database is refused.
func TestNtfyImportNeedsFreshDB(t *testing.T) {
	s, _, _ := newTest(t)
	if _, err := s.db.Exec(`INSERT INTO events (type, topic, data, created_at) VALUES ('created', 'live', '', 0)`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := NtfyImport{AuthFile: dir + "/user.db", CacheFile: dir + "/cache.db", DefaultAccess: "deny-all"}
	if err := s.ImportNtfy(context.Background(), in, io.Discard); err == nil || !strings.Contains(err.Error(), "fresh") {
		t.Fatalf("import into a used database: %v", err)
	}
	// Events pruned away, a notification left: still used.
	if _, err := s.db.Exec(`DELETE FROM events; INSERT INTO notifications (id, topic, state, created_at, updated_at) VALUES ('n1', 'live', 'active', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportNtfy(context.Background(), in, io.Discard); err == nil || !strings.Contains(err.Error(), "fresh") {
		t.Fatalf("import with notifications but no events: %v", err)
	}
}
