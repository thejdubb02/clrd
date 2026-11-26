package server

import (
	"encoding/json"
	"io/fs"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The build packs a sorted, alias-only table. Keep it as text in memory so
// there is no full-table parse or map allocation at startup.
var emojiAliases = func() string {
	b, err := fs.ReadFile(serverFiles, "emoji_aliases.txt")
	if err != nil {
		panic(err)
	}
	_, rows, _ := strings.Cut(string(b), "\n---\n")
	return rows
}()

// emojiAlias binary-searches the sorted rows. Both bounds stay at line
// boundaries, so each step compares one alias without allocating an index.
func emojiAlias(name string) string {
	lo, hi := 0, len(emojiAliases)
	for lo < hi {
		mid := lo + (hi-lo)/2
		start := lo + strings.LastIndexByte(emojiAliases[lo:mid], '\n') + 1
		end := mid + strings.IndexByte(emojiAliases[mid:hi], '\n')
		alias, emoji, _ := strings.Cut(emojiAliases[start:end], "\t")
		switch {
		case name < alias:
			hi = start
		case name > alias:
			lo = end + 1
		default:
			return emoji
		}
	}
	return ""
}

func nameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '+' || c == '-'
}

// word reports whether r is a letter or digit in any script, so é:b:ç stays as typed.
func word(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// closeFence is where the run of exactly len(fence) backticks closing a code
// span starts in s, or -1: a longer or shorter run does not close it.
func closeFence(s, fence string) int {
	for k := 0; k < len(s); {
		e := strings.Index(s[k:], fence)
		if e < 0 {
			return -1
		}
		e += k
		n := len(fence)
		for e+n < len(s) && s[e+n] == '`' {
			n++
		}
		if n == len(fence) {
			return e
		}
		k = e + n
	}
	return -1
}

// emojify shows the known shortcodes in s as emoji, outside `code` and ```blocks```.
func emojify(s string) string { out, _ := emojiScan(s); return out }

// emojiScan is emojify plus the number of shortcode-shaped names it did not
// know (with a letter, so :30: is not one), for the format check (#308).
func emojiScan(s string) (string, int) {
	unknown := 0
	if !strings.Contains(s, ":") {
		return s, 0
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '`' { // copy a code span or block through to its closing fence
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			if end := closeFence(s[i+n:], s[i:i+n]); end >= 0 {
				b.WriteString(s[i : i+n+end+n])
				i += n + end + n
				continue
			}
			b.WriteString(s[i : i+n]) // no closing fence: plain backticks
			i += n
			continue
		}
		if before, _ := utf8.DecodeLastRuneInString(s[:i]); s[i] == ':' && !word(before) {
			j := i + 1
			for j < len(s) && nameByte(s[j]) {
				j++
			}
			if after, _ := utf8.DecodeRuneInString(s[min(j+1, len(s)):]); j < len(s) && s[j] == ':' && !word(after) {
				if e := emojiAlias(s[i+1 : j]); e != "" {
					b.WriteString(e)
					i = j + 1
					continue
				}
				if strings.ContainsAny(s[i+1:j], "abcdefghijklmnopqrstuvwxyz") {
					unknown++
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), unknown
}

// tagEmoji is the emoji of every tag that names one, as ntfy apps put in
// front of the title, and "" when none does.
func tagEmoji(tags []string) string {
	var b strings.Builder
	for _, t := range tags {
		b.WriteString(emojiAlias(t))
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + " "
}

// shown is the title and message as sent: shortcodes as emoji and the tags'
// emoji in front of the title, or of the message when there is no title.
func (n Notification) shown() (title, message string) {
	title, message = emojify(n.Title), emojify(n.Message)
	if p := tagEmoji(n.Tags); p != "" {
		if title != "" {
			title = p + strings.TrimPrefix(title, p)
		} else {
			message = p + strings.TrimPrefix(message, p)
		}
	}
	return
}

// MarshalJSON sends the shown text, so every API, stream and webhook gets it.
func (n Notification) MarshalJSON() ([]byte, error) {
	type plain Notification // no methods: no recursion
	p := plain(n)
	p.Title, p.Message = n.shown()
	return json.Marshal(p)
}
