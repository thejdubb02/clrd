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
