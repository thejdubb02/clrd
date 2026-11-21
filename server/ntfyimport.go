package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// NtfyImport says what `clrd import ntfy` reads (issue #117).
type NtfyImport struct {
	AuthFile       string        // ntfy auth-file (user.db); "" skips users and tokens
	CacheFile      string        // ntfy cache-file (cache.db); "" skips messages
	AttachmentDir  string        // ntfy attachment-cache-dir; "" drops uploaded files
	DefaultAccess  string        // ntfy auth-default-access: read-write, read-only, write-only or deny-all
	ClearOlderThan time.Duration // messages older than this arrive dismissed; 0 keeps them all open
	DryRun         bool          // report only, change nothing
}

var ntfyDefaults = map[string][]string{
	"read-write": {ScopeRead, ScopePublish},
	"read-only":  {ScopeRead},
	"write-only": {ScopePublish},
	"deny-all":   nil,
}

type ntfyRule struct {
	topic       string // ntfy form: % for *, \_ for _
	read, write bool
	everyone    bool
	reservation bool
}

// grant turns a rule into a clrd grant. A rule clrd cannot express fails
// the whole import: dropping a deny would widen access.
func (r ntfyRule) grant() (Grant, error) {
	t := strings.ReplaceAll(strings.ReplaceAll(r.topic, `\_`, "_"), "%", "*")
	if !grantPattern.MatchString(t) {
		return Grant{}, fmt.Errorf("ntfy access rule on %q cannot be imported", r.topic)
	}
	g := Grant{Topic: t, Scopes: []string{}}
	if r.read {
		g.Scopes = append(g.Scopes, ScopeRead)
	}
	if r.write {
		g.Scopes = append(g.Scopes, ScopePublish)
	}
	return g, nil
}

// ntfyGrants orders rules the way ntfy checks them: the user's own, then
// everyone's, each longest topic first and write before read; then the
// default for every other topic.
func ntfyGrants(own, everyone []ntfyRule, def []string) ([]Grant, []string, error) {
	var out []Grant
	for _, rules := range [][]ntfyRule{own, everyone} {
		rules = slices.Clone(rules)
		sort.SliceStable(rules, func(i, j int) bool {
			if len(rules[i].topic) != len(rules[j].topic) {
				return len(rules[i].topic) > len(rules[j].topic)
			}
			return rules[i].write && !rules[j].write
		})
		for _, r := range rules {
			g, err := r.grant()
			if err != nil {
				return nil, nil, err
			}
			out = append(out, g)
		}
	}
	if len(def) > 0 {
		out = append(out, Grant{Topic: "*", Scopes: def})
	}
	var scopes []string
	for _, g := range out {
		for _, sc := range g.Scopes {
			if !slices.Contains(scopes, sc) {
				scopes = append(scopes, sc)
			}
		}
	}
	return out, scopes, nil
}

func openRO(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro&_pragma=busy_timeout(5000)")
}

