package server

import (
	"fmt"
	"strings"
	"testing"
)

// Issue #308: the format check counts each rule per topic, keeps the newest
// example, and leaves times, code and good messages alone.
func TestFormatCheck(t *testing.T) {
	s, ts, admin := newTest(t)
	pub := func(topic, title, msg, format string) {
		t.Helper()
		if code, out := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": topic, "title": title, "message": msg, "format": format}); code != 200 {
			t.Fatalf("publish: %d %v", code, out)
		}
	}
	// Clean: none of these may count.
	pub("good", "Backup", "done at 12:30:45, ratio a:b:c, lap :42: :white_check_mark:", "text")
	pub("good", "Code", "```\n:not_an_emoji: {\"a\":1}\n```\n`:also_not:` **bold** [x](https://x.test)", "markdown")
	pub("good", "Hash", "#1 in line, #nospace, 2**8", "text")
	pub("good", "Data", "{not json", "text")
	// Problems.
	pub("bad", "", "**Disk** at 91%", "text")
	pub("bad", "Deploy", ":party_parrot: shipped", "markdown")
	pub("bad", "Dump", `{"host":"a","disk":91}`, "markdown")
	pub("bad", "Long", strings.Repeat("x", 2001), "markdown")
	pub("bad", "", "## Heading\nnewest", "text")

	_, out := call(t, ts, "GET", "/api/v1/format-check", admin, nil)
	if out["checked"] != float64(9) || out["problems"] != float64(5) {
		t.Fatalf("totals: %v", out)
	}
	topics := out["topics"].([]any)
	if len(topics) != 1 {
		t.Fatalf("only bad has findings: %v", topics)
	}
	bad := topics[0].(map[string]any)
	got := map[string]float64{}
	for _, f := range bad["findings"].([]any) {
		f := f.(map[string]any)
		got[f["rule"].(string)] = f["count"].(float64)
		if f["rule"] == "markdown_as_text" && f["example"] != "## Heading\nnewest" {
			t.Errorf("example is not the newest: %q", f["example"])
		}
	}
	want := map[string]float64{"markdown_as_text": 2, "unknown_shortcode": 1, "no_title": 2, "long_body": 1, "json_dump": 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("findings %v, want %v", got, want)
	}
	prompt := bad["prompt"].(string)
	if !strings.Contains(prompt, `topic "bad"`) || !strings.Contains(prompt, "$CLRD_TOKEN") || strings.Contains(prompt, admin) ||
		!strings.Contains(prompt, `"format": "markdown"`) || !strings.Contains(prompt, ts.URL+"/api/v1/notifications") {
		t.Fatalf("prompt: %s", prompt)
	}

	// A token sees only what it may read.
	ro := mkToken(t, s, "ro", []string{"read"}, []string{"good"})
	if _, out := call(t, ts, "GET", "/api/v1/format-check", ro, nil); out["checked"] != float64(4) || len(out["topics"].([]any)) != 0 {
		t.Fatalf("limited token: %v", out)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/format-check?topic=bad", ro, nil); code != 403 {
		t.Fatalf("other topic: %d", code)
	}
}

