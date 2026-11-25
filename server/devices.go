package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Linked devices (issue #246): every device, unlink from another device, and
// a log of links, sign-ins and unlinks. A full admin sees every token. Anyone
// else sees the devices under their own top tokens (every one of their user,
// #143): the token typed in, every
// browser signed in with it, every phone linked from one of those, and the
// browsers those phones approved. Only linked devices (browsers and phones)
// can be unlinked here; typed-in tokens are managed with /api/v1/tokens.

// LinkedDevice is one row of the device list. It never carries a secret or hash.
type LinkedDevice struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Detail     string     `json:"detail,omitempty"` // "Chrome 131 on Windows", to tell rows of one name apart (#765)
	Kind       string     `json:"kind"`             // token, browser, phone or agent
	Via        string     `json:"via,omitempty"`    // the token or device it hangs under: revoking that one signs this one out too
	ViaID      string     `json:"via_id,omitempty"` // its id, since names can repeat
	LinkedAt   time.Time  `json:"linked_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	This       bool       `json:"this"` // the token asking
	CanUnlink  bool       `json:"can_unlink"`
	Sound      string     `json:"sound"`   // one of GET /api/v1/sounds
	Approve    string     `json:"approve"` // allowed, ask or denied: may it answer approval requests (#187)
	// CanApprove is true on the asker's own row when it may change who
	// approves (#329, #433): allowed, and on every topic, as SetApprove checks.
	CanApprove bool `json:"can_approve"`
	// Live sync state (#300): connected has a stream open now, caught_up has
	// one past its replay, synced_seq is the last event seq the server sent it.
	Connected bool  `json:"connected"`
	CaughtUp  bool  `json:"caught_up"`
	SyncedSeq int64 `json:"synced_seq"`
}

// devicesMsg is the stream message carrying tok's device list, and a key of
// who is in it and their live state: the stream sends the message only when
// the key changes, so it never tells of devices outside tok's list.
func (s *Server) devicesMsg(ctx context.Context, tok *Token) ([]byte, string, error) {
	ds, err := s.Devices(ctx, tok)
	if err != nil {
		return nil, "", err
	}
	var key strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&key, "%s %t %t %s %q,", d.ID, d.Connected, d.CaughtUp, d.Approve, d.Name)
	}
	msg, err := json.Marshal(struct {
		Type    string         `json:"type"`
		Devices []LinkedDevice `json:"devices"`
	}{"devices", ds})
	return msg, key.String(), err
}

// DeviceEvent is one line of the activity log.
type DeviceEvent struct {
	At       time.Time `json:"at"`
	Action   string    `json:"action"` // linked, signed in or unlinked
	Device   string    `json:"device"`
	DeviceID string    `json:"device_id"`
	By       string    `json:"by"` // the device that did it
}

// maxDeviceLog is how many log lines each top token keeps, so one sign-in
// loop cannot push out anyone else's history.
const maxDeviceLog = 200

func (s *Server) deviceRoutes() {
	s.api("GET /api/v1/devices", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		ds, err := s.Devices(r.Context(), tok) // the one scope for both lists (#263)
		if err != nil {
			return err
		}
		if r.URL.Query().Get("all") != "" {
			writeJSON(w, 200, map[string]any{"devices": ds})
			return nil
		}
		type device struct {
			ID         string     `json:"id"`
			Name       string     `json:"name"`
			LastSeenAt *time.Time `json:"last_seen_at"`
		}
		out := []device{}
		since := time.Now().Add(-10 * time.Minute)
		for _, d := range ds {
			if d.LastSeenAt != nil && d.LastSeenAt.After(since) {
				out = append(out, device{d.ID, d.Name, d.LastSeenAt})
			}
		}
		writeJSON(w, 200, map[string]any{"devices": out})
		return nil
	})
	s.api("GET /api/v1/devices/log", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		es, err := s.DeviceLog(r.Context(), tok)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"events": es})
		return nil
	})
	// One-click tidy (#765): unlink my browsers and phones unseen for days
	// (default 7) that nothing still uses. dry_run only counts them.
	s.api("POST /api/v1/devices/tidy", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		in := struct {
			Days   int  `json:"days"`
			DryRun bool `json:"dry_run"`
		}{Days: 7}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.Days < 1 || in.Days > maxKeepDays {
			return errf(400, "bad_request", "days must be 1 to %d", maxKeepDays)
		}
		ids, err := s.Tidy(r.Context(), tok, in.Days, in.DryRun)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"count": len(ids), "devices": ids, "dry_run": in.DryRun})
		return nil
	})
	s.api("DELETE /api/v1/devices/{id}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		id := r.PathValue("id")
		if id == "this" { // the asker itself, whose id it may not know (#609)
			id = tok.ID
		}
		if id != tok.ID { // a device may always unlink itself, whatever its scope (#609)
			if err := tok.need(ScopeRead, ""); err != nil {
				return err
			}
		}
		if err := s.Unlink(r.Context(), tok, id); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// topOf walks up every parent, phones included, to the token typed in.
func (s *Server) topOf(ctx context.Context, id string) (string, error) { return topIn(ctx, s.db, id) }

func topIn(ctx context.Context, q querier, id string) (string, error) {
	var top string
	err := q.QueryRowContext(ctx, `WITH RECURSIVE up(id, n) AS
	  (SELECT ?, 0 UNION SELECT parent_id, n + 1 FROM sessions JOIN up ON token_id = up.id)
	  SELECT id FROM up ORDER BY n DESC LIMIT 1`, id).Scan(&top)
	return top, err
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// targets resolves a publish's devices (names, any case, or token ids) to
// the ids of every token that may read topic and matches (#82). A name on
// several devices targets them all; one that matches none is refused, so a
// typo never silences everyone. Empty is every device.
func (s *Server) targets(ctx context.Context, topic string, want []string) ([]string, error) {
	out := []string{}
	if len(want) == 0 {
		return out, nil
	}
	if len(want) > 20 {
		return nil, errf(400, "bad_request", "devices: at most 20")
	}
	toks, err := s.Tokens(ctx, time.Time{})
	if err != nil {
		return nil, err
	}
	for _, w := range want {
		hit := false
		byID := slices.ContainsFunc(toks, func(t *Token) bool { return t.ID == w }) // an id never also matches a device named like it
		for _, t := range toks {
			if (t.ID == w || !byID && strings.EqualFold(t.Name, w)) && s.withUser(ctx, t) == nil && t.may(ScopeRead, topic) {
				hit = true
				if !slices.Contains(out, t.ID) {
					out = append(out, t.ID)
				}
			}
		}
		if !hit {
			return nil, errf(400, "bad_request", "devices: %q is not a device that may read %q", w, topic)
		}
	}
	return out, nil
}

// Devices lists the devices tok may see, oldest first.
func (s *Server) Devices(ctx context.Context, tok *Token) ([]LinkedDevice, error) {
	return s.devices(ctx, s.db, tok)
}

// Rename names a browser or phone in tok's list (#765). A token typed in is
// named on its own page, an agent where it was invited.
func (s *Server) Rename(ctx context.Context, tok *Token, id, name string) (LinkedDevice, error) {
	var d LinkedDevice
	if err := tok.need(ScopeRead, ""); err != nil {
		return d, err
	}
	if strings.TrimSpace(name) == "" {
		return d, errf(400, "bad_request", "name must not be empty")
	}
	name = deviceName(name, "") // cleaned and cut as at sign-in
	tx, d, err := s.deviceTx(ctx, tok, id)
	if tx != nil {
		defer tx.Rollback()
	}
	if err == nil && d.Kind != "browser" && d.Kind != "phone" {
		err = errf(403, "forbidden", "only a browser or phone is renamed here; a token typed in is renamed with /api/v1/tokens")
	}
	if err == nil {
		if _, err = tx.ExecContext(ctx, `UPDATE tokens SET name = ? WHERE id = ?`, name, id); err == nil {
			err = tx.Commit()
		}
		d.Name = name
	}
	s.mu.Unlock()
	if err == nil {
		s.devicesChanged.fire()
	}
	return d, err
}

// deviceTx locks s.mu, opens a transaction (unlinks wait for its commit) and
// finds device id in tok's device list, which must hold tok itself too, else
// 404. The caller rolls back and unlocks s.mu, also on error (#299).
func (s *Server) deviceTx(ctx context.Context, tok *Token, id string) (*sql.Tx, LinkedDevice, error) {
	var d LinkedDevice
	s.mu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, d, err
	}
	d, err = s.device(ctx, tx, tok, id)
	return tx, d, err
}

// device finds id in tok's device list read through q, which must hold tok
// itself too, else 404.
func (s *Server) device(ctx context.Context, q querier, tok *Token, id string) (LinkedDevice, error) {
	ds, err := s.devices(ctx, q, tok)
	i := slices.IndexFunc(ds, func(d LinkedDevice) bool { return d.ID == id })
	if err == nil && (i < 0 || !slices.ContainsFunc(ds, func(d LinkedDevice) bool { return d.ID == tok.ID })) {
		err = errf(404, "not_found", "device not found")
	}
	if err != nil {
		return LinkedDevice{}, err
	}
	return ds[i], nil
}

// devices is Devices read through q, so a write can check inside its own
// transaction (#299).
func (s *Server) devices(ctx context.Context, q querier, tok *Token) ([]LinkedDevice, error) {
	all := tok.needFullAdmin() == nil
	sounds, err := s.deviceSounds(ctx, q)
	if err != nil {
		return nil, err
	}
	// The agents my invites made (#454) are mine too, with what hangs under them.
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE `+topsSQL+`, k(id) AS
	  (SELECT id FROM tops UNION SELECT t.id FROM tokens t JOIN people u ON u.id = t.user_id WHERE u.kind = 'agent' AND u.owner IN tops
	   UNION SELECT token_id FROM sessions JOIN k ON parent_id = k.id)
	  SELECT t.id, t.name, t.detail, t.created_at, t.last_seen_at, s.phone, p.name, p.id, t.approve, COALESCE(u.kind, '')
	  FROM tokens t LEFT JOIN sessions s ON s.token_id = t.id LEFT JOIN tokens p ON p.id = s.parent_id LEFT JOIN people u ON u.id = t.user_id
	  WHERE ? OR t.id IN k ORDER BY t.created_at, t.rowid`, tok.ID, all)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LinkedDevice{}
	for rows.Next() {
		var d LinkedDevice
		var created int64
		var seen sql.NullInt64
		var phone sql.NullBool
		var via, viaID sql.NullString
		var kind string
		if err := rows.Scan(&d.ID, &d.Name, &d.Detail, &created, &seen, &phone, &via, &viaID, &d.Approve, &kind); err != nil {
			return nil, err
		}
		d.LinkedAt, d.Via, d.ViaID, d.This, d.CanUnlink = fromMS(created), via.String, viaID.String, d.ID == tok.ID, phone.Valid || kind == "agent"
		d.CanApprove = d.This && d.Approve == ApproveAllowed && len(tok.Topics) == 0 && len(tok.Grants) == 0
		d.Sound = soundOf(sounds, d.ID)
		d.Connected, d.CaughtUp, d.SyncedSeq = s.hub.status(d.ID)
		if seen.Valid {
			v := fromMS(seen.Int64)
			d.LastSeenAt = &v
		}
		d.Kind = "token"
		if kind == "agent" {
			d.Kind = "agent"
		} else if phone.Valid {
			d.Kind = "browser"
			if phone.Bool {
				d.Kind = "phone"
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Unlink signs a browser or phone out at once, with every browser it
// approved: their streams close and their next request gets 401. tok must see
// the device (as in Devices), so a reader cannot unlink anyone else's.
func (s *Server) Unlink(ctx context.Context, tok *Token, id string) error {
	ds, err := s.Devices(ctx, tok)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if d.ID != id {
			continue
		}
		if !d.CanUnlink {
			return errf(403, "forbidden", "a token typed in is revoked with /api/v1/tokens, not unlinked")
		}
		ctx = context.WithoutCancel(ctx)
		top, err := s.topOf(ctx, id) // before the delete, which drops the sessions rows
		var user string
		if err == nil && d.Kind == "agent" { // logged where it was linked, and its user goes with it
			err = s.db.QueryRowContext(ctx, `SELECT p.owner, p.id FROM tokens t JOIN people p ON p.id = t.user_id WHERE t.id = ?`, id).Scan(&top, &user)
		}
		if err != nil {
			return err
		}
		if err := s.DeleteToken(ctx, id); err != nil {
			return err
		}
		if user != "" {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM people WHERE id = ? AND NOT EXISTS (SELECT 1 FROM tokens WHERE user_id = ?)`, user, user); err != nil {
				s.log.Warn("drop agent user", "err", err)
			}
		}
		s.logDevice(ctx, top, "unlinked", d.Name, id, tok.Name)
		return nil
	}
	return errf(404, "not_found", "device not found")
}

// idleSince lists the browsers (and phones, unless browsersOnly) last seen
// before cutoff that nothing still uses: no stream open now, no web push,
// nothing signed in under them (#765). Never a token typed in, never an agent.
func (s *Server) idleSince(ctx context.Context, cutoff time.Time, browsersOnly bool) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id FROM tokens t JOIN sessions s ON s.token_id = t.id
	  LEFT JOIN people u ON u.id = t.user_id
	  WHERE COALESCE(t.last_seen_at, t.created_at) < ? AND (s.phone = 0 OR NOT ?) AND COALESCE(u.kind, '') != 'agent'
	    AND NOT EXISTS (SELECT 1 FROM push_subs p WHERE p.token_id = t.id)
	    AND NOT EXISTS (SELECT 1 FROM sessions c WHERE c.parent_id = t.id)
	  ORDER BY t.created_at, t.rowid`, ms(cutoff), browsersOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if live, _, _ := s.hub.status(id); !live {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// Tidy unlinks tok's browsers and phones idle for n days (see idleSince),
// never tok itself, and returns their ids; dryRun only lists them.
func (s *Server) Tidy(ctx context.Context, tok *Token, n int, dryRun bool) ([]string, error) {
	idle, err := s.idleSince(ctx, time.Now().Add(-days(n)), false)
	if err != nil {
		return nil, err
	}
	ds, err := s.Devices(ctx, tok)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, d := range ds {
		if d.This || !d.CanUnlink || d.Kind == "agent" || !slices.Contains(idle, d.ID) {
			continue
		}
		if !dryRun {
			if err := s.Unlink(ctx, tok, d.ID); err != nil && !isNotFound(err) {
				return out, err
			}
		}
		out = append(out, d.ID)
	}
	return out, nil
}

// unlinkIdle is the background sweep (#765): browsers unseen for the
// unlink_idle_browsers_days setting are unlinked, each with a device log
// line. Phones and tokens typed in are never swept.
func (s *Server) unlinkIdle(ctx context.Context) error {
	st, err := s.loadSettings(ctx, s.db)
	if err != nil {
		return err
	}
	n := unlinkIdleDays(st)
	if n == 0 {
		return nil
	}
	ids, err := s.idleSince(ctx, time.Now().Add(-days(n)), true)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var name string
		top, err := s.topOf(ctx, id)
		if err == nil {
			err = s.db.QueryRowContext(ctx, `SELECT name FROM tokens WHERE id = ?`, id).Scan(&name)
		}
		if err == nil {
			err = s.DeleteToken(ctx, id)
		}
		if errors.Is(err, sql.ErrNoRows) || isNotFound(err) {
			continue // gone already
		}
		if err != nil {
			return err
		}
		s.logDevice(ctx, top, "unlinked", name, id, fmt.Sprintf("unused for %d days", n))
	}
	return nil
}

// DeviceLog is the newest log lines tok may see, newest first.
func (s *Server) DeviceLog(ctx context.Context, tok *Token) ([]DeviceEvent, error) {
	all := tok.needFullAdmin() == nil
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE `+topsSQL+` SELECT at, action, device, device_id, by_name FROM device_log
	  WHERE ? OR owner IN tops ORDER BY id DESC LIMIT ?`, tok.ID, all, maxDeviceLog)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceEvent{}
	for rows.Next() {
		var e DeviceEvent
		var at int64
		if err := rows.Scan(&at, &e.Action, &e.Device, &e.DeviceID, &e.By); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// logDevice adds a log line under top and keeps that top's newest
// maxDeviceLog. A failure is logged, never returned: the change already happened.
func (s *Server) logDevice(ctx context.Context, top, action, device, id, by string) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO device_log (at, owner, action, device, device_id, by_name) VALUES (?, ?, ?, ?, ?, ?)`,
		ms(time.Now()), top, action, device, id, by)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM device_log WHERE owner = ? AND id <=
		  (SELECT id FROM device_log WHERE owner = ? ORDER BY id DESC LIMIT 1 OFFSET ?)`, top, top, maxDeviceLog)
	}
	if err != nil {
		s.log.Warn("device log", "err", err)
	}
}
