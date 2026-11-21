package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Linked devices (issue #246): every device, unlink from another device, and
// a log of links, sign-ins and unlinks. A full admin sees every token. Anyone
// else sees the devices under their own top token: the token typed in, every
// browser signed in with it, every phone linked from one of those, and the
// browsers those phones approved. Only linked devices (browsers and phones)
// can be unlinked here; typed-in tokens are managed with /api/v1/tokens.

// LinkedDevice is one row of the device list. It never carries a secret or hash.
type LinkedDevice struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`             // token, browser or phone
	Via        string     `json:"via,omitempty"`    // the token or device it hangs under: revoking that one signs this one out too
	ViaID      string     `json:"via_id,omitempty"` // its id, since names can repeat
	LinkedAt   time.Time  `json:"linked_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	This       bool       `json:"this"` // the token asking
	CanUnlink  bool       `json:"can_unlink"`
	Sound      string     `json:"sound"`   // one of GET /api/v1/sounds
	Approve    string     `json:"approve"` // allowed, ask or denied: may it answer approval requests (#187)
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
		fmt.Fprintf(&key, "%s %t %t %s,", d.ID, d.Connected, d.CaughtUp, d.Approve)
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
	s.api("DELETE /api/v1/devices/{id}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		if err := s.Unlink(r.Context(), tok, r.PathValue("id")); err != nil {
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

// Devices lists the devices tok may see, oldest first.
func (s *Server) Devices(ctx context.Context, tok *Token) ([]LinkedDevice, error) {
	return s.devices(ctx, s.db, tok)
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
	top, err := topIn(ctx, q, tok.ID)
	if err != nil {
		return nil, err
	}
	sounds, err := s.deviceSounds(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE k(id) AS
	  (SELECT ? UNION SELECT token_id FROM sessions JOIN k ON parent_id = k.id)
	  SELECT t.id, t.name, t.created_at, t.last_seen_at, s.phone, p.name, p.id, t.approve
	  FROM tokens t LEFT JOIN sessions s ON s.token_id = t.id LEFT JOIN tokens p ON p.id = s.parent_id
	  WHERE ? OR t.id IN k ORDER BY t.created_at, t.rowid`, top, all)
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
		if err := rows.Scan(&d.ID, &d.Name, &created, &seen, &phone, &via, &viaID, &d.Approve); err != nil {
			return nil, err
		}
		d.LinkedAt, d.Via, d.ViaID, d.This, d.CanUnlink = fromMS(created), via.String, viaID.String, d.ID == tok.ID, phone.Valid
		d.Sound = soundOf(sounds, d.ID)
		d.Connected, d.CaughtUp, d.SyncedSeq = s.hub.status(d.ID)
		if seen.Valid {
			v := fromMS(seen.Int64)
			d.LastSeenAt = &v
		}
		d.Kind = "token"
		if phone.Valid {
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
		if err != nil {
			return err
		}
		if err := s.DeleteToken(ctx, id); err != nil {
			return err
		}
		s.logDevice(ctx, top, "unlinked", d.Name, id, tok.Name)
		return nil
	}
	return errf(404, "not_found", "device not found")
}

// DeviceLog is the newest log lines tok may see, newest first.
func (s *Server) DeviceLog(ctx context.Context, tok *Token) ([]DeviceEvent, error) {
	all := tok.needFullAdmin() == nil
	top, err := s.topOf(ctx, tok.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT at, action, device, device_id, by_name FROM device_log
	  WHERE ? OR owner = ? ORDER BY id DESC LIMIT ?`, all, top, maxDeviceLog)
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
