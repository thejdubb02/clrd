package server

import (
	"context"
	"database/sql"
	"math"
	"strconv"
	"strings"
)

const defaultTheme = "default"

// ThemeColors is the shared custom palette, stored as six RGB hex colors.
type ThemeColors struct {
	BG     string `json:"bg"`
	Card   string `json:"card"`
	Text   string `json:"text"`
	Clear  string `json:"clear"`
	Urgent string `json:"urgent"`
	Snooze string `json:"snooze"`
}

// ThemeSettings contains the saved palette and the calling device's override.
type ThemeSettings struct {
	Theme       string       `json:"theme,omitempty"`
	ThemeColors *ThemeColors `json:"theme_colors,omitempty"`
	DeviceTheme string       `json:"device_theme,omitempty"`
}

func validTheme(name string) bool {
	switch name {
	case defaultTheme, "high-contrast", "midnight", "custom":
		return true
	}
	return false
}

func checkThemeColors(c ThemeColors) error {
	colors := [...]string{c.BG, c.Card, c.Text, c.Clear, c.Urgent, c.Snooze}
	names := [...]string{"Background", "Cards", "Text", "Clear", "Urgent", "Snooze"}
	var values [6]uint64
	var light [6]float64
	for i, color := range colors {
		if !hexColor(color) {
			return errf(400, "bad_request", "%s must be a #RRGGBB color", names[i])
		}
		values[i], _ = strconv.ParseUint(color[1:], 16, 32)
		for j, weight := range [...]float64{0.2126, 0.7152, 0.0722} {
			channel := float64((values[i]>>uint(16-8*j))&255) / 255
			if channel <= 0.04045 {
				channel /= 12.92
			} else {
				channel = math.Pow((channel+0.055)/1.055, 2.4)
			}
			light[i] += weight * channel
		}
	}
	for _, pair := range [...]struct {
		fg, bg int
		min    float64
	}{{2, 0, 4.5}, {2, 1, 4.5}, {3, 1, 3}, {4, 1, 3}, {5, 1, 3}} {
		a, b := light[pair.fg], light[pair.bg]
		ratio := (max(a, b) + 0.05) / (min(a, b) + 0.05)
		if ratio < pair.min {
			return errf(400, "bad_request", "%s on %s has too little contrast (%.2f:1, needs %g:1)", names[pair.fg], strings.ToLower(names[pair.bg]), ratio, pair.min)
		}
	}
	distance := 0
	for _, shift := range [...]uint{0, 8, 16} {
		d := int((values[3]>>shift)&255) - int((values[4]>>shift)&255)
		distance += d * d
	}
	if distance < 80*80 {
		return errf(400, "bad_request", "Urgent and Clear colors are too similar")
	}
	return nil
}

// themeKey follows the subscriptions ownership rule. Read the current owner
// through q so a token attached or detached during a PUT cannot edit its old user.
func themeKey(ctx context.Context, q querier, id string) (string, error) {
	top, err := topIn(ctx, q, id)
	if err != nil {
		return "", err
	}
	var user sql.NullString
	err = q.QueryRowContext(ctx, "SELECT user_id FROM tokens WHERE id = ?", top).Scan(&user)
	if err == sql.ErrNoRows {
		return "", errf(401, "unauthorized", "token no longer exists")
	}
	if user.Valid {
		return "theme:u:" + user.String, err
	}
	return "theme:t:" + top, err
}

func readTheme(ctx context.Context, q querier, id string) (ThemeSettings, error) {
	st := ThemeSettings{Theme: defaultTheme}
	key, err := themeKey(ctx, q, id)
	if err == nil {
		err = settingJSON(ctx, q, key, &st)
	}
	return st, err
}

func saveTheme(ctx context.Context, tx *sql.Tx, tok *Token, p SettingsPatch) error {
	key, err := themeKey(ctx, tx, tok.ID)
	if err != nil {
		return err
	}
	st := ThemeSettings{Theme: defaultTheme}
	err = settingJSON(ctx, tx, key, &st)
	if err != nil {
		return err
	}
	if p.Theme != nil {
		st.Theme = *p.Theme
		if !validTheme(st.Theme) {
			return errf(400, "bad_request", "theme must be default, high-contrast, midnight or custom")
		}
	}
	if p.ThemeColors != nil {
		colors := *p.ThemeColors
		if err := checkThemeColors(colors); err != nil {
			return err
		}
		for _, c := range []*string{&colors.BG, &colors.Card, &colors.Text, &colors.Clear, &colors.Urgent, &colors.Snooze} {
			*c = strings.ToLower(*c)
		}
		st.ThemeColors = &colors
	}
	if st.Theme == "custom" && st.ThemeColors == nil {
		return errf(400, "bad_request", "custom needs theme_colors")
	}
	return putSettingJSON(ctx, tx, key, st)
}

func (s *Server) setDeviceTheme(ctx context.Context, tok *Token, id, theme string) (LinkedDevice, error) {
	if err := tok.need(ScopeRead, ""); err != nil {
		return LinkedDevice{}, err
	}
	if theme != "" && !validTheme(theme) {
		return LinkedDevice{}, errf(400, "bad_request", "device theme must be empty, default, high-contrast, midnight or custom")
	}
	if id == "this" {
		id = tok.ID
	}
	tx, d, err := s.deviceTx(ctx, tok, id)
	if tx != nil {
		defer tx.Rollback()
	}
	if err == nil && theme == "custom" {
		var st ThemeSettings
		st, err = readTheme(ctx, tx, id)
		if err == nil && st.ThemeColors == nil {
			err = errf(400, "bad_request", "custom needs the device owner's theme_colors")
		}
	}
	if err == nil {
		d.Theme = theme
		if theme == "" {
			_, err = tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", "theme:d:"+id)
		} else {
			err = putSettingJSON(ctx, tx, "theme:d:"+id, theme)
		}
		if err == nil {
			err = tx.Commit()
		}
	}
	s.mu.Unlock()
	if err == nil {
		s.settingsChanged.fire()
		s.devicesChanged.fire()
	}
	return d, err
}
