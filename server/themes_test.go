package server

import (
	"context"
	"strings"
	"testing"
)

func TestThemes(t *testing.T) {
	s, ts, admin := newTest(t)
	const settings = "/api/v1/settings"
	ctx := context.Background()
	alice := mkToken(t, s, "alice phone", []string{ScopeRead}, []string{"alerts"})
	laptop := mkToken(t, s, "alice laptop", []string{ScopeRead}, nil)
	bob := mkToken(t, s, "bob", []string{ScopeRead}, nil)
	code, user := call(t, ts, "POST", "/api/v1/users", admin, map[string]any{"name": "Alice"})
	if code != 200 && code != 201 {
		t.Fatalf("create user: %d %v", code, user)
	}
	for _, raw := range []string{alice, laptop} {
		id := s.lookupToken(ctx, raw).ID
		if code, out := call(t, ts, "PUT", "/api/v1/tokens/"+id+"/user", admin, map[string]any{"user": user["id"]}); code != 200 {
			t.Fatalf("attach device: %d %v", code, out)
		}
	}
	if code, st := call(t, ts, "GET", settings, alice, nil); code != 200 || st["theme"] != "default" || st["theme_colors"] != nil {
		t.Fatalf("initial theme: %d %v", code, st)
	}

	colors := ThemeColors{"#F5F3EE", "#FFFFFF", "#0F1114", "#0B7A55", "#C2410C", "#9D640E"}
	if code, st := call(t, ts, "PUT", settings, alice, map[string]any{"theme": "custom", "theme_colors": colors}); code != 200 || st["theme"] != "custom" {
		t.Fatalf("save on a topic-limited device: %d %v", code, st)
	}
	if code, st := call(t, ts, "GET", settings, laptop, nil); code != 200 || st["theme"] != "custom" || st["theme_colors"].(map[string]any)["bg"] != "#f5f3ee" {
		t.Fatalf("same user on separate top token did not inherit: %d %v", code, st)
	}
	if code, st := call(t, ts, "GET", settings, bob, nil); code != 200 || st["theme"] != "default" || st["theme_colors"] != nil {
		t.Fatalf("different owner got Alice's theme: %d %v", code, st)
	}

	for _, bad := range []struct {
		name string
		edit func(*ThemeColors)
	}{
		{"Background must be", func(c *ThemeColors) { c.BG = "#fff;foo" }},
		{"Text on background", func(c *ThemeColors) { c.Text = "#aaaaaa" }},
		{"Text on cards", func(c *ThemeColors) { c.Card = "#151515" }},
		{"Clear on cards", func(c *ThemeColors) { c.Clear = "#dddddd" }},
		{"Urgent on cards", func(c *ThemeColors) { c.Urgent = "#dddddd" }},
		{"Snooze on cards", func(c *ThemeColors) { c.Snooze = "#dddddd" }},
		{"Urgent and Clear", func(c *ThemeColors) { c.Urgent = c.Clear }},
	} {
		c := colors
		bad.edit(&c)
		code, out := call(t, ts, "PUT", settings, laptop, map[string]any{"theme": "midnight", "theme_colors": c})
		if code != 400 || !strings.Contains(out["error"].(string), bad.name) {
			t.Fatalf("%s: %d %v", bad.name, code, out)
		}
	}
	if code, st := call(t, ts, "GET", settings, alice, nil); code != 200 || st["theme"] != "custom" {
		t.Fatalf("rejected write changed the theme: %d %v", code, st)
	}
	if code, _ := call(t, ts, "PUT", settings, bob, map[string]any{"theme": "custom"}); code != 400 {
		t.Fatalf("accepted custom without a saved palette: %d", code)
	}

	id := s.lookupToken(ctx, alice).ID
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+id, bob, map[string]any{"theme": "midnight"}); code != 404 {
		t.Fatalf("another user could change the device override: %d", code)
	}
	for _, theme := range []string{"midnight", ""} {
		if code, _ := call(t, ts, "PUT", "/api/v1/devices/this", alice, map[string]any{"theme": theme}); code != 200 {
			t.Fatalf("set override %q: %d", theme, code)
		}
		if code, st := call(t, ts, "GET", settings, alice, nil); code != 200 || st["theme"] != "custom" || (theme != "" && st["device_theme"] != theme) || (theme == "" && st["device_theme"] != nil) {
			t.Fatalf("read override %q: %d %v", theme, code, st)
		}
		if code, st := call(t, ts, "GET", settings, laptop, nil); code != 200 || st["theme"] != "custom" || st["device_theme"] != nil {
			t.Fatalf("override leaked to another device: %d %v", code, st)
		}
	}
}

func TestBrowserThemeAfterSigningInAgain(t *testing.T) {
	s, ts, _ := newTest(t)
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	oldID, old := browserIn(t, s, ts, typed, installA)
	if code, st := call(t, ts, "PUT", "/api/v1/devices/this", old.Value, map[string]any{"theme": "midnight"}); code != 200 || st["theme"] != "midnight" {
		t.Fatalf("set theme: %d %v", code, st)
	}
	code, device, current := signIn(t, ts, `{"token":"`+typed+`","install":"`+installA+`"}`, firefoxLinux, old)
	if code != 200 || current == nil || device["id"] == oldID {
		t.Fatalf("replace browser session: %d %v", code, device)
	}
	if code, st := call(t, ts, "GET", "/api/v1/settings", current.Value, nil); code != 200 || st["device_theme"] != "midnight" {
		t.Errorf("replacement lost the override: %d %v", code, st)
	}
	if code := withCookie(t, ts, "DELETE", "/api/v1/session", current); code != 204 {
		t.Fatalf("sign out: %d", code)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN (?, ?)`, "theme:d:"+oldID, "theme:d:"+device["id"].(string)).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("found %d theme rows for deleted sessions: %v", rows, err)
	}
}

func TestSingleKeyShortcutsPerOwner(t *testing.T) {
	s, ts, admin := newTest(t)
	owner := mkToken(t, s, "work", []string{ScopeRead}, []string{"alerts"})
	_, browser := browserIn(t, s, ts, owner, installA)
	const path = "/api/v1/settings"
	if code, out := call(t, ts, "GET", path, owner, nil); code != 200 || out["single_key_shortcuts"] != true {
		t.Fatalf("expected on by default: %d %v", code, out)
	}
	if code, out := call(t, ts, "PUT", path, browser.Value, map[string]any{"single_key_shortcuts": false}); code != 200 || out["single_key_shortcuts"] != false {
		t.Fatalf("set from a limited browser: %d %v", code, out)
	}
	if code, out := call(t, ts, "GET", path, admin, nil); code != 200 || out["single_key_shortcuts"] != true {
		t.Fatalf("different owner was affected: %d %v", code, out)
	}
	if code, out := call(t, ts, "PUT", path, owner, map[string]any{"theme": "midnight"}); code != 200 || out["single_key_shortcuts"] != false || out["theme"] != "midnight" {
		t.Fatalf("same owner did not keep shortcuts off when saving theme: %d %v", code, out)
	}
	if code, out := call(t, ts, "PUT", path, owner, map[string]any{"single_key_shortcuts": true}); code != 200 || out["single_key_shortcuts"] != true || out["theme"] != "midnight" {
		t.Fatalf("re-enable shortcuts and retain theme: %d %v", code, out)
	}
}
