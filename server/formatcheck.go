package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Format check (#308): plain string rules over the newest messages, grouped
// by topic (or sender), with a fix prompt a user can paste into their agent. Advice only:
// nothing is blocked or changed, and it runs only when asked. Only what a
// user dismissed or turned off is stored.

// formatRules is every rule, in the order findings are listed, with the fix
// in plain words for the prompt.
type formatRule struct {
	id, fix string
	hit     func(n *Notification) bool
}

var formatRules = []formatRule{
	{"markdown_as_text", `Messages are sent as plain text but contain Markdown, so it shows as raw symbols. Send them as Markdown: "format": "markdown" in JSON (the default), or the header "Markdown: yes" in the ntfy style.`,
		func(n *Notification) bool { return n.Format == FormatText && looksMarkdown(n.Message) }},
	{"unknown_shortcode", `Messages contain :name: shortcodes clrd does not know, which show as typed. Use the emoji character itself, or a common name such as :warning: or :white_check_mark:.`,
		func(n *Notification) bool { _, a := emojiScan(n.Title); _, b := emojiScan(n.Message); return a+b > 0 }},
	{"no_title", `Messages have no title. Send a short title ("title" in JSON, the "Title" header in the ntfy style) and keep the detail in the message.`,
		func(n *Notification) bool { return n.Title == "" }},
	{"long_body", fmt.Sprintf(`Messages are longer than %d characters, too long to read on a phone. Send a short summary and put the full text behind a link ("click") or an attachment.`, longBody),
		func(n *Notification) bool { return utf8.RuneCountInString(n.Message) > longBody }},
	{"json_dump", `The message is a raw JSON document. Pick out the few fields a person needs and write them as a sentence or a short list.`,
		func(n *Notification) bool {
			m := strings.TrimSpace(n.Message)
			return m != "" && (m[0] == '{' || m[0] == '[') && json.Valid([]byte(m))
		}},
}

const longBody = 2000

// looksMarkdown reports Markdown that plain text would show as symbols:
// bold, a link, a code fence or a heading.
func looksMarkdown(s string) bool {
	if strings.Count(s, "**") >= 2 || strings.Contains(s, "](http") || strings.Contains(s, "```") {
		return true
	}
	for _, l := range strings.Split(s, "\n") {
		if h := strings.TrimLeft(l, "#"); len(h) < len(l) && len(l)-len(h) <= 6 && strings.HasPrefix(h, " ") {
			return true
		}
	}
	return false
}

type formatFinding struct {
	Rule      string `json:"rule"`
	Count     int    `json:"count"`
	ExampleID string `json:"example_id"` // the newest message that broke the rule
	Example   string `json:"example"`    // its title and start of its message
	Fix       string `json:"fix"`
}

type formatTopic struct {
	Topic    string          `json:"topic"`
	SenderID string          `json:"sender_id,omitempty"` // with by=sender: the sending token, "" when unknown
	Sender   string          `json:"sender,omitempty"`    // its name
	Checked  int             `json:"checked"`
	Problems int             `json:"problems"` // messages with at least one finding
	Findings []formatFinding `json:"findings"`
	Prompt   string          `json:"prompt"` // "" for a sender tok may not see
	hide     bool            // its sender is not tok's to name (#406)
}

type formatIgnore struct {
	Topic    string `json:"topic"`
	Rule     string `json:"rule"`
	SenderID string `json:"sender_id"` // "" for every sender
}

// FormatCheck runs the rules over the newest 500 messages tok can read
// (in topic, when given) and returns the groups with findings: one per
// topic, or with bySender one per topic and sending token (#320). Topics
// turned off and dismissed findings (#320) are left out and listed.
// ponytail: one page of 500, page through older ones if anyone needs more.
func (s *Server) FormatCheck(r *http.Request, tok *Token, topic string, bySender bool) (map[string]any, error) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT topic, rule, sender FROM format_ignore ORDER BY topic, rule, sender`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ignored, off, dismissed := map[formatIgnore]bool{}, []string{}, []formatIgnore{}
	for rows.Next() {
		var g formatIgnore
		if err := rows.Scan(&g.Topic, &g.Rule, &g.SenderID); err != nil {
			return nil, err
		}
		if tok.need(ScopeRead, g.Topic) != nil || topic != "" && g.Topic != topic {
			continue
		}
		if ignored[g] = true; g.Rule == "" {
			off = append(off, g.Topic)
		} else {
			dismissed = append(dismissed, g)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Off topics are left out in the query, so they never use up the 500.
	lq := ListQuery{State: "all", Limit: 500, Skip: off}
	if topic != "" {
		lq.Topics = []string{topic}
	}
	ns, err := s.List(r.Context(), tok, lq)
	if err != nil {
		return nil, err
	}
	// A sender's name, and the prompt that holds it, go only to a full admin
	// or to the person it belongs to, as in Linked devices (#245, #406).
	all := tok.needFullAdmin() == nil
	own, err := topIn(r.Context(), s.db, tok.ID)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	groups := map[[2]string]*formatTopic{}
	checked := len(ns)
	for _, n := range ns {
		key := [2]string{n.Topic}
		if bySender {
			key[1] = n.sender
		}
		t := groups[key]
		if t == nil {
			t = &formatTopic{Topic: n.Topic, SenderID: key[1], Findings: make([]formatFinding, len(formatRules))}
			t.hide = t.SenderID != "" && !all && t.SenderID != own
			if t.SenderID != "" && !t.hide {
				if _, ok := names[t.SenderID]; !ok {
					var name string
					if err := s.db.QueryRowContext(r.Context(), `SELECT name FROM tokens WHERE id = ?`, t.SenderID).Scan(&name); err != nil && err != sql.ErrNoRows {
						return nil, err
					}
					names[t.SenderID] = name
				}
				t.Sender = names[t.SenderID]
			}
			groups[key] = t
		}
		t.Checked++
		bad := false
		for i, rule := range formatRules {
			if ignored[formatIgnore{n.Topic, rule.id, ""}] || ignored[formatIgnore{n.Topic, rule.id, n.sender}] {
				continue
			}
			if f := &t.Findings[i]; rule.hit(n) {
				if f.Count == 0 { // newest first, so the first hit is the example
					*f = formatFinding{Rule: rule.id, ExampleID: n.ID, Example: excerpt(n), Fix: rule.fix}
				}
				f.Count++
				bad = true
			}
		}
		if bad {
			t.Problems++
		}
	}
	topics := []*formatTopic{}
	problems := 0
	for _, t := range groups {
		found := t.Findings[:0]
		for _, f := range t.Findings {
			if f.Count > 0 {
				found = append(found, f)
			}
		}
		if t.Findings = found; len(found) > 0 {
			if !t.hide {
				t.Prompt = fixPrompt(t, s.baseFor(r))
			}
			topics = append(topics, t)
			problems += t.Problems
		}
	}
	sort.Slice(topics, func(i, j int) bool {
		a, b := topics[i], topics[j]
		return a.Topic < b.Topic || a.Topic == b.Topic && a.SenderID < b.SenderID
	})
	return map[string]any{"checked": checked, "problems": problems, "topics": topics, "off": off, "dismissed": dismissed}, nil
}

// SetFormatIgnore dismisses a rule's findings in topic (for one sending
// token, or every one when senderID is ""), or with rule "" turns the
// check off for the topic; on false undoes it. Off by default (#320).
func (s *Server) SetFormatIgnore(ctx context.Context, tok *Token, topic, rule, senderID string, on bool) error {
	if err := validTopic(topic); err != nil {
		return err
	}
	if err := tok.need(ScopePublish, topic); err != nil {
		return err
	}
	if rule != "" && !slices.ContainsFunc(formatRules, func(r formatRule) bool { return r.id == rule }) {
		return errf(400, "bad_request", "unknown rule %q", rule)
	}
	if rule == "" && senderID != "" {
		return errf(400, "bad_request", "a topic turns off for every sender: leave sender_id out")
	}
	q := `INSERT OR IGNORE INTO format_ignore (topic, rule, sender) VALUES (?, ?, ?)`
	if !on {
		q = `DELETE FROM format_ignore WHERE topic = ? AND rule = ? AND sender = ?`
	}
	_, err := s.db.ExecContext(ctx, q, topic, rule, senderID)
	return err
}

// excerpt is the title and the first 200 characters of the message, with
// anything that looks like a secret hidden (#315): the prompt is pasted into
// an agent, so a key a sender put in a message must not ride along.
func excerpt(n *Notification) string {
	m := []rune(hideSecrets(n.Message)) // hide first, so a cut cannot split a secret from its marker
	s := string(m[:min(len(m), 200)])
	if len(m) > 200 {
		s += "..."
	}
	if n.Title != "" {
		s = hideSecrets(n.Title) + "\n" + s
	}
	return s
}

// secretValue is a value after password=, token: and the like, or after
// Bearer, quoted or bare; secretURL is the password in user:pass@host;
// secretLike is a long run mixing letters and digits, as keys and tokens are.
var (
	secretValue = regexp.MustCompile(`(?i)(\b(?:passw(?:or)?d|pass|secret|token|api[_-]?key|key|auth)["']?\s*[:=]\s*|\bbearer\s+)("[^"]*"|'[^']*'|[^\s"',;&}\]]+)`)
	secretURL   = regexp.MustCompile(`(://[^/\s:@]+:)[^/\s@]+@`)
	secretLike  = regexp.MustCompile(`[A-Za-z0-9_+/=-]{20,}`)
)

