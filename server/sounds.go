package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
)

// Notification sounds (issue #282): each device has its own choice, kept
// by device id. "default" is the phone's or browser's own sound, "none" is
// silent. Quiet hours, silences and priority rules still win over it.
var soundNames = []string{"default", "none", "chime", "ding", "pop", "bell", "alert", "soft"}

const defaultSound = "default"

// deviceSounds is every device's sound choice that is not the default.
func (s *Server) deviceSounds(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (map[string]string, error) {
	m := map[string]string{}
	return m, settingJSON(ctx, q, "sounds", &m)
}

// settingJSON reads settings row key into v; a missing row leaves v as it is.
func settingJSON(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string, v any) error {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), v)
	}
	return err
}

// putSettingJSON writes v as settings row key inside tx.
func putSettingJSON(ctx context.Context, tx *sql.Tx, key string, v any) error {
	raw, _ := json.Marshal(v)
	_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(raw))
	return err
}

func soundOf(m map[string]string, id string) string {
	if v, ok := m[id]; ok {
		return v
	}
	return defaultSound
}

// SetSound sets device id's sound and returns the device as written. tok
// must see the device (as in Devices) and, like the other global settings,
// not be limited to some topics. The check runs inside the write's
// transaction, so a device or caller unlinked meanwhile is refused (#299).
func (s *Server) SetSound(ctx context.Context, tok *Token, id, sound string) (LinkedDevice, error) {
	var d LinkedDevice
	if err := tok.need(ScopeRead, ""); err != nil {
		return d, err
	}
	if tok.limited() {
		return d, errf(403, "forbidden", "a token limited to some topics cannot change sounds")
	}
	if !slices.Contains(soundNames, sound) {
		return d, errf(400, "bad_request", "unknown sound %q, use one of: %s", sound, strings.Join(soundNames, ", "))
	}
	tx, d, err := s.deviceTx(ctx, tok, id)
	if tx != nil {
		defer tx.Rollback()
	}
	m := map[string]string{}
	if err == nil {
		d.Sound = sound
		m, err = s.deviceSounds(ctx, tx)
	}
	if err == nil {
		if sound == defaultSound {
			delete(m, id)
		} else {
			m[id] = sound
		}
		if err = putSettingJSON(ctx, tx, "sounds", m); err == nil {
			err = tx.Commit()
		}
	}
	s.mu.Unlock()
	if err == nil {
		s.settingsChanged.fire() // the device hears its new sound on its stream
	}
	return d, err
}

func (s *Server) soundRoutes() {
	s.api("GET /api/v1/sounds", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"sounds": soundNames, "default": defaultSound})
		return nil
	})
	s.api("PUT /api/v1/devices/{id}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var p struct {
			Sound *string `json:"sound"`
		}
		if err := decodeJSON(r, &p); err != nil {
			return err
		}
		if p.Sound == nil {
			return errf(400, "bad_request", "sound is required")
		}
		d, err := s.SetSound(r.Context(), tok, r.PathValue("id"), *p.Sound)
		if err != nil {
			return err
		}
		writeJSON(w, 200, d) // as written, not a fresh read a racing PUT may have changed (#299)
		return nil
	})
}
