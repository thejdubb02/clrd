package server

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Gotify 3.1.1's tables, as its gorm migration makes them. Before 2.2 an
// application had no default_priority.
const gotifySchema = "CREATE TABLE `applications` (`id` integer PRIMARY KEY AUTOINCREMENT,`token` varchar(180),`user_id` integer,`name` text,`description` text,`internal` numeric,`image` text,`default_priority` integer,`created_at` datetime,`last_used` datetime,`sort_key` blob);" +
	"CREATE TABLE `messages` (`id` integer PRIMARY KEY AUTOINCREMENT,`application_id` integer,`message` text,`title` text,`priority` integer,`extras` blob,`date` datetime);" +
	"CREATE TABLE `users` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` varchar(180),`pass` blob,`admin` numeric,`created_at` datetime,`oidc_id` text);" +
	"CREATE TABLE `clients` (`id` integer PRIMARY KEY AUTOINCREMENT,`token` varchar(180),`user_id` integer,`name` text,`created_at` datetime,`last_used` datetime);" +
	"CREATE TABLE `plugin_confs` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer,`module_path` text,`token` varchar(180),`application_id` integer,`enabled` numeric);"

// A Gotify 3 key with the seed 0, 1, ..., 31, and the form Gotify's database
// keeps it in: the seed's ed25519 public key, worked out with openssl.
var gotifyKey3 = "gtfya." + base64.RawURLEncoding.EncodeToString([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})

const (
	gotifyStored3 = "gtfya.A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg"
	gotifyLegacy  = "AlegacyTok3n_0"
)

func gotifyFixture(t *testing.T, schema string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gotify.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range strings.SplitAfter(schema, ";") {
		if strings.TrimSpace(q) == "" {
			continue
		}
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rows := []string{
		`INSERT INTO users (name, admin) VALUES ('admin', 1), ('kid', 0)`,
		`INSERT INTO clients (token, user_id, name) VALUES ('Cclienttoken01', 1, 'phone')`,
		`INSERT INTO messages (application_id, message, title, priority, extras, date) VALUES
			(1, '**md**', 'T', 9, '{"client::display":{"contentType":"text/markdown"},"client::notification":{"click":{"url":"https://example.com/x"}}}', '2026-11-21 05:02:09.424214975+00:00'),
			(2, 'old one', '', 2, NULL, '2020-01-02 03:04:05+00:00'),
			(99, 'orphan', 'x', 5, NULL, '2026-01-01 00:00:00+00:00')`,
	}
	if strings.Contains(schema, "default_priority") {
		rows = append(rows, `INSERT INTO applications (token, user_id, name, default_priority) VALUES ('`+gotifyStored3+`', 1, 'Sonarr TV', 7), ('`+gotifyLegacy+`', 2, 'sonarr  tv!', NULL), ('Aapitoken00000', 1, 'API', 0)`)
	} else {
		rows = append(rows, `INSERT INTO applications (token, user_id, name) VALUES ('`+gotifyStored3+`', 1, 'Sonarr TV'), ('`+gotifyLegacy+`', 2, 'sonarr  tv!'), ('Aapitoken00000', 1, 'API')`)
	}
	for _, q := range rows {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

func TestGotifyImport(t *testing.T) {
	s, ts, admin := newTest(t)
	path := gotifyFixture(t, gotifySchema)
	var rep strings.Builder
	if err := s.ImportGotify(context.Background(), GotifyImport{DB: path, ClearOlderThan: 365 * 24 * time.Hour}, &rep); err != nil {
		t.Fatalf("import: %v\n%s", err, rep.String())
	}
	for _, want := range []string{`"Sonarr TV": topic sonarr-tv`, `"sonarr  tv!": topic sonarr-tv-2`, `"API": topic api-app`,
		"Messages: 2 copied, 1 dismissed", "not imported: 1 messages of apps", "not imported: 2 users", "not imported: 1 client tokens", "Import done."} {
		if !strings.Contains(rep.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, rep.String())
		}
	}

	// Messages keep Gotify's time, format, click and priority (9 is high).
	n := latest(t, ts.URL, admin, "sonarr-tv")
	if n["title"] != "T" || n["format"] != "markdown" || n["click"] != "https://example.com/x" || n["priority"] != 4.0 ||
		n["state"] != Active || !strings.HasPrefix(n["created_at"].(string), "2026-11-21T05:02:09.424") {
		t.Fatalf("imported message: %v", n)
	}
	// An untitled message takes the app name; one past --clear-older-than is dismissed.
	if n = latest(t, ts.URL, admin, "sonarr-tv-2&state=all"); n["title"] != "sonarr  tv!" || n["state"] != Dismissed || n["priority"] != 2.0 {
		t.Fatalf("old message: %v", n)
	}

	// Gotify 3's key, as the sender has it, still sends, with the app's default priority.
	post := func(key string) (int, map[string]any) {
		return gotifyPost(t, ts.URL+"/message", "application/json", `{"message":"after"}`, map[string]string{"X-Gotify-Key": key})
	}
	if code, m := post(gotifyKey3); code != 200 || m["priority"] != 7.0 || m["title"] != "Sonarr TV" || latest(t, ts.URL, admin, "sonarr-tv")["message"] != "after" {
		t.Fatalf("gtfya key: %d %v", code, m)
	}
	if code, _ := post(gotifyLegacy); code != 200 || latest(t, ts.URL, admin, "sonarr-tv-2")["message"] != "after" {
		t.Fatalf("legacy key: %d", code)
	}
	// Gotify 3's signed form works within 15 minutes of its time.
	seed, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(gotifyKey3, "gtfya."))
	sign := func(at int64) string {
		h := sha512.Sum512([]byte(fmt.Sprintf("iat=%d", at)))
		sig, _ := ed25519.NewKeyFromSeed(seed).Sign(nil, h[:], crypto.SHA512)
		return fmt.Sprintf("%s.%d.%s", gotifyStored3, at, base64.RawURLEncoding.EncodeToString(sig))
	}
	if code, _ := post(sign(time.Now().Unix() - 60)); code != 200 {
		t.Fatalf("signed key: %d", code)
	}
	fresh := sign(time.Now().Unix())
	// Change a character inside the signature, where all 6 bits count, to a
	// different one; the last ones carry pad bits, so editing them can be a no-op (#445).
	i := len(fresh) - 10
	mangled := fresh[:i] + map[bool]string{true: "B", false: "A"}[fresh[i] == 'A'] + fresh[i+1:]
	// The stored form is not a key, nor is a mangled, stale or forged one.
	for _, k := range []string{gotifyStored3, gotifyKey3 + "x", "gtfya." + strings.Repeat("A", 43) + ".x", "gtfy",
		sign(time.Now().Unix() - 20*60), mangled, strings.Replace(fresh, ".", ".1", 2)} {
		if code, _ := post(k); code != 401 {
			t.Fatalf("key %q: %d", k, code)
		}
	}

	if err := s.ImportGotify(context.Background(), GotifyImport{DB: path}, &rep); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("second import: %v", err)
	}
	var seq int64
	s.db.QueryRow(`SELECT MAX(seq) FROM events`).Scan(&seq)
	if s.seq.Load() != seq {
		t.Fatalf("seq %d, events reach %d", s.seq.Load(), seq)
	}
}

