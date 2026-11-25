package me.itsjustin.clrd

import android.graphics.Typeface
import android.text.SpannableString
import android.text.Spanned
import android.text.style.StyleSpan
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.foundation.text.appendInlineContent
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withLink
import androidx.compose.ui.text.withStyle
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

/**
 * The Markdown subset from docs/SPEC.md section 2.1, drawn as styled text: bold, italic, underline (`<u>` or `++`),
 * strike, code, code blocks, lists, quotes, small headings and http(s) links. No images, no other HTML: anything
 * else stays as the sender typed it. One pass over each line, no backtracking across lines, so any input is quick.
 */
object Md {
    private val BOLD = SpanStyle(fontWeight = FontWeight.Bold)
    private val ITALIC = SpanStyle(fontStyle = FontStyle.Italic)
    private val UNDER = SpanStyle(textDecoration = TextDecoration.Underline)
    private val STRIKE = SpanStyle(textDecoration = TextDecoration.LineThrough)

    // Order matters: code first so nothing inside it is touched, then links, then the paired markers.
    // A marker pair must hug its text (no space just inside) and "_" only counts at word edges, so
    // snake_case, globs and "2 * 3" stay literal.
    // A styled run is at most 300 characters, so an unclosed marker costs a short scan, not the rest of the line.
    private const val BARE_URL = """https?://[^\s<>]+"""
    private val INLINE = Regex(
        """`([^`]+)`""" +
            """|\[([^\]\n]+)]\((https?://[^\s)]+)\)""" +
            "|(" + BARE_URL + ")" +
            """|\*\*(?=\S)(.{1,300}?)(?<=\S)\*\*""" +
            """|(?<!\w)__(?=\S)(.{1,300}?)(?<=\S)__(?!\w)""" +
            """|~~(?=\S)(.{1,300}?)(?<=\S)~~""" +
            """|\+\+(?=\S)(.{1,300}?)(?<=\S)\+\+""" +
            """|<u>(.{1,300}?)</u>""" +
            """|\*(?=[^\s*])(.{1,300}?)(?<=[^\s*])\*""" +
            """|(?<!\w)_(?=[^\s_])(.{1,300}?)(?<=[^\s_])_(?!\w)""" +
            """|\\([\\`*_~+\[\]()<>#.!-])""" +
            """|<(https?://[^\s<>]+)>""", // an address in angle brackets is a written link: no brackets shown, even with autolink off
    )
    private val BARE = Regex(BARE_URL)
    private val HEADING = Regex("""^#{1,6}\s+(.*)""")
    private val BULLET = Regex("""^(\s*)[-*+]\s+(.*)""")
    private val NUMBER = Regex("""^(\s*)(\d{1,9}[.)])\s+(.*)""")
    private val QUOTE = Regex("""^>\s?(.*)""")
    private const val MAX_DEPTH = 4
    const val ICON = "icon:"

    /** The site a link goes to, which names its icon's inline content; null when it has none. */
    fun host(url: String) = url.toHttpUrlOrNull()?.host