// ponytail: pattern guesses, not a scanner; a short secret with no keyword
// before it still shows. Fine for an example the user reads before pasting.
func hideSecrets(s string) string {
	s = secretURL.ReplaceAllString(s, "${1}[hidden]@")
	s = secretValue.ReplaceAllStringFunc(s, func(v string) string {
		k := secretValue.FindStringSubmatch(v)
		if q := k[2][:1]; q == `"` || q == "'" {
			return k[1] + q + "[hidden]" + q
		}
		return k[1] + "[hidden]"
	})
	return secretLike.ReplaceAllStringFunc(s, func(v string) string {
		if strings.ContainsAny(v, "0123456789") && strings.IndexFunc(v, unicode.IsLetter) >= 0 {
			return "[hidden]"
		}
		return v
	})
}

// fixPrompt is a prompt for the user's agent: what is wrong, an example, the
// format clrd expects and a test. It holds no token: $CLRD_TOKEN stands in.
func fixPrompt(t *formatTopic, base string) string {
	var b strings.Builder
	by := ""
	if t.Sender != "" {
		by = fmt.Sprintf(" with the token named %q", t.Sender)
	}
	fmt.Fprintf(&b, "Fix the program that sends notifications to the clrd topic %q%s. Of its last %d messages, %d have formatting problems:\n\n", t.Topic, by, t.Checked, t.Problems)
	for _, f := range t.Findings {
		fmt.Fprintf(&b, "- %d: %s\n", f.Count, f.Fix)
	}
	ex := t.Findings[0].Example
	fmt.Fprintf(&b, "\nExample of what it sends now:\n\n%s\n\n", ex)
	b.WriteString("clrd takes POST /api/v1/notifications with a JSON body: topic, title, message, format (\"markdown\", the default, or \"text\"), priority (1 to 5), tags, click (a link opened on tap). " +
		"Or the ntfy style: POST /<topic> with the message as the body and the headers Title, Priority, Tags, Click and Markdown.\n\n" +
		"Change only how messages are built. Keep the token where it is now. Then send one test message and check it looks right in the clrd inbox:\n\n")
	test, _ := json.Marshal(map[string]string{"topic": t.Topic, "title": "Format test", "message": "**Bold** works and so does :white_check_mark:"})
	fmt.Fprintf(&b, "curl -H \"Authorization: Bearer $CLRD_TOKEN\" -d '%s' %s/api/v1/notifications\n\nMore: %s/docs/formatting\n", test, base, base)
	return b.String()
}