func TestGotifyImportOldAndDry(t *testing.T) {
	s, ts, admin := newTest(t)
	old := strings.Replace(gotifySchema, "`default_priority` integer,", "", 1)
	var rep strings.Builder
	if err := s.ImportGotify(context.Background(), GotifyImport{DB: gotifyFixture(t, old), DryRun: true}, &rep); err != nil {
		t.Fatalf("dry run: %v\n%s", err, rep.String())
	}
	var n int
	s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM tokens) + (SELECT COUNT(*) FROM events) + (SELECT COUNT(*) FROM gotify_apps) + (SELECT COUNT(*) FROM meta WHERE key = 'gotify_imported')`).Scan(&n)
	if n != 1 || !strings.Contains(rep.String(), "Dry run") || !strings.Contains(rep.String(), "Messages: 2 copied") {
		t.Fatalf("dry run wrote %d rows:\n%s", n, rep.String())
	}
	if err := s.ImportGotify(context.Background(), GotifyImport{DB: gotifyFixture(t, old)}, &rep); err != nil {
		t.Fatalf("import: %v\n%s", err, rep.String())
	}
	// No default priority before Gotify 2.2: clrd's.
	if code, m := gotifyPost(t, ts.URL+"/message", "application/json", `{"message":"m"}`, map[string]string{"X-Gotify-Key": gotifyKey3}); code != 200 || latest(t, ts.URL, admin, "sonarr-tv")["priority"] != 3.0 {
		t.Fatalf("old schema key: %d %v", code, m)
	}
}

func TestGotifyImportNeedsFreshDB(t *testing.T) {
	s, _, _ := newTest(t)
	if _, err := s.db.Exec(`INSERT INTO events (type, topic, data, created_at) VALUES ('created', 'live', '', 0)`); err != nil {
		t.Fatal(err)
	}
	var rep strings.Builder
	if err := s.ImportGotify(context.Background(), GotifyImport{DB: gotifyFixture(t, gotifySchema)}, &rep); err == nil || !strings.Contains(err.Error(), "fresh") {
		t.Fatalf("import into a used database: %v", err)
	}
}

func TestGotifySlug(t *testing.T) {
	used := map[string]bool{}
	for _, c := range [][2]string{{"Sonarr TV", "sonarr-tv"}, {"  sonarr--tv ", "sonarr--tv"}, {"Sonarr/TV", "sonarr-tv-2"}, {"Ünïcode", "n-code"}, {"日本", "app-5"}, {"docs", "docs-app"}, {strings.Repeat("x", 80), strings.Repeat("x", 56)}} {
		if got := gotifySlug(c[0], 5, used); got != c[1] || validTopic(got) != nil {
			t.Errorf("gotifySlug(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}
