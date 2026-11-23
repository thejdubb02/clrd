package server

import (
	"encoding/json"
	"slices"
	"strings"
)

// ColorRule colors matching cards (#165, spec Color rules). Every client
// runs the same match: the first rule whose set fields all match wins, and a
// rule beats the sender's own color. The server only checks and stores them.
type ColorRule struct {
	Topic       string `json:"topic"`        // the topic it landed in; "" any
	Tag         string `json:"tag"`          // one of its tags; "" any
	PriorityMin int    `json:"priority_min"` // inclusive, 0 no limit
	PriorityMax int    `json:"priority_max"`
	Title       string `json:"title"` // text in the title, any case; "" any
	Color       string `json:"color"` // #RRGGBB
	Style       string `json:"style"` // one of colorStyles
}

var colorStyles = []string{"bar", "tint", "solid", "gradient"}

const maxColorRules = 50

// hexColor reports whether c is #RRGGBB: it ends up in CSS, so nothing else.
func hexColor(c string) bool {
	if len(c) != 7 || c[0] != '#' {
		return false
	}
	return strings.Trim(strings.ToLower(c[1:]), "0123456789abcdef") == ""
}

// colorRuleList reads a PUT's color_rules: null is a 400, like the swipe keys,
// and a field of the wrong type names its rule (#615).
func colorRuleList(raw json.RawMessage) ([]ColorRule, error) {
	var items []json.RawMessage
	if string(raw) == "null" || json.Unmarshal(raw, &items) != nil {
		return nil, errf(400, "bad_request", "color_rules must be a list of rules ([] clears it)")
	}
	rules := make([]ColorRule, len(items))
	for i, it := range items {
		if err := json.Unmarshal(it, &rules[i]); err != nil {
			return nil, errf(400, "bad_request", "color_rules[%d]: %v", i, err)
		}
	}
	return rules, checkColorRules(rules)
}

func checkColorRules(rules []ColorRule) error {
	if len(rules) > maxColorRules {
		return errf(400, "bad_request", "color_rules holds at most %d rules", maxColorRules)
	}
	for i, r := range rules {
		bad := func(f string, a ...any) error {
			return errf(400, "bad_request", "color_rules[%d]: "+f, append([]any{i}, a...)...)
		}
		switch {
		case r.Topic != "" && validTopic(r.Topic) != nil:
			return bad("bad topic %q", r.Topic)
		case len(r.Tag) > 64 || len(r.Title) > 100:
			return bad("tag is at most 64 bytes and title at most 100")
		case r.PriorityMin < 0 || r.PriorityMin > 5 || r.PriorityMax < 0 || r.PriorityMax > 5:
			return bad("priority_min and priority_max are 0 (no limit) to 5")
		case r.PriorityMax > 0 && r.PriorityMin > r.PriorityMax:
			return bad("priority_min is above priority_max")
		case !hexColor(r.Color):
			return bad("color %q must be #RRGGBB", r.Color)
		case !slices.Contains(colorStyles, r.Style):
			return bad("style %q must be one of: %s", r.Style, strings.Join(colorStyles, ", "))
		}
	}
	return nil
}