// ImportNtfy copies ntfy's users, tokens, access rules, messages and
// attachments into this server in one transaction, and writes a report of
// what mapped and what did not to out. It runs once per database.
func (s *Server) ImportNtfy(ctx context.Context, in NtfyImport, out io.Writer) (err error) {
	def, ok := ntfyDefaults[in.DefaultAccess]
	if !ok {
		return fmt.Errorf("--default-access must be read-write, read-only, write-only or deny-all (ntfy's auth-default-access)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int64
	if tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'ntfy_imported'`).Scan(&done) == nil {
		return fmt.Errorf("this database already has an ntfy import (%s); start from a fresh one", fromMS(done).Format(time.RFC3339))
	}
	// Imported events carry ntfy's old times, and a prune drops every event
	// up to the newest old one, so they must not sit after live ones (#388).
	// Notifications too: a prune can empty events and leave them.
	var has int
	if tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM events) OR EXISTS (SELECT 1 FROM notifications)`).Scan(&has); has == 1 {
		return fmt.Errorf("this database already has notifications; import into a fresh one (a new CLRD_DB)")
	}
	var copied []string // files to remove if the import does not commit
	defer func() {
		if err != nil || in.DryRun {
			for _, f := range copied {
				os.Remove(f)
			}
		}
	}()
	rep := &report{w: out}
	if in.DryRun {
		rep.line("Dry run: nothing is written.")
	}
	var gone []string
	if in.AuthFile != "" {
		// ntfy's anonymous access replaces any set here before (#375), so
		// no older anonymous token stays live beside the imported one.
		if gone, err = dropAnonymous(ctx, tx); err != nil {
			return err
		}
		if err := s.importNtfyUsers(ctx, tx, in, def, rep); err != nil {
			return err
		}
	}
	var maxSeq int64
	if in.CacheFile != "" {
		if maxSeq, copied, err = s.importNtfyMessages(ctx, tx, in, rep); err != nil {
			return err
		}
	}
	if in.DryRun {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('ntfy_imported', ?)`, ms(now())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.revokeAll(gone)
	if maxSeq > 0 {
		s.seq.Store(maxSeq)
	}
	rep.line("Import done.")
	return nil
}

type report struct{ w io.Writer }

func (r *report) line(format string, args ...any) { fmt.Fprintf(r.w, format+"\n", args...) }

func (r *report) skip(n int, what string) {
	if n > 0 {
		r.line("  not imported: %d %s", n, what)
	}
}

func count(ctx context.Context, db *sql.DB, q string) int {
	var n int
	db.QueryRowContext(ctx, q).Scan(&n) // a table older ntfy lacks counts as 0
	return n
}

func (s *Server) importNtfyUsers(ctx context.Context, tx *sql.Tx, in NtfyImport, def []string, rep *report) error {
	src, err := openRO(in.AuthFile)
	if err != nil {
		return fmt.Errorf("auth file: %w", err)
	}
	defer src.Close()
	type user struct {
		id, name, pass, role string
		rules                []ntfyRule
	}
	users := map[string]*user{}
	var order []string
	rows, err := src.QueryContext(ctx, `SELECT id, user, pass, role FROM user WHERE deleted IS NULL ORDER BY user`)
	if err != nil {
		return fmt.Errorf("auth file: %w", err)
	}
	for rows.Next() {
		u := &user{}
		if err := rows.Scan(&u.id, &u.name, &u.pass, &u.role); err != nil {
			rows.Close()
			return err
		}
		users[u.id] = u
		order = append(order, u.id)
	}
	rows.Close()
	var everyone []ntfyRule
	reservations := 0
	rows, err = src.QueryContext(ctx, `SELECT user_id, topic, read, write, owner_user_id IS NOT NULL FROM user_access`)
	if err != nil {
		return fmt.Errorf("auth file: %w", err)
	}
	for rows.Next() {
		var uid string
		var r ntfyRule
		if err := rows.Scan(&uid, &r.topic, &r.read, &r.write, &r.reservation); err != nil {
			rows.Close()
			return err
		}
		if r.reservation {
			reservations++
		}
		if u := users[uid]; u != nil && u.role == "anonymous" {
			everyone = append(everyone, r)
		} else if u != nil {
			u.rules = append(u.rules, r)
		}
	}
	rows.Close()

	rep.line("Users and tokens (default access for other topics: %s):", in.DefaultAccess)
	made := map[string]*Token{} // ntfy user id -> its token, for its tk_ tokens to copy
	var noAccess []string
	for _, id := range order {
		u := users[id]
		if u.role == "anonymous" {
			continue
		}
		t := &Token{Name: u.name, ID: randomString(12), CreatedAt: now()}
		if u.role == "admin" {
			t.Scopes = []string{ScopeAdmin}
		} else if t.Grants, t.Scopes, err = ntfyGrants(u.rules, everyone, def); err != nil {
			return err
		}
		if len(t.Scopes) == 0 {
			noAccess = append(noAccess, u.name)
			continue
		}
		made[id] = t
		if !strings.HasPrefix(u.pass, "$2") {
			rep.line("  user %s: no bcrypt password, tokens only", u.name)
			continue
		}
		if err := insertToken(ctx, tx, t, hashToken(tokenPrefix+randomString(32))); err != nil { // unusable secret: signs in by password only
			return fmt.Errorf("user %s: %w", u.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (name, hash, token_id) VALUES (?,?,?)`, u.name, u.pass, t.ID); err != nil {
			return err
		}
		rep.line("  user %s: password kept, %s", u.name, describe(t))
	}
	if len(noAccess) > 0 {
		rep.line("  not imported: users with no access at all: %s", strings.Join(noAccess, ", "))
	}

	rows, err = src.QueryContext(ctx, `SELECT user_id, token, label, expires FROM user_token ORDER BY user_id, label`)
	if err != nil {
		return fmt.Errorf("auth file: %w", err)
	}
	expiring := 0
	type tk struct{ uid, raw, label string }
	var tks []tk
	for rows.Next() {
		var t tk
		var expires int64
		if err := rows.Scan(&t.uid, &t.raw, &t.label, &expires); err != nil {
			rows.Close()
			return err
		}
		if expires != 0 { // web app sign-ins; the phone apps' tokens never expire
			expiring++
			continue
		}
		tks = append(tks, t)
	}
	rows.Close()
	for _, k := range tks {
		owner := made[k.uid]
		if owner == nil || !strings.HasPrefix(k.raw, ntfyTokenPrefix) {
			expiring++
			continue
		}
		name := strings.TrimSpace(k.label)
		if name == "" {
			name = owner.Name
		}
		t := &Token{ID: randomString(12), Name: deviceName(name, ""), Scopes: owner.Scopes, Grants: owner.Grants, CreatedAt: now()}
		if err := insertToken(ctx, tx, t, hashToken(k.raw)); err != nil {
			return fmt.Errorf("token %s: %w", name, err)
		}
		rep.line("  token %s (user %s): kept", t.Name, users[k.uid].name)
	}
	rep.skip(expiring, "expiring or orphaned tokens (web app sign-ins; sign in again)")

	if grants, scopes, err := ntfyGrants(nil, everyone, def); err != nil {
		return err
	} else if len(scopes) > 0 {
		t := &Token{ID: anonymousID, Name: "anonymous", Scopes: scopes, Grants: grants, CreatedAt: now()}
		if err := insertToken(ctx, tx, t, hashToken(tokenPrefix+randomString(32))); err != nil {
			return fmt.Errorf("anonymous: %w", err)
		}
		rep.line("  anonymous (no credentials, ntfy routes only): %s", describe(t))
	} else {
		rep.line("  anonymous: no access, as in ntfy")
	}
	rep.skip(count(ctx, src, `SELECT COUNT(*) FROM tier`), "tiers (no limits per user in clrd)")
	rep.skip(reservations, "topic reservations (kept as access rules, not as ownership)")
	rep.skip(count(ctx, src, `SELECT COUNT(*) FROM user_phone`), "phone numbers (no calls)")
	rep.skip(count(ctx, src, `SELECT COUNT(*) FROM user_email`), "email addresses (no email)")
	rep.skip(count(ctx, src, `SELECT COUNT(*) FROM user WHERE stripe_customer_id IS NOT NULL AND stripe_customer_id != ''`), "billing accounts (no billing)")
	rep.line("  not imported: web push subscriptions (ntfy keeps them in a separate file; browsers subscribe again)")
	return nil
}

