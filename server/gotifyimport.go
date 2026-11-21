package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// GotifyImport says what `clrd import gotify` reads (issue #116).
type GotifyImport struct {
	DB             string        // Gotify's database (data/gotify.db)
	ClearOlderThan time.Duration // messages older than this arrive dismissed; 0 keeps them all open
	DryRun         bool          // report only, change nothing
}

type gotifySrcApp struct{ topic, name string }

// ImportGotify copies Gotify's apps and messages into this server in one
// transaction. Each app becomes a topic and a Gotify app key for it, so a
// sender keeps its URL and token; each message arrives on its app's topic.
// It runs once per database.
func (s *Server) ImportGotify(ctx context.Context, in GotifyImport, out io.Writer) error {
	src, err := openRO(in.DB)
	if err != nil {
		return fmt.Errorf("gotify database: %w", err)
	}
	defer src.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int64
	if tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'gotify_imported'`).Scan(&done) == nil {
		return fmt.Errorf("this database already has a Gotify import (%s); start from a fresh one", fromMS(done).Format(time.RFC3339))
	}
	// Imported events carry Gotify's old times, and a prune drops every event
	// up to the newest old one, so they must not sit after live ones. A fresh
	// database also keeps app topics from landing in existing feeds.
	var has int
	if tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM events)`).Scan(&has); has == 1 {
		return fmt.Errorf("this database already has notifications; import into a fresh one (a new CLRD_DB)")
	}
	rep := &report{w: out}
	if in.DryRun {
		rep.line("Dry run: nothing is written.")
	}
	apps, err := importGotifyApps(ctx, tx, src, rep)
	if err != nil {
		return err
	}
	maxSeq, err := s.importGotifyMessages(ctx, tx, src, in, apps, rep)
	if err != nil {
		return err
	}
	rep.skip(count(ctx, src, `SELECT COUNT(*) FROM users`), "users (make clrd tokens or accounts for the people who read)")
	rep.skip(count(ctx, src, `SELECT COUNT(*) FROM clients`), "client tokens (sign each phone and browser in to clrd)")
	rep.skip(count(ctx, src, `SELECT COUNT(*) FROM plugin_confs WHERE enabled`), "enabled plugins")
	if in.DryRun {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('gotify_imported', ?)`, ms(now())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if maxSeq > 0 {
		s.seq.Store(maxSeq)
	}
	rep.line("Import done.")
	return nil
}

// importGotifyApps makes each Gotify app a publish token for its own topic,
// keyed by the token as Gotify stores it (see gotifyStored), plus its
// gotify_apps row for the default priority.
func importGotifyApps(ctx context.Context, tx *sql.Tx, src *sql.DB, rep *report) (map[int64]gotifySrcApp, error) {
	prio := "NULL" // Gotify before 2.2 has no default priority
	if count(ctx, src, `SELECT COUNT(*) FROM pragma_table_info('applications') WHERE name = 'default_priority'`) == 1 {
		prio = "default_priority"
	}
	rows, err := src.QueryContext(ctx, `SELECT id, token, name, `+prio+` FROM applications ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("gotify database: %w", err)
	}
	defer rows.Close()
	apps, used := map[int64]gotifySrcApp{}, map[string]bool{}
	noKey := 0
	for rows.Next() {
		var id int64
		var key, name sql.NullString
		var def sql.NullInt64
		if err := rows.Scan(&id, &key, &name, &def); err != nil {
			return nil, err
		}
		app := gotifySrcApp{topic: gotifySlug(name.String, id, used), name: strings.TrimSpace(name.String)}
		if app.name == "" {
			app.name = app.topic
		}
		apps[id] = app
		if key.String == "" {
			noKey++
			continue
		}
		t := &Token{ID: randomString(12), Name: app.name, Scopes: []string{ScopePublish}, Topics: []string{app.topic}, CreatedAt: now()}
		if err := insertToken(ctx, tx, t, hashToken(key.String)); err != nil {
			return nil, fmt.Errorf("app %d: %w", id, err)
		}
		var p *int
		if def.Valid { // Gotify takes -1 too; gotifyPriority maps any number
			v := int(def.Int64)
			p = &v
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO gotify_apps (token_id, priority) VALUES (?, ?)`, t.ID, p); err != nil {
			return nil, fmt.Errorf("app %d: %w", id, err)
		}
		rep.line("app %q: topic %s, its token keeps working", app.name, app.topic)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep.line("Apps: %d imported.", len(apps))
	rep.skip(noKey, "app tokens that were empty (their messages are still copied)")
	return apps, nil
}

// importGotifyMessages copies every message, oldest first, as a created
// event at Gotify's time on its app's topic.
func (s *Server) importGotifyMessages(ctx context.Context, tx *sql.Tx, src *sql.DB, in GotifyImport, apps map[int64]gotifySrcApp, rep *report) (maxSeq int64, err error) {
	rows, err := src.QueryContext(ctx, `SELECT application_id, message, title, priority, extras, date FROM messages ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("gotify database: %w", err)
	}
	defer rows.Close()
	cutoff := time.Time{}
	if in.ClearOlderThan > 0 {
		cutoff = now().Add(-in.ClearOlderThan)
	}
	var copied, old, orphans, badExtras int
	for rows.Next() {
		var appid int64
		var msg, title sql.NullString
		var prio sql.NullInt64
		var extras []byte
		var date any
		if err := rows.Scan(&appid, &msg, &title, &prio, &extras, &date); err != nil {
			return 0, err
		}
		app, ok := apps[appid]
		if !ok {
			orphans++
			continue
		}
		t := gotifyTime(date)
		n := &Notification{ID: s.newID(t), Topic: app.topic, Title: title.String, Message: msg.String, Format: FormatText,
			Priority: gotifyPriority(int(prio.Int64)), Tags: []string{}, Actions: []Action{}, State: Active, CreatedAt: t}
		if strings.TrimSpace(n.Title) == "" {
			n.Title = app.name
		}
		var ex map[string]any
		if len(extras) > 0 && json.Unmarshal(extras, &ex) != nil {
			badExtras++
		}
		var md bool
		md, n.Click, n.Attachment = gotifyExtras(ex)
		if md {
			n.Format = FormatMarkdown
		}
		if err := importEvent(ctx, tx, EventCreated, n, t, ""); err != nil {
			return 0, err
		}
		if !cutoff.IsZero() && t.Before(cutoff) {
			n.State, n.StateBy = Dismissed, "gotify import"
			if err := importEvent(ctx, tx, EventState, n, now(), "gotify_import"); err != nil {
				return 0, err
			}
			old++
		}
		maxSeq = n.Seq
		copied++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rep.line("Messages: %d copied, %d dismissed as older than %s.", copied, old, in.ClearOlderThan)
	rep.skip(orphans, "messages of apps that no longer exist")
	rep.skip(badExtras, "extras clrd cannot read (the messages are copied without them)")
	return maxSeq, nil
}

// gotifySlug names an app's topic: its name in lower case, with runs of
// anything a topic cannot hold made one "-", and a number added when two
// apps would share it.
func gotifySlug(name string, id int64, used map[string]bool) string {
	base := strings.Join(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}), "-")
	if base = strings.Trim(base[:min(len(base), 56)], "-"); base == "" {
		base = fmt.Sprintf("app-%d", id)
	}
	if reserved[base] {
		base += "-app"
	}
	t := base
	for i := 2; used[t]; i++ {
		t = fmt.Sprintf("%s-%d", base, i)
	}
	used[t] = true
	return t
}

// gotifyTime reads Gotify's date column, which the driver may hand over as
// a time or as SQLite text.
func gotifyTime(v any) time.Time {
	if b, ok := v.([]byte); ok {
		v = string(b)
	}
	switch d := v.(type) {
	case time.Time:
		return d.UTC()
	case string:
		for _, l := range []string{"2006-01-02 15:04:05.999999999-07:00", time.RFC3339Nano, "2006-01-02 15:04:05"} {
			if t, err := time.Parse(l, d); err == nil {
				return t.UTC()
			}
		}
	}
	return now()
}
