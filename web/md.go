package web

import (
	"bytes"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Message bodies use the Markdown subset of docs/SPEC.md section 2.1: bold,
// italic, underline (<u> or ++text++), strikethrough, code, code blocks,
// lists, quotes, headings and links. Any other HTML shows as typed, images
// show their alt text (a remote image in a message is a tracking pixel), and
// every line break the sender typed is kept, so a plain alert looks the same
// as before. No tables or task lists: they are not in the subset.
var (
	mdLinks   = newMD(true)
	mdNoLinks = newMD(false)
	bareURL   = regexp.MustCompile(`https?://[^\s<>"]+[^\s<>".,;:!?)\]'*_]`)
	linkHref  = regexp.MustCompile(`<a [^>]*href="((?i:https?)://[^"]+)"`)
	uTag      = regexp.MustCompile(`</?u>|</(?:p|li|h[1-6]|blockquote)>`)
)

func newMD(autolink bool) goldmark.Markdown {
	ext := []goldmark.Extender{extension.Strikethrough, underlineExt{}}
	if autolink {
		ext = append(ext, extension.NewLinkify(extension.WithLinkifyAllowedProtocols([][]byte{[]byte("http:"), []byte("https:")})))
	}
	return goldmark.New(goldmark.WithExtensions(ext...), goldmark.WithRendererOptions(gmhtml.WithHardWraps()))
}

// renderBody renders a message for its card, following the display settings
// in snap. A plain text message (format "text", or every message while
// formatting is off) shows exactly as sent, with web addresses as links when
// autolink is on, as on Android. With favicons on, each web link gets its
// site's icon, fetched by the server (the browser never contacts the site).
func renderBody(src, format string, snap Snapshot) template.HTML {
	out := body(src, format, snap.Formatting, snap.Autolink)
	if !snap.Favicons {
		return template.HTML(out)
	}
	return template.HTML(linkHref.ReplaceAllStringFunc(out, func(a string) string {
		u, err := url.Parse(html.UnescapeString(linkHref.FindStringSubmatch(a)[1]))
		if err != nil || u.Hostname() == "" {
			return a
		}
		return `<img class="favicon" src="/api/v1/favicon?host=` + url.QueryEscape(u.Hostname()) + `" alt="" width="16" height="16" loading="lazy">` + a
	}))
}

func body(src, format string, formatting, autolink bool) string {
	if format == "text" || !formatting {
		var b strings.Builder
		at := 0
		if autolink {
			for _, m := range bareURL.FindAllStringIndex(src, -1) {
				u := template.HTMLEscapeString(src[m[0]:m[1]])
				b.WriteString(template.HTMLEscapeString(src[at:m[0]]) + `<a href="` + u + `" rel="noopener noreferrer">` + u + `</a>`)
				at = m[1]
			}
		}
		b.WriteString(template.HTMLEscapeString(src[at:]))
		return `<p class="whitespace-pre-line">` + b.String() + `</p>`
	}
	md := mdNoLinks
	if autolink {
		md = mdLinks
	}
	var b bytes.Buffer
	if err := md.Convert([]byte(src), &b); err != nil {
		return `<p class="whitespace-pre-line">` + template.HTMLEscapeString(src) + `</p>`
	}
	out := strings.NewReplacer(`<a href=`, `<a rel="noopener noreferrer" href=`, `<pre>`, `<pre tabindex="0">`).Replace(b.String())
	return balanceU(out)
}

// balanceU drops a </u> that closes nothing and closes a <u> left open at
// the end of its block, so a stray tag cannot underline the next paragraph or
// the rest of the page. Only the renderer writes these tags: a sender's own
// text is escaped.
func balanceU(s string) string {
	depth := 0
	s = uTag.ReplaceAllStringFunc(s, func(t string) string {
		if t != "<u>" && t != "</u>" { // end of a block
			t = strings.Repeat("</u>", depth) + t
			depth = 0
			return t
		}
		if t == "<u>" {
			depth++
			return t
		}
		if depth == 0 {
			return "&lt;/u&gt;"
		}
		depth--
		return t
	})
	return s + strings.Repeat("</u>", depth)
}

// underlineExt adds ++text++ and keeps raw HTML as typed, except <u> and </u>.
type underlineExt struct{}

func (underlineExt) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(util.Prioritized(underlineParser{}, 500)))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(util.Prioritized(safeRenderer{}, 100)))
}

var kindUnderline = gast.NewNodeKind("Underline")

type underline struct{ gast.BaseInline }

func (*underline) Kind() gast.NodeKind             { return kindUnderline }
func (n *underline) Dump(source []byte, level int) { gast.DumpHelper(n, source, level, nil, nil) }

type underlineDelim struct{}

func (underlineDelim) IsDelimiter(b byte) bool                   { return b == '+' }
func (underlineDelim) CanOpenCloser(o, c *parser.Delimiter) bool { return o.Char == c.Char }
func (underlineDelim) OnMatch(int) gast.Node                     { return &underline{} }

type underlineParser struct{}

func (underlineParser) Trigger() []byte { return []byte{'+'} }

func (underlineParser) Parse(_ gast.Node, block text.Reader, pc parser.Context) gast.Node {
	before := block.PrecendingCharacter()
	line, seg := block.PeekLine()
	d := parser.ScanDelimiter(line, before, 2, underlineDelim{})
	if d == nil || d.OriginalLength != 2 || before == '+' {
		return nil
	}
	d.Segment = seg.WithStop(seg.Start + d.OriginalLength)
	block.Advance(d.OriginalLength)
	pc.PushDelimiter(d)
	return d
}

type safeRenderer struct{}

func (safeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindUnderline, func(w util.BufWriter, _ []byte, _ gast.Node, entering bool) (gast.WalkStatus, error) {
		if entering {
			w.WriteString("<u>")
		} else {
			w.WriteString("</u>")
		}
		return gast.WalkContinue, nil
	})
	reg.Register(gast.KindRawHTML, func(w util.BufWriter, src []byte, n gast.Node, entering bool) (gast.WalkStatus, error) {
		if entering {
			segs := n.(*gast.RawHTML).Segments
			var raw []byte
			for i := range segs.Len() {
				seg := segs.At(i)
				raw = append(raw, seg.Value(src)...)
			}
			if s := strings.ToLower(string(raw)); s == "<u>" || s == "</u>" {
				w.WriteString(s)
			} else {
				w.Write(util.EscapeHTML(raw))
			}
		}
		return gast.WalkSkipChildren, nil
	})
	reg.Register(gast.KindHTMLBlock, func(w util.BufWriter, src []byte, node gast.Node, entering bool) (gast.WalkStatus, error) {
		n := node.(*gast.HTMLBlock)
		if entering {
			w.WriteString(`<p class="whitespace-pre-line">`)
			for i := range n.Lines().Len() {
				line := n.Lines().At(i)
				w.Write(util.EscapeHTML(line.Value(src)))
			}
			return gast.WalkContinue, nil
		}
		if n.HasClosure() {
			w.Write(util.EscapeHTML(n.ClosureLine.Value(src)))
		}
		w.WriteString("</p>\n")
		return gast.WalkContinue, nil
	})
	// An image shows its alt text: its children are the text.
	reg.Register(gast.KindImage, func(util.BufWriter, []byte, gast.Node, bool) (gast.WalkStatus, error) {
		return gast.WalkContinue, nil
	})
}