// Issue #315: the example pasted into an agent hides what looks like a
// secret, and leaves ordinary words, times and short numbers alone.
func TestExcerptHidesSecrets(t *testing.T) {
	for in, want := range map[string]string{
		"login password=hunter2 ok":                                       "login password=[hidden] ok",
		`{"token": "abc", "api_key":"x1"}`:                                `{"token": "[hidden]", "api_key":"[hidden]"}`,
		"sent as bearer FAKE1 then":                                       "sent as bearer [hidden] then",
		"see https://me:FAKE_PW@host.test/x":                              "see https://me:[hidden]@host.test/x",
		"used clrd_FAKE000000000000000000 by mistake":                     "used [hidden] by mistake",
		"Disk 91% at 12:30:45, token revoked, key point":                  "Disk 91% at 12:30:45, token revoked, key point",
		"averyveryverylongwordwithnodigitsatall here":                     "averyveryverylongwordwithnodigitsatall here",
		`password="correct horse battery" next`:                           `password="[hidden]" next`,
		`{"token":123456}`:                                                `{"token":[hidden]}`,
		strings.Repeat("x", 180) + " https://me:FAKE_PW_LONGER@host.test": strings.Repeat("x", 180) + " https://me:[hidden]...",
	} {
		if got := excerpt(&Notification{Message: in}); got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
}

// Issue #320: by=sender splits a topic by sending token, a dismissed rule
// stops counting (for one sender or all), and a topic can be turned off.
// All of it is off until set.
func TestFormatCheckSenderDismissOff(t *testing.T) {
	s, ts, admin := newTest(t)
	bot := mkToken(t, s, "backup-bot", []string{"publish"}, []string{"jobs"})
	ro := mkToken(t, s, "ro", []string{"read"}, []string{"jobs"})
	for _, tok := range []string{admin, bot, bot} {
		if code, out := call(t, ts, "POST", "/api/v1/notifications", tok, map[string]any{"topic": "jobs", "message": "no title"}); code != 200 {
			t.Fatalf("publish: %d %v", code, out)
		}
	}
	check := func(q string) []any {
		t.Helper()
		code, out := call(t, ts, "GET", "/api/v1/format-check"+q, admin, nil)
		if code != 200 {
			t.Fatalf("check %s: %d %v", q, code, out)
		}
		return out["topics"].([]any)
	}
	if g := check(""); len(g) != 1 || g[0].(map[string]any)["checked"] != float64(3) || g[0].(map[string]any)["sender_id"] != nil {
		t.Fatalf("default groups by topic: %v", g)
	}
	g := check("?by=sender")
	if len(g) != 2 {
		t.Fatalf("by sender: %v", g)
	}
	var botID string
	for _, x := range g {
		x := x.(map[string]any)
		if x["sender"] == "backup-bot" {
			botID = x["sender_id"].(string)
			if x["checked"] != float64(2) || !strings.Contains(x["prompt"].(string), `token named "backup-bot"`) {
				t.Fatalf("bot group: %v", x)
			}
		}
	}
	if botID == "" {
		t.Fatalf("no backup-bot group: %v", g)
	}

	dismiss := func(tok string, body map[string]any) int {
		t.Helper()
		code, _ := call(t, ts, "POST", "/api/v1/format-check/dismiss", tok, body)
		return code
	}
	if c := dismiss(ro, map[string]any{"topic": "jobs", "rule": "no_title"}); c != 403 {
		t.Fatalf("read-only dismiss: %d", c)
	}
	if c := dismiss(admin, map[string]any{"topic": "jobs", "rule": "nope"}); c != 400 {
		t.Fatalf("unknown rule: %d", c)
	}
	if c := dismiss(admin, map[string]any{"topic": "jobs", "rule": "no_title", "sender_id": botID}); c != 200 {
		t.Fatalf("dismiss: %d", c)
	}
	if g := check("?by=sender"); len(g) != 1 || g[0].(map[string]any)["sender"] == "backup-bot" {
		t.Fatalf("bot's finding still shows: %v", g)
	}
	if g := check(""); len(g) != 1 || g[0].(map[string]any)["problems"] != float64(1) {
		t.Fatalf("by topic, only the other sender counts: %v", g)
	}
	if c := dismiss(admin, map[string]any{"topic": "jobs", "rule": "no_title"}); c != 200 || len(check("")) != 0 {
		t.Fatalf("dismiss for every sender: %d", c)
	}
	dismiss(admin, map[string]any{"topic": "secret", "rule": "no_title"}) // ro may not read it, so must not see it
	_, out := call(t, ts, "GET", "/api/v1/format-check", ro, nil)
	if fmt.Sprint(out["dismissed"]) != fmt.Sprintf("[map[rule:no_title sender_id: topic:jobs] map[rule:no_title sender_id:%s topic:jobs]]", botID) {
		t.Fatalf("dismissed list: %v", out["dismissed"])
	}
	dismiss(admin, map[string]any{"topic": "jobs", "rule": "no_title", "dismissed": false})
	dismiss(admin, map[string]any{"topic": "jobs", "rule": "no_title", "sender_id": botID, "dismissed": false})
	if len(check("")) != 1 {
		t.Fatal("undismiss did not bring it back")
	}

	if code, _ := call(t, ts, "PUT", "/api/v1/topics/jobs", ro, map[string]any{"format_check": false}); code != 403 {
		t.Fatalf("read-only turns off: %d", code)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/topics/jobs", admin, map[string]any{"format_check": false}); code != 200 {
		t.Fatalf("turn off: %d", code)
	}
	if _, out := call(t, ts, "GET", "/api/v1/format-check", admin, nil); out["checked"] != float64(0) || fmt.Sprint(out["off"]) != "[jobs]" {
		t.Fatalf("off topic still checked: %v", out)
	}
	call(t, ts, "PUT", "/api/v1/topics/jobs", admin, map[string]any{"format_check": true})
	if len(check("")) != 1 {
		t.Fatal("turning it back on did not")
	}
	// 500 newer messages in an off topic must not push out an older one.
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "quiet", "message": "no title"})
	s.tokLimit, s.topicLimit = nil, nil
	for range 500 {
		if _, err := s.Publish(t.Context(), &Token{Name: "x", Scopes: []string{ScopeAdmin}}, PublishInput{Topic: "noisy", Title: "ok", Message: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	call(t, ts, "PUT", "/api/v1/topics/noisy", admin, map[string]any{"format_check": false})
	call(t, ts, "PUT", "/api/v1/topics/jobs", admin, map[string]any{"format_check": false})
	if g := check(""); len(g) != 1 || g[0].(map[string]any)["topic"] != "quiet" {
		t.Fatalf("off topics used up the limit: %v", g)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/format-check?by=bogus", admin, nil); code != 400 {
		t.Fatalf("bad by: %d", code)
	}
}
