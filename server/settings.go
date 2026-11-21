package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Message formats (spec, Message formatting).
const (
	FormatMarkdown = "markdown"
	FormatText     = "text"
)

// publishFormat picks a notification's format: format if given, else
// ntfy's markdown flag, else Markdown.
func publishFormat(format string, markdown *bool) (string, error) {
	switch {
	case format == FormatMarkdown || format == FormatText:
		return format, nil
	case format != "":
		return "", errf(400, "bad_request", `format must be "markdown" or "text"`)
	case markdown != nil && !*markdown:
		return FormatText, nil
	}
	return FormatMarkdown, nil
}

// ntfyMarkdown reads X-Markdown: yes/true/1 or no/false/0; anything else
// (or nothing) leaves the default.
func ntfyMarkdown(v string) *bool {
	var b bool
	switch strings.ToLower(v) {
	case "yes", "true", "1":
		b = true
	case "no", "false", "0":
	default:
		return nil
	}
	return &b
}

// Settings are the display choices every device follows (spec, Message
// formatting). One set per server: tokens are devices, not users.
type Settings struct {
	RenderFormatting bool `json:"render_formatting"`
	Autolink         bool `json:"autolink"`
	Favicons         bool `json:"favicons"`
	LinkPreviews     bool `json:"link_previews"`
	// TopicPreviews overrides LinkPreviews for one topic.
	TopicPreviews map[string]bool `json:"topic_previews"`
	// SnoozeIntervals are the choices in a Snooze menu, as Go durations
	// ("30m", "4h"). SnoozeDefault is one of them: a plain tap uses it.
	SnoozeIntervals []string `json:"snooze_intervals"`
	SnoozeDefault   string   `json:"snooze_default"`
	// KeepClearedDays is how long a dismissed or resolved notification is
	// kept, counted from when it was cleared; 0 keeps it forever. Stored
	// only once set; until then CLRD_RETENTION decides, and reads show
	// that in whole days (rounded up).
	KeepClearedDays *int `json:"keep_cleared_days"`
	// Sound is the asking device's own sound (#282), filled on reads and
	// set with PUT /api/v1/devices/{id}.
	Sound string `json:"sound,omitempty"`
}

// maxKeepDays is the longest keep_cleared_days a number can ask for (100 years).
const maxKeepDays = 36500

// keepCleared is how long cleared notifications are kept (0 = forever).
func (s *Server) keepCleared(st Settings) time.Duration {
	if st.KeepClearedDays == nil {
		return s.opt.Retention
	}
	return time.Duration(*st.KeepClearedDays) * 24 * time.Hour
}

// prunable picks the cleared notifications prune deletes: final, and last
// changed before the cutoff. The Settings preview counts the same rows.
const prunable = `state IN ('dismissed', 'resolved') AND updated_at < ?`

