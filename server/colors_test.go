package server

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// colorFor is the match every client runs (spec, Color rules): the first
// rule whose set fields all match, else the sender's color as a bar.
func colorFor(rules []ColorRule, n Notification) (string, string) {
	for _, r := range rules {
		if (r.Topic == "" || r.Topic == n.Topic) && (r.Tag == "" || slices.Contains(n.Tags, r.Tag)) &&
			(r.PriorityMin == 0 || n.Priority >= r.PriorityMin) && (r.PriorityMax == 0 || n.Priority <= r.PriorityMax) &&
			strings.Contains(strings.ToLower(n.Title), strings.ToLower(r.Title)) {
			return r.Color, r.Style
		}
	}
	if n.Color != "" {
		return n.Color, "bar"
	}
	return "", ""
}

// The shared fixture clients test against says what the spec says, and the
// server takes its rules.
func TestColorFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/colorrules.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Rules []ColorRule
		Cases []struct {
			N            Notification
			Color, Style string
		}
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if err := checkColorRules(f.Rules); err != nil {
		t.Fatal(err)
	}
	for i, c := range f.Cases {
		if color, style := colorFor(f.Rules, c.N); color != c.Color || style != c.Style {
			t.Errorf("case %d: got %s %s, want %s %s", i, color, style, c.Color, c.Style)
		}
	}
}

func TestColorRules(t *testing.T) {
	s, ts, admin := newTest(t)
	if _, st := call(t, ts, "GET", "/api/v1/settings", admin, nil); fmt.Sprint(st["color_rules"]) != "[]" {
		t.Fatalf("default: %v", st["color_rules"])
	}
	rules := []map[string]any{{"topic": "gatus", "priority_min": 4, "color": "#DC2626", "style": "tint"}, {"tag": "discord", "color": "#7c3aed", "style": "bar"}}
	code, st := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"color_rules": rules})
	if got, _ := st["color_rules"].([]any); code != 200 || len(got) != 2 || got[1].(map[string]any)["color"] != "#7c3aed" {
		t.Fatalf("put: %d %v", code, st["color_rules"])
	}
	for _, c := range []struct {
		want string
		rule map[string]any
	}{
		{"#RRGGBB", map[string]any{"color": "red", "style": "bar"}},
		{"#RRGGBB", map[string]any{"color": "#12345g", "style": "bar"}},
		{"#RRGGBB", map[string]any{"color": "#123;}x", "style": "bar"}},
		{"style", map[string]any{"color": "#123456", "style": "glow"}},
		{"above", map[string]any{"priority_min": 4, "priority_max": 2, "color": "#123456", "style": "bar"}},
		{"0 (no limit) to 5", map[string]any{"priority_max": 6, "color": "#123456", "style": "bar"}},
		{"topic", map[string]any{"topic": "a b", "color": "#123456", "style": "bar"}},
		{"title", map[string]any{"title": strings.Repeat("x", 101), "color": "#123456", "style": "bar"}},
	} {
		code, body := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"color_rules": []any{c.rule}})
		if msg, _ := body["error"].(string); code != 400 || !strings.Contains(msg, c.want) {
			t.Errorf("%v: %d %q", c.rule, code, msg)
		}
	}
	for _, c := range []struct {
		want string
		v    any
	}{
		{"color_rules must be a list", nil},
		{"color_rules must be a list", "x"},
		{"color_rules[1]: ", []any{map[string]any{"color": "#123456", "style": "bar"}, map[string]any{"color": 123, "style": "bar"}}},
	} {
		code, body := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"color_rules": c.v})
		if msg, _ := body["error"].(string); code != 400 || !strings.Contains(msg, c.want) {
			t.Errorf("%v: %d %q", c.v, code, msg)
		}
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"color_rules": make([]map[string]any, 51)}); code != 400 {
		t.Errorf("51 rules: %d", code)
	}
	// A limited token sees only rules for its topics or none, and changes none.
	lim := mkToken(t, s, "phone", []string{ScopeRead}, []string{"mine"})
	if _, st := call(t, ts, "GET", "/api/v1/settings", lim, nil); fmt.Sprint(st["color_rules"]) != "[map[color:#7c3aed priority_max:0 priority_min:0 style:bar tag:discord title: topic:]]" {
		t.Fatalf("limited read: %v", st["color_rules"])
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", lim, map[string]any{"color_rules": []any{}}); code != 403 {
		t.Fatalf("limited put: %d", code)
	}
	// An empty list clears them; leaving the key out keeps them.
	call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"autolink": false})
	if _, st := call(t, ts, "GET", "/api/v1/settings", admin, nil); len(st["color_rules"].([]any)) != 2 {
		t.Fatal("another field wiped the rules")
	}
	if _, st := call(t, ts, "PUT", "/api/v1/settings", admin, map[string]any{"color_rules": []any{}}); fmt.Sprint(st["color_rules"]) != "[]" {
		t.Fatalf("clear: %v", st["color_rules"])
	}
}

// A sender's own color is kept, follows an update by external_id, and is
// checked like a rule's.
func TestPublishColor(t *testing.T) {
	_, ts, admin := newTest(t)
	code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "m", "external_id": "d", "color": "#0EA5E9"})
	if code != 201 && code != 200 || n["color"] != "#0EA5E9" {
		t.Fatalf("publish: %d %v", code, n["color"])
	}
	_, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "m2", "external_id": "d"})
	if _, got := call(t, ts, "GET", "/api/v1/notifications/"+n["id"].(string), admin, nil); got["color"] != "" {
		t.Fatalf("update kept the old color: %v", got["color"])
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "m", "color": "url(x)"}); code != 400 {
		t.Fatalf("bad color: %d", code)
	}
}