    /**
     * [src] drawn as Markdown, or exactly as sent when [markdown] is false. With [icons], each link has an inline
     * content slot before it, named "icon:" and its [host], for the site's icon (the favicons setting, #107). With
     * [autolink] off, a bare web address stays text; a written `[words](address)` link is still a link.
     */
    fun render(src: String, markdown: Boolean = true, link: Color = Color.Unspecified, muted: Color = Color.Unspecified, icons: Boolean = false, autolink: Boolean = true): AnnotatedString {
        val linkStyle = TextLinkStyles(SpanStyle(color = link, textDecoration = TextDecoration.Underline))
        val linked: AnnotatedString.Builder.(String, String) -> Unit = { url, label ->
            if (icons) host(url)?.let { appendInlineContent(ICON + it, "\u200B") }
            withLink(LinkAnnotation.Url(url, linkStyle)) { append(label) }
        }
        if (!markdown && !autolink) return AnnotatedString(src)
        if (!markdown) return buildAnnotatedString {
            var at = 0
            for (m in BARE.findAll(src)) {
                append(src, at, m.range.first)
                val url = bareUrl(m.value)
                linked(url, url)
                at = m.range.first + url.length
            }
            append(src, at, src.length)
        }
        val code = SpanStyle(fontFamily = FontFamily.Monospace)
        return buildAnnotatedString {
            var fence = false
            var first = true
            for (line in src.lines()) {
                if (line.trimStart().startsWith("```")) {
                    fence = !fence // the fence line itself is not shown
                    continue
                }
                if (!first) append('\n')
                first = false
                val heading = HEADING.matchEntire(line)
                val bullet = BULLET.matchEntire(line)
                val number = NUMBER.matchEntire(line)
                val quote = QUOTE.matchEntire(line)
                when {
                    fence -> withStyle(code) { append(line) }
                    heading != null -> withStyle(BOLD) { inline(heading.groupValues[1], linked, code, 0, autolink) }
                    bullet != null -> {
                        append(bullet.groupValues[1] + "\u2022 ")
                        inline(bullet.groupValues[2], linked, code, 0, autolink)
                    }
                    number != null -> {
                        append(number.groupValues[1] + number.groupValues[2] + " ")
                        inline(number.groupValues[3], linked, code, 0, autolink)
                    }
                    quote != null -> withStyle(SpanStyle(color = muted, fontStyle = FontStyle.Italic)) {
                        append("| ")
                        inline(quote.groupValues[1], linked, code, 0, autolink)
                    }
                    else -> inline(line, linked, code, 0, autolink)
                }
            }
        }
    }

    private fun AnnotatedString.Builder.inline(s: String, linked: AnnotatedString.Builder.(String, String) -> Unit, code: SpanStyle, depth: Int, autolink: Boolean) {
        if (depth >= MAX_DEPTH) {
            append(s)
            return
        }
        var at = 0
        for (m in INLINE.findAll(s)) {
            append(s, at, m.range.first)
            at = m.range.last + 1
            val g = m.groups
            fun nest(style: SpanStyle, inner: String) = withStyle(style) { inline(inner, linked, code, depth + 1, autolink) }
            when {
                g[1] != null -> withStyle(code) { append(g[1]!!.value) }
                g[2] != null -> linked(g[3]!!.value, g[2]!!.value)
                g[4] != null && !autolink -> append(g[4]!!.value) // whole match as text, so nothing inside it is styled
                g[4] != null -> {
                    val url = bareUrl(g[4]!!.value)
                    linked(url, url)
                    at = m.range.first + url.length
                }
                g[5] != null -> nest(BOLD, g[5]!!.value)
                g[6] != null -> nest(BOLD, g[6]!!.value)
                g[7] != null -> nest(STRIKE, g[7]!!.value)
                g[8] != null -> nest(UNDER, g[8]!!.value)
                g[9] != null -> nest(UNDER, g[9]!!.value)
                g[10] != null -> nest(ITALIC, g[10]!!.value)
                g[11] != null -> nest(ITALIC, g[11]!!.value)
                g[12] != null -> append(g[12]!!.value)
                g[13] != null -> linked(g[13]!!.value, g[13]!!.value)
            }
        }
        append(s, at, s.length)
    }

    // A sentence ending in a URL keeps its full stop outside the link.
    private fun bareUrl(s: String) = s.trimEnd('.', ',', ';', ':', '!', '?', '\'', '"').let { u ->
        if (u.endsWith(')') && u.count { it == '(' } < u.count { it == ')' }) u.dropLast(1) else u
    }

    /** For the system notification: the text without markers, with bold and italic only (docs/SPEC.md, issue #107). */
    fun tray(src: String, markdown: Boolean): CharSequence { // links do not show in the tray, so autolink does not matter here
        if (!markdown) return src
        val a = render(src)
        val out = SpannableString(a.text)
        for (r in a.spanStyles) {
            if (r.item.fontWeight == FontWeight.Bold) out.setSpan(StyleSpan(Typeface.BOLD), r.start, r.end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
            if (r.item.fontStyle == FontStyle.Italic) out.setSpan(StyleSpan(Typeface.ITALIC), r.start, r.end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
        }
        return out
    }
}