// WouldRemove counts the cleared notifications tok can read that a keep of
// days would delete on the next prune run (0 days = forever, so none).
func (s *Server) WouldRemove(ctx context.Context, tok *Token, days int) (int, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return 0, err
	}
	if days < 0 || days > maxKeepDays {
		return 0, errf(400, "bad_request", "keep_cleared_days must be 0 (forever) to %d", maxKeepDays)
	}
	if days == 0 {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT topic, COUNT(*) FROM notifications WHERE `+prunable+` GROUP BY topic`,
		ms(time.Now().Add(-time.Duration(days)*24*time.Hour)))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var topic string
		var k int
		if err := rows.Scan(&topic, &k); err != nil {
			return 0, err
		}
		if tok.may(ScopeRead, topic) {
			n += k
		}
	}
	return n, rows.Err()
}

// maxSnooze matches the web form's limit on a snooze duration.
const maxSnooze = 31 * 24 * time.Hour

// checkSnooze validates a snooze list and its default: 1 to 8 different
// durations, each from 1 minute to 31 days, and the default one of them.
func checkSnooze(list []string, def string) error {
	if len(list) < 1 || len(list) > 8 {
		return errf(400, "bad_request", "snooze_intervals needs 1 to 8 durations")
	}
	seen := map[time.Duration]bool{}
	for _, v := range list {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute || d > maxSnooze {
			return errf(400, "bad_request", "snooze interval %q must be a duration like 30m or 4h, from 1m to 744h", v)
		}
		if seen[d] {
			return errf(400, "bad_request", "snooze interval %q is listed twice", v)
		}
		seen[d] = true
	}
	if !slices.Contains(list, def) {
		return errf(400, "bad_request", "snooze_default %q must be one of snooze_intervals", def)
	}
	return nil
}

func defaultSettings() Settings {
	return Settings{RenderFormatting: true, Autolink: true, Favicons: true, TopicPreviews: map[string]bool{},
		SnoozeIntervals: []string{"1h", "4h"}, SnoozeDefault: "1h"}
}

// SettingsPatch is a PUT body: only the fields given change. A topic
// mapped to null drops its override.
type SettingsPatch struct {
	RenderFormatting *bool            `json:"render_formatting"`
	Autolink         *bool            `json:"autolink"`
	Favicons         *bool            `json:"favicons"`
	LinkPreviews     *bool            `json:"link_previews"`
	TopicPreviews    map[string]*bool `json:"topic_previews"`
	SnoozeIntervals  []string         `json:"snooze_intervals"`
	SnoozeDefault    *string          `json:"snooze_default"`
	KeepClearedDays  *int             `json:"keep_cleared_days"`
	Sound            *string          `json:"sound"` // this device's own sound
}

// Settings returns the display settings as tok may see them: a token
// limited to some topics only sees those topics' overrides.
func (s *Server) Settings(ctx context.Context, tok *Token) (Settings, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return Settings{}, err
	}
	st, err := s.loadSettings(ctx, s.db)
	if err != nil {
		return st, err
	}
	for t := range st.TopicPreviews {
		if !tok.Allows(t) {
			delete(st.TopicPreviews, t)
		}
	}
	if st.KeepClearedDays == nil { // filled on reads only, so a save never pins CLRD_RETENTION
		d := int((s.opt.Retention + 24*time.Hour - 1) / (24 * time.Hour))
		st.KeepClearedDays = &d
	}
	sounds, err := s.deviceSounds(ctx, s.db)
	st.Sound = soundOf(sounds, tok.ID)
	return st, err
}

func (s *Server) loadSettings(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (Settings, error) {
	st := defaultSettings()
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'display'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return st, err
	}
	if st.TopicPreviews == nil {
		st.TopicPreviews = map[string]bool{}
	}
	return st, nil
}

// UpdateSettings applies p and tells every live stream. Read scope is
// enough (it is the user's own display). The four switches need a token
// with no topic list; a topic override needs the topic.
func (s *Server) UpdateSettings(ctx context.Context, tok *Token, p SettingsPatch) (Settings, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return Settings{}, err
	}
	global := p.RenderFormatting != nil || p.Autolink != nil || p.Favicons != nil || p.LinkPreviews != nil ||
		p.SnoozeIntervals != nil || p.SnoozeDefault != nil || p.KeepClearedDays != nil || p.Sound != nil
	if global && tok.limited() {
		return Settings{}, errf(403, "forbidden", "a token limited to some topics can only change topic_previews")
	}
	if p.KeepClearedDays != nil { // it deletes data, so admin only
		if err := tok.need(ScopeAdmin, ""); err != nil {
			return Settings{}, err
		}
		if *p.KeepClearedDays < 0 || *p.KeepClearedDays > maxKeepDays {
			return Settings{}, errf(400, "bad_request", "keep_cleared_days must be 0 (forever) to %d", maxKeepDays)
		}
	}
	for t := range p.TopicPreviews {
		if err := validTopic(t); err != nil {
			return Settings{}, err
		}
		if !tok.Allows(t) {
			return Settings{}, errf(403, "forbidden", "token may not use topic %q", t)
		}
	}
	if p.Sound != nil && !slices.Contains(soundNames, *p.Sound) { // checked now, set once the rest is saved
		return Settings{}, errf(400, "bad_request", "unknown sound %q, use one of: %s", *p.Sound, strings.Join(soundNames, ", "))
	}
	s.mu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return Settings{}, err
	}
	defer tx.Rollback()
	st, err := s.loadSettings(ctx, tx)
	if err == nil {
		for _, f := range []struct {
			from *bool
			to   *bool
		}{{p.RenderFormatting, &st.RenderFormatting}, {p.Autolink, &st.Autolink}, {p.Favicons, &st.Favicons}, {p.LinkPreviews, &st.LinkPreviews}} {
			if f.from != nil {
				*f.to = *f.from
			}
		}
		for t, v := range p.TopicPreviews {
			if v == nil {
				delete(st.TopicPreviews, t)
			} else {
				st.TopicPreviews[t] = *v
			}
		}
		if p.SnoozeIntervals != nil {
			st.SnoozeIntervals = p.SnoozeIntervals
			if p.SnoozeDefault == nil && len(st.SnoozeIntervals) > 0 && !slices.Contains(st.SnoozeIntervals, st.SnoozeDefault) {
				st.SnoozeDefault = st.SnoozeIntervals[0] // the old default left the list
			}
		}
		if p.SnoozeDefault != nil {
			st.SnoozeDefault = *p.SnoozeDefault
		}
		if p.SnoozeIntervals != nil || p.SnoozeDefault != nil {
			err = checkSnooze(st.SnoozeIntervals, st.SnoozeDefault)
		}
		if p.KeepClearedDays != nil {
			st.KeepClearedDays = p.KeepClearedDays
		}
	}
	if err == nil {
		raw, _ := json.Marshal(st)
		if _, err = tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('display', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(raw)); err == nil {
			err = tx.Commit()
		}
	}
	s.mu.Unlock()
	if err == nil && p.Sound != nil {
		_, err = s.SetSound(ctx, tok, tok.ID, *p.Sound)
	}
	if err != nil {
		return Settings{}, err
	}
	s.settingsChanged.fire()
	return s.Settings(ctx, tok)
}

// previewsOn reports whether link previews are on for topic ("" = no topic).
func (st Settings) previewsOn(topic string) bool {
	if v, ok := st.TopicPreviews[topic]; ok && topic != "" {
		return v
	}
	return st.LinkPreviews
}

// signal wakes every waiter once per fire: wait returns a channel that
// closes on the next fire.
type signal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (g *signal) wait() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch == nil {
		g.ch = make(chan struct{})
	}
	return g.ch
}

func (g *signal) fire() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch != nil {
		close(g.ch)
		g.ch = nil
	}
}

// settingsMsg is the stream message carrying tok's view of the settings.
func (s *Server) settingsMsg(ctx context.Context, tok *Token) ([]byte, error) {
	st, err := s.Settings(ctx, tok)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type     string   `json:"type"`
		Settings Settings `json:"settings"`
	}{"settings", st})
}

func (s *Server) settingsRoutes() {
	s.api("GET /api/v1/settings", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		st, err := s.Settings(r.Context(), tok)
		if err != nil {
			return err
		}
		writeJSON(w, 200, st)
		return nil
	})
	s.api("PUT /api/v1/settings", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var p SettingsPatch
		if err := decodeJSON(r, &p); err != nil {
			return err
		}
		st, err := s.UpdateSettings(r.Context(), tok, p)
		if err != nil {
			return err
		}
		writeJSON(w, 200, st)
		return nil
	})
}