// describe says what a token may do, for the report.
func describe(t *Token) string {
	if len(t.Grants) == 0 {
		return strings.Join(t.Scopes, ",") + " on every topic"
	}
	parts := make([]string, len(t.Grants))
	for i, g := range t.Grants {
		sc := strings.Join(g.Scopes, "+")
		if sc == "" {
			sc = "deny"
		}
		parts[i] = g.Topic + "=" + sc
	}
	return strings.Join(parts, " ")
}

// importNtfyMessages replays ntfy's cache in time order: each message is a
// created or updated event, a clear is acknowledged, a delete is dismissed.
// Every event keeps ntfy's time, and each ntfy message id maps to its seq so
// a subscriber's since=<id> carries on where it left off.
func (s *Server) importNtfyMessages(ctx context.Context, tx *sql.Tx, in NtfyImport, rep *report) (maxSeq int64, copied []string, err error) {
	src, err := openRO(in.CacheFile)
	if err != nil {
		return 0, nil, fmt.Errorf("cache file: %w", err)
	}
	defer src.Close()
	rows, err := src.QueryContext(ctx, `SELECT mid, sequence_id, time, event, topic, message, title, priority, tags, click, icon,
		actions, attachment_name, attachment_type, attachment_size, attachment_url, content_type, published
		FROM messages ORDER BY time, id`)
	if err != nil {
		return 0, nil, fmt.Errorf("cache file: %w", err)
	}
	defer rows.Close()
	open := map[[2]string]*Notification{}
	var skipped, badTopic, files, external, dropped, badActions, dismissedOld int
	cutoff := time.Time{}
	if in.ClearOlderThan > 0 {
		cutoff = now().Add(-in.ClearOlderThan)
	}
	emit := func(typ string, n *Notification, at time.Time, mid string) error {
		if err := importEvent(ctx, tx, typ, n, at, "ntfy_import"); err != nil {
			return err
		}
		maxSeq = n.Seq
		if mid == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO ntfy_mids (mid, seq) VALUES (?, ?)`, mid, n.Seq)
		return err
	}
	for rows.Next() {
		var mid, sid, event, topic, msg, title, tags, click, icon, actions, aName, aType, aURL, ctype string
		var at, prio, aSize int64
		var published bool
		if err := rows.Scan(&mid, &sid, &at, &event, &topic, &msg, &title, &prio, &tags, &click, &icon,
			&actions, &aName, &aType, &aSize, &aURL, &ctype, &published); err != nil {
			return 0, copied, err
		}
		t := fromMS(at * 1000)
		key := [2]string{topic, sid}
		n := open[key]
		switch event {
		case "message_clear", "message_delete":
			if n == nil || n.Final() {
				skipped++
				continue
			}
			n.State, n.StateBy, n.SnoozeUntil = Acknowledged, "ntfy", nil
			if event == "message_delete" {
				n.State = Dismissed
			}
			if err := emit(EventState, n, t, mid); err != nil {
				return 0, copied, err
			}
			continue
		case "message":
		default:
			skipped++
			continue
		}
		if validTopic(topic) != nil {
			badTopic++
			continue
		}
		typ := EventUpdated
		if n == nil || n.Final() {
			n = &Notification{ID: s.newID(t), Topic: topic, CreatedAt: t}
			if sid != mid {
				n.ExternalID = sid
			}
			open[key] = n
			typ = EventCreated
		}
		if prio == 0 {
			prio = 3
		}
		n.Title, n.Message, n.Priority, n.Tags, n.Click, n.Icon = title, msg, int(prio), splitList(tags), click, ntfyIcon(icon)
		if n.Actions, err = ntfyActions(actions); err != nil {
			n.Actions = nil
			badActions++
		}
		if n.Actions == nil {
			n.Actions = []Action{}
		}
		n.Format = FormatText // as ntfy showed it
		if ctype == "text/markdown" {
			n.Format = FormatMarkdown
		}
		n.Attachment, n.file = nil, ""
		if aName != "" {
			src := filepath.Join(in.AttachmentDir, mid)
			// ntfy names the file by message id; anything else, or a symlink, is not its file.
			if st, err := os.Lstat(src); in.AttachmentDir != "" && ntfyMID.MatchString(mid) && err == nil && st.Mode().IsRegular() {
				id := randomHex()
				if !in.DryRun {
					dst := filepath.Join(s.opt.Attachments, id)
					if err := copyFile(src, dst); err != nil {
						return 0, copied, fmt.Errorf("attachment %s: %w", mid, err)
					}
					copied = append(copied, dst)
				}
				n.Attachment = &Attachment{Name: cleanName(aName), URL: "/api/v1/files/" + id, Type: aType, Size: st.Size()}
				n.file = id
				files++
			} else if aSize == 0 && (strings.HasPrefix(aURL, "https://") || strings.HasPrefix(aURL, "http://")) {
				n.Attachment = &Attachment{Name: cleanName(aName), URL: aURL}
				external++
			} else {
				dropped++
			}
		}
		n.State, n.StateBy, n.SnoozeUntil = Active, "", nil
		if !published { // a Delay message ntfy has not sent yet
			n.State, n.SnoozeUntil = Snoozed, &t
		}
		if err := emit(typ, n, t, mid); err != nil {
			return 0, copied, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, copied, err
	}
	if !cutoff.IsZero() {
		var olds []*Notification
		for _, n := range open {
			if !n.Final() && n.State != Snoozed && n.UpdatedAt.Before(cutoff) {
				olds = append(olds, n)
			}
		}
		sort.Slice(olds, func(i, j int) bool { return olds[i].ID < olds[j].ID })
		for _, n := range olds {
			n.State, n.StateBy = Dismissed, "ntfy import"
			if err := emit(EventState, n, now(), ""); err != nil {
				return 0, copied, err
			}
			dismissedOld++
		}
	}
	active := 0
	for _, n := range open {
		if !n.Final() {
			active++
		}
	}
	rep.line("Messages: %d notifications, %d still open, %d dismissed as older than %s.", len(open), active, dismissedOld, in.ClearOlderThan)
	rep.line("  attachments: %d files copied, %d external links kept", files, external)
	rep.skip(dropped, "attachments whose file is gone (expired in ntfy, or no attachment dir given)")
	rep.skip(badTopic, "messages on topics clrd reserves or cannot name")
	rep.skip(badActions, "action lists clrd cannot read")
	rep.skip(skipped, "other events (clears of unknown messages, keepalives)")
	return maxSeq, copied, nil
}

// importEvent writes one imported event at the source's own time and saves
// n as it stands after it. A state change records via as its cause.
func importEvent(ctx context.Context, tx *sql.Tx, typ string, n *Notification, at time.Time, via string) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO events (type, topic, data, created_at) VALUES (?, ?, '', ?)`, typ, n.Topic, ms(at))
	if err != nil {
		return err
	}
	n.Seq, _ = res.LastInsertId()
	n.UpdatedAt = at
	if err := saveNotification(tx, n); err != nil {
		return err
	}
	ev := Event{Seq: n.Seq, Type: typ, Notification: n}
	if typ == EventState {
		ev.Via = via
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE events SET data = ? WHERE seq = ?`, string(raw), n.Seq)
	return err
}

func randomHex() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

var ntfyMID = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
