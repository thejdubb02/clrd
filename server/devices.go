package server

import (
	"context"
	"database/sql"
	"net/http"
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
	Kind       string     `json:"kind"`          // token, browser or phone
	Via        string     `json:"via,omitempty"` // the device it was linked or signed in from
	LinkedAt   time.Time  `json:"linked_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	This       bool       `json:"this"` // the token asking
	CanUnlink  bool       `json:"can_unlink"`
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
		if r.URL.Query().Get("all") != "" {
			ds, err := s.Devices(r.Context(), tok)
			if err != nil {
				return err
			}
			writeJSON(w, 200, map[string]any{"devices": ds})
			return nil
		}
		ts, err := s.Tokens(r.Context(), time.Now().Add(-10*time.Minute))
		if err != nil {
			return err
		}
		type device struct {
			ID         string     `json:"id"`
			Name       string     `json:"name"`
			LastSeenAt *time.Time `json:"last_seen_at"`
		}
		out := []device{}
		for _, t := range ts {
			if tok.seesAll(t) {
				out = append(out, device{t.ID, t.Name, t.LastSeenAt})
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
func (s *Server) topOf(ctx context.Context, id string) (string, error) {
	var top string
	err := s.db.QueryRowContext(ctx, `WITH RECURSIVE up(id, n) AS
	  (SELECT ?, 0 UNION SELECT parent_id, n + 1 FROM sessions JOIN up ON token_id = up.id)
	  SELECT id FROM up ORDER BY n DESC LIMIT 1`, id).Scan(&top)
	return top, err
}

// Devices lists the devices tok may see, oldest first.
func (s *Server) Devices(ctx context.Context, tok *Token) ([]LinkedDevice, error) {
	all := tok.needFullAdmin() == nil
	top, err := s.topOf(ctx, tok.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE k(id) AS
	  (SELECT ? UNION SELECT token_id FROM sessions JOIN k ON parent_id = k.id)
	  SELECT t.id, t.name, t.created_at, t.last_seen_at, s.phone, p.name
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
		var via sql.NullString
		if err := rows.Scan(&d.ID, &d.Name, &created, &seen, &phone, &via); err != nil {
			return nil, err
		}
		d.LinkedAt, d.Via, d.This, d.CanUnlink = fromMS(created), via.String, d.ID == tok.ID, phone.Valid
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
