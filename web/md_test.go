package web

import (
	"regexp"
	"strings"
	"testing"
)

func TestRenderBodyAllowList(t *testing.T) {
	allowed := map[string]bool{"p": true, "br": true, "strong": true, "em": true, "u": true, "del": true, "code": true,
		"pre": true, "ul": true, "ol": true, "li": true, "blockquote": true, "a": true, "hr": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true}
	tag := regexp.MustCompile(`<(/?)([a-zA-Z0-9]+)([^>]*)>`)
	for _, src := range []string{
		"<script>alert(1)</script>", "hi <iframe src=x></iframe>", "<style>body{}</style>", "<!-- c -->",
		`<img src=x onerror=alert(1)>`, `<u onclick="x()">u</u>`, "![pixel](https://e.example/p.png)",
		"[x](javascript:alert(1))", `[t](https://e.example "a\">b")`, "<div>\nblock\n</div>", "| a | b |\n|---|---|\n| 1 | 2 |",
		"- [ ] task", "<https://e.example/>", "go to https://e.example/x now", "a <nil> b <stdin>", "<u>open",
		"</u>stray", "```\n<b>x</b>\n```",
	} {
		out := string(renderBody(src, "", Snapshot{Formatting: true, Autolink: true}, ""))
		for _, m := range tag.FindAllStringSubmatch(out, -1) {
			if !allowed[strings.ToLower(m[2])] {
				t.Errorf("%q: tag <%s> in %q", src, m[2], out)
			}
			if m[2] == "a" && m[1] == "" && (!strings.Contains(m[3], `rel="noopener noreferrer"`) || strings.Contains(m[3], "javascript")) {
				t.Errorf("%q: bad link %q", src, m[0])
			}
			if m[2] == "u" && m[3] != "" {
				t.Errorf("%q: <u> with attributes %q", src, m[0])
			}
		}
		if strings.Count(out, "<u>") != strings.Count(out, "</u>") {
			t.Errorf("%q: unbalanced <u> in %q", src, out)
		}
	}
}

func TestRenderBody(t *testing.T) {
	for _, c := range []struct {
		src, format       string
		formatting, links bool
		want              string
	}{
		{"**b** *i* ++u++ <u>v</u> ~~s~~ `c`", "", true, true, "<p><strong>b</strong> <em>i</em> <u>u</u> <u>v</u> <del>s</del> <code>c</code></p>\n"},
		{"Disk 91%\nHost web1\nCheck it", "", true, true, "<p>Disk 91%<br>\nHost web1<br>\nCheck it</p>\n"},
		{"a <nil> b", "", true, true, "<p>a &lt;nil&gt; b</p>\n"},
		{"![pixel](https://e.example/p.png)", "", true, true, "<p>pixel</p>\n"},
		{"see https://e.example/x.", "", true, true, `<p>see <a rel="noopener noreferrer" href="https://e.example/x">https://e.example/x</a>.</p>` + "\n"},
		{"see https://e.example/x", "", true, false, "<p>see https://e.example/x</p>\n"},
		{"**raw** https://e.example/x.", "text", true, true, `<p class="whitespace-pre-line">**raw** <a href="https://e.example/x" rel="noopener noreferrer">https://e.example/x</a>.</p>`},
		{"**raw** <b>", "markdown", false, false, `<p class="whitespace-pre-line">**raw** &lt;b&gt;</p>`},
		{"<u>open", "", true, true, "<p><u>open</u></p>\n"},
		{"<u>open\n\nnext", "", true, true, "<p><u>open</u></p>\n<p>next</p>\n"},
		{"<https://e.example/a>", "text", true, true, `<p class="whitespace-pre-line">&lt;<a href="https://e.example/a" rel="noopener noreferrer">https://e.example/a</a>&gt;</p>`},
		{"x https://e.example/?a=1&b=2", "text", true, true, `<p class="whitespace-pre-line">x <a href="https://e.example/?a=1&amp;b=2" rel="noopener noreferrer">https://e.example/?a=1&amp;b=2</a></p>`},
		{"a ++ b ++ c", "", true, true, "<p>a ++ b ++ c</p>\n"},
	} {
		if got := string(renderBody(c.src, c.format, Snapshot{Formatting: c.formatting, Autolink: c.links}, "")); got != c.want {
			t.Errorf("renderBody(%q, %q, %v, %v)\n got %q\nwant %q", c.src, c.format, c.formatting, c.links, got, c.want)
		}
	}
}

func TestRenderBodyFavicons(t *testing.T) {
	snap := Snapshot{Formatting: true, Autolink: true, Favicons: true}
	got := string(renderBody("[a](https://user@Sub.example.com:8443/x?y=1&z=2) and https://b.example/", "", snap, ""))
	want := `<p><img class="favicon" src="/api/v1/favicon?host=Sub.example.com" alt="" width="16" height="16" loading="lazy"><a rel="noopener noreferrer" href="https://user@Sub.example.com:8443/x?y=1&amp;z=2">a</a> and <img class="favicon" src="/api/v1/favicon?host=b.example" alt="" width="16" height="16" loading="lazy"><a rel="noopener noreferrer" href="https://b.example/">https://b.example/</a></p>` + "\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := string(renderBody("see https://b.example/", "text", snap, "/clrd")); !strings.Contains(got, `src="/clrd/api/v1/favicon?host=b.example"`) {
		t.Errorf("plain text link has no icon: %q", got)
	}
	if got := string(renderBody("[a](HTTPS://b.example/)", "", snap, "")); !strings.Contains(got, `favicon?host=b.example"`) {
		t.Errorf("upper case scheme has no icon: %q", got)
	}
	if got := string(renderBody("[a](https://b.example/)", "", Snapshot{Formatting: true}, "")); strings.Contains(got, "<img") {
		t.Errorf("icon with favicons off: %q", got)
	}
}

func TestPreviewsOn(t *testing.T) {
	s := Snapshot{LinkPreviews: true, TopicPreviews: map[string]bool{"quiet": false, "on": true}}
	if !s.PreviewsOn("infra") || s.PreviewsOn("quiet") || s.TopicPreview("quiet") != "off" || s.TopicPreview("infra") != "" {
		t.Error("topic override ignored")
	}
	s.LinkPreviews = false
	if s.PreviewsOn("infra") || !s.PreviewsOn("on") || s.TopicPreview("on") != "on" {
		t.Error("default ignored")
	}
}

func TestFormatCards(t *testing.T) {
	f := []FormatFinding{{Rule: "no_title", Count: 1}}
	s := Snapshot{Format: []FormatTopic{
		{Topic: "a", SenderID: "1", Findings: f},
		{Topic: "b", SenderID: "1", Findings: f, CanFix: true},
		{Topic: "a", SenderID: "2", Findings: append(f, FormatFinding{Rule: "long_body"}), CanFix: true},
	}}
	c := s.FormatCards()
	if len(c) != 2 || c[0].Topic != "a" || len(c[0].Groups) != 2 || c[0].Findings != 3 || !c[0].CanFix || c[1].Topic != "b" || c[1].Findings != 1 {
		t.Errorf("cards: %+v", c)
	}
}
