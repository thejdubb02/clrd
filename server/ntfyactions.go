package server

import (
	"encoding/json"
	"strings"
	"unicode"
)

// ntfy's action buttons (#401): view, http and broadcast map onto Action;
// copy and anything newer are dropped. http and broadcast reach a topic only
// where an admin turned device_actions on (deviceactions.go); elsewhere a
// ntfy publish drops them, as before, instead of failing.

type ntfyAction struct {
	ID      string            `json:"id"`
	Action  string            `json:"action"`
	Label   string            `json:"label"`
	URL     string            `json:"url,omitempty"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Intent  string            `json:"intent,omitempty"`
	Extras  map[string]string `json:"extras,omitempty"`
	Clear   bool              `json:"clear"`
}

func (a ntfyAction) action() (Action, bool) {
	out := Action{Label: a.Label, Clear: a.Clear}
	switch strings.ToLower(a.Action) {
	case "view":
		out.URL = a.URL
		return out, a.URL != ""
	case "http":
		out.HTTP = &HTTPAction{Method: a.Method, URL: a.URL, Headers: a.Headers, Body: a.Body}
	case "broadcast":
		out.Broadcast = &BroadcastAction{Intent: a.Intent, Extras: a.Extras}
	default:
		return Action{}, false
	}
	return out, true
}

// ntfyActionOut is how a ntfy subscriber sees an action; reply has no ntfy form.
func ntfyActionOut(a Action) (ntfyAction, bool) {
	na := ntfyAction{Label: a.Label, Clear: a.Clear}
	switch {
	case a.URL != "":
		na.Action, na.URL = "view", a.URL
	case a.HTTP != nil:
		na.Action, na.URL, na.Method, na.Headers, na.Body = "http", a.HTTP.URL, a.HTTP.Method, a.HTTP.Headers, a.HTTP.Body
	case a.Broadcast != nil:
		na.Action, na.Intent, na.Extras = "broadcast", a.Broadcast.Intent, a.Broadcast.Extras
	default:
		return na, false
	}
	return na, true
}

// ntfyActions reads ntfy's JSON array form or its short form
// ("view, Open, https://x, clear=true; http, Off, https://y, body='{"on": false}'").
func ntfyActions(v string) ([]Action, error) {
	var as []ntfyAction
	if v = strings.TrimSpace(v); v == "" {
		return nil, nil
	}
	if strings.HasPrefix(v, "[") {
		if err := json.Unmarshal([]byte(v), &as); err != nil {
			return nil, errf(400, "bad_request", "invalid actions: %v", err)
		}
	} else {
		fields, err := ntfySplitActions(v)
		if err != nil {
			return nil, err
		}
		for _, f := range fields {
			as = append(as, ntfyShortAction(f))
		}
	}
	return ntfyActionList(as), nil
}

func ntfyActionList(actions []ntfyAction) []Action {
	var out []Action
	for _, a := range actions {
		if pa, ok := a.action(); ok {
			out = append(out, pa)
		}
	}
	return out
}

// ntfySplitActions splits the short form into actions (";") of fields (",").
// A quote opens only at the start of a field or right after "=", so a label
// like Don't stays as written. Inside quotes, spaces stay and \" (or \')
// is the quote itself (#446); outside, spaces around a value are dropped.
func ntfySplitActions(v string) ([][]string, error) {
	var acts [][]string
	var fields []string
	var b strings.Builder
	var q rune
	keep := 0 // b up to here ends in quoted text, whose spaces stay
	field := func() {
		s := b.String()
		fields = append(fields, s[:keep]+strings.TrimRightFunc(s[keep:], unicode.IsSpace))
		b.Reset()
		keep = 0
	}
	rs := []rune(v)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		cur := b.String()
		key := strings.Count(cur, "=") == 1 && strings.HasSuffix(strings.TrimSpace(cur), "=") // just past key=
		switch {
		case q != 0 && c == '\\' && i+1 < len(rs) && rs[i+1] == q:
			b.WriteRune(q)
			i++
		case q != 0 && c == q:
			q, keep = 0, b.Len()
		case q != 0:
			b.WriteRune(c)
		case unicode.IsSpace(c) && (keep > 0 && b.Len() == keep || cur == "" || key && strings.HasSuffix(cur, "=")):
			// before a value or after a closing quote
		case (c == '\'' || c == '"') && (cur == "" || key):
			q = c
		case c == ',':
			field()
		case c == ';':
			field()
			acts, fields = append(acts, fields), nil
		default:
			b.WriteRune(c)
		}
	}
	if q != 0 {
		return nil, errf(400, "bad_request", "invalid actions: a quote is not closed")
	}
	field()
	return append(acts, fields), nil
}

// ntfyShortAction reads one short-form action: action, label, url by
// position, the rest as key=value, headers.X=... and extras.x=...
func ntfyShortAction(fields []string) ntfyAction {
	var a ntfyAction
	pos := []*string{&a.Action, &a.Label, &a.URL} // by index, as ntfy reads them
	keys := map[string]*string{"action": &a.Action, "label": &a.Label, "url": &a.URL, "method": &a.Method, "body": &a.Body, "intent": &a.Intent}
	for i, f := range fields {
		rawK, v, eq := strings.Cut(f, "=")
		rawK = strings.TrimSpace(rawK) // the splitter trimmed v, but not inside quotes
		k := strings.ToLower(rawK)
		switch {
		case eq && keys[k] != nil:
			*keys[k] = v
		case eq && k == "clear":
			a.Clear = strings.EqualFold(v, "true") || v == "1" || strings.EqualFold(v, "yes")
		case eq && strings.HasPrefix(k, "headers.") && len(k) > len("headers."):
			a.Headers = setKey(a.Headers, rawK[len("headers."):], v)
		case eq && strings.HasPrefix(k, "extras.") && len(k) > len("extras."):
			a.Extras = setKey(a.Extras, rawK[len("extras."):], v)
		case i < len(pos): // no key, or not one ntfy knows: "Show x=y", https://x?a=b

			*pos[i] = f
		}
	}
	return a
}

func setKey(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[k] = v
	return m
}
