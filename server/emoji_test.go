package server

import (
	"context"
	"strings"
	"testing"
)

// Issue #306: common shortcodes are sent as emoji, everything else as typed.
func TestEmojify(t *testing.T) {
	for in, want := range map[string]string{
		":warning: server: Stopping server": "⚠️ server: Stopping server",
		"done :white_check_mark::tada:":     "done ✅🎉",
		":+1: and :-1:":                     "👍 and 👎",
		"at 10:30:15 a:b:c":                 "at 10:30:15 a:b:c",
		"x:fire: y:fire:z":                  "x:fire: y:fire:z",
		":no_such_name: :Fire:":             ":no_such_name: :Fire:",
		"run `:fire:` now :fire:":           "run `:fire:` now 🔥",
		"```\n:fire:\n``` :fire:":           "```\n:fire:\n``` 🔥",
		"lone ` tick :fire:":                "lone ` tick 🔥",
		"(:fire:)":                          "(🔥)",
		"``a ` :fire: b`` :fire:":           "``a ` :fire: b`` 🔥",
		"`a ``` :fire: ` :fire:":            "`a ``` :fire: ` 🔥",
		"é:b:ç ✅:fire:":                     "é:b:ç ✅🔥",
		"":                                  "",
	} {
		if got := emojify(in); got != want {
			t.Errorf("emojify(%q) = %q, want %q", in, got, want)
		}
	}
}

// The API, the stream and the ntfy endpoint send the emoji; the stored row
// keeps the text as it came in; tags that name an emoji go before the title.
func TestEmojiServed(t *testing.T) {
	s, ts, admin := newTest(t)
	ws := dialWS(t, ts.URL, admin, 0)
	ws.until(func(m msg) bool { return m.Type == "ready" })
	code, _ := raw(t, "POST", ts.URL+"/infra", admin, "deploy :rocket: at 10:30:15",
		map[string]string{"Title": ":warning: server: Stopping server", "Tags": "rotating_light,disk"})
	if code != 200 {
		t.Fatalf("ntfy publish: %d", code)
	}
	want := "🚨 ⚠️ server: Stopping server"
	if m := ws.until(func(m msg) bool { return m.Notification != nil }); m.Notification.Title != want ||
		m.Notification.Message != "deploy 🚀 at 10:30:15" {
		t.Fatalf("stream: %+v", m.Notification)
	}
	_, out := call(t, ts, "GET", "/api/v1/notifications", admin, nil)
	if n := out["notifications"].([]any)[0].(map[string]any); n["title"] != want {
		t.Fatalf("list: %v", n)
	}
	var title string
	s.db.QueryRowContext(context.Background(), `SELECT title FROM notifications`).Scan(&title)
	if title != ":warning: server: Stopping server" {
		t.Fatalf("stored %q", title)
	}
	// ntfy apps add the tags' emoji themselves: theirs has only the shortcodes.
	_, out2 := raw(t, "GET", ts.URL+"/infra/json?poll=1", admin, "", nil)
	if m := ntfyDecode(t, strings.TrimSpace(out2)); m.Title != "⚠️ server: Stopping server" {
		t.Fatalf("ntfy poll: %+v", m)
	}
	// No title: the tags' emoji go before the message.
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "t2", "message": "m", "tags": []string{"fire"}})
	if m := ws.until(func(m msg) bool { return m.Notification != nil && m.Notification.Topic == "t2" }); m.Notification.Message != "🔥 m" {
		t.Fatalf("untitled: %+v", m.Notification)
	}
}
