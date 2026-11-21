package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Format check (#308): plain string rules over the newest messages, grouped
// by topic, with a fix prompt a user can paste into their agent. Advice only:
// nothing is stored, blocked or changed, and it runs only when asked.

// formatRules is every rule, in the order findings are listed, with the fix
// in plain words for the prompt.
var formatRules = []struct {
	id, fix string
	hit     func(n *Notification) bool
}{
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
	Checked  int             `json:"checked"`
	Problems int             `json:"problems"` // messages with at least one finding
	Findings []formatFinding `json:"findings"`
	Prompt   string          `json:"prompt"`
}

// FormatCheck runs the rules over the newest 500 messages tok can read
// (in topic, when given) and returns the topics with findings.
// ponytail: one page of 500, page through older ones if anyone needs more.
func (s *Server) FormatCheck(r *http.Request, tok *Token, topic string) (map[string]any, error) {
	lq := ListQuery{State: "all", Limit: 500}
	if topic != "" {
		lq.Topics = []string{topic}
	}
	ns, err := s.List(r.Context(), tok, lq)
	if err != nil {
		return nil, err
	}
	byTopic := map[string]*formatTopic{}
	for _, n := range ns {
		t := byTopic[n.Topic]
		if t == nil {
			t = &formatTopic{Topic: n.Topic, Findings: make([]formatFinding, len(formatRules))}
			byTopic[n.Topic] = t
		}
		t.Checked++
		bad := false
		for i, rule := range formatRules {
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
	for _, t := range byTopic {
		found := t.Findings[:0]
		for _, f := range t.Findings {
			if f.Count > 0 {
				found = append(found, f)
			}
		}
		if t.Findings = found; len(found) > 0 {
			t.Prompt = fixPrompt(t, s.baseFor(r))
			topics = append(topics, t)
			problems += t.Problems
		}
	}
	sort.Slice(topics, func(i, j int) bool { return topics[i].Topic < topics[j].Topic })
	return map[string]any{"checked": len(ns), "problems": problems, "topics": topics}, nil
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
	fmt.Fprintf(&b, "Fix the program that sends notifications to the clrd topic %q. Of its last %d messages, %d have formatting problems:\n\n", t.Topic, t.Checked, t.Problems)
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
