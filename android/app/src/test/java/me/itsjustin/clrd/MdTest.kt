package me.itsjustin.clrd

import android.graphics.Typeface
import android.text.Spanned
import android.text.style.StyleSpan
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** The Markdown subset (docs/SPEC.md section 2.1), and above all the ordinary alert text it must leave alone. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class MdTest {
    private fun text(s: String) = Md.render(s).text
    private fun AnnotatedString.styled(part: String) = spanStyles.filter { text.substring(it.start, it.end) == part }.map { it.item }
    private fun AnnotatedString.links() = getLinkAnnotations(0, length).map { text.substring(it.start, it.end) to (it.item as LinkAnnotation.Url).url }

    @Test fun inlineMarkers() {
        val a = Md.render("**Priority:** low, *soon*, _maybe_, ~~old~~, ++new++, <u>here</u>, `x*y`")
        assertEquals("Priority: low, soon, maybe, old, new, here, x*y", a.text)
        assertEquals(FontWeight.Bold, a.styled("Priority:").single().fontWeight)
        assertEquals(FontStyle.Italic, a.styled("soon").single().fontStyle)
        assertEquals(FontStyle.Italic, a.styled("maybe").single().fontStyle)
        assertEquals(TextDecoration.LineThrough, a.styled("old").single().textDecoration)
        assertEquals(TextDecoration.Underline, a.styled("new").single().textDecoration)
        assertEquals(TextDecoration.Underline, a.styled("here").single().textDecoration)
        assertEquals(FontFamily.Monospace, a.styled("x*y").single().fontFamily)
        val nested = Md.render("**bold _and italic_**")
        assertEquals("bold and italic", nested.text)
        assertEquals(FontWeight.Bold, nested.styled("bold and italic").single().fontWeight)
        assertEquals(FontStyle.Italic, nested.styled("and italic").single().fontStyle)
    }

    @Test fun blocks() {
        assertEquals("Disk\n• one\n  • two\n1. first\n| said\nplain", text("## Disk\n- one\n  * two\n1. first\n> said\nplain"))
        val code = Md.render("before\n```\n**not bold** _x_\n```\nafter")
        assertEquals("before\n**not bold** _x_\nafter", code.text)
        assertEquals(FontFamily.Monospace, code.styled("**not bold** _x_").single().fontFamily)
        assertEquals("a\n**b**", text("a\n```\n**b**")) // an unclosed fence runs to the end
        assertEquals("line\n\nnext\n", text("line\n\nnext\n"))
    }

    @Test fun ordinaryTextStaysLiteral() {
        for (s in listOf(
            "disk_usage_percent on db_1", "rm *.log and *.tmp", "2 * 3 = 6", "Fixes #123", "a ** b", "x__init__y",
            "price: 5*", "* not a list without text", "<b>no html</b>", "[not a link](javascript:alert(1))",
            "file_name_here.txt", "set max_conn to pool_", "~/home ~", "C++ and ++i",
        )) assertEquals(s, text(s).replace("• ", "* "))
        assertEquals("__init__", text("`__init__`"))
        assertEquals("!alt", text("![alt](https://x.example/p.png)")) // never an image, the link stays a link
    }

    @Test fun links() {
        val a = Md.render("See [the run](https://ci.example/r/1). Or https://x.example/a_b*c~d.")
        assertEquals("See the run. Or https://x.example/a_b*c~d.", a.text)
        assertEquals(listOf("the run" to "https://ci.example/r/1", "https://x.example/a_b*c~d" to "https://x.example/a_b*c~d"), a.links())
        assertEquals(listOf("https://en.example/wiki/A_(b)" to "https://en.example/wiki/A_(b)"), Md.render("(https://en.example/wiki/A_(b))").links())
        assertEquals("https://x.example/q", Md.render("at https://x.example/q, then").links().single().second)
        assertTrue(Md.render("ftp://x.example/f mailto:a@b.example").links().isEmpty())
    }

    @Test fun textFormatIsShownAsSent() {
        val s = "**raw** *stars* _x_ see https://x.example/a?b=1\nand https://y.example."
        val a = Md.render(s, markdown = false)
        assertEquals(s, a.text)
        assertTrue(a.spanStyles.isEmpty())
        assertEquals(listOf("https://x.example/a?b=1" to "https://x.example/a?b=1", "https://y.example" to "https://y.example"), a.links())
        assertEquals(s, Md.tray(s, false).toString())
        assertFalse(Notif.fromJson(JSONObject("""{"id":"a","format":"text"}""")).isMarkdown)
        assertTrue(Notif.fromJson(JSONObject("""{"id":"a"}""")).isMarkdown) // events from before formats existed
    }

    @Test fun hostileInputFinishesFast() {
        val junk = "*_[~+`".repeat(800) + "\n" + "**".repeat(2000) + "\n" + "<u>".repeat(1000) + "\n" + "*a ".repeat(2000)
        val t = System.nanoTime()
        Md.render(junk)
        Md.render("**_~~++" .repeat(500) + "x" + "++~~_**".repeat(500))
        assertTrue("took ${(System.nanoTime() - t) / 1_000_000} ms", System.nanoTime() - t < 2_000_000_000L)
    }

    @Test fun trayKeepsOnlyBoldAndItalic() {
        val s = Md.tray("**Down** for *5 min*, ~~old~~ `code` [run](https://x.example)", true) as Spanned
        assertEquals("Down for 5 min, old code run", s.toString())
        val spans = s.getSpans(0, s.length, Any::class.java)
        assertTrue(spans.all { it is StyleSpan })
        val got = spans.map { it as StyleSpan }.associate { s.subSequence(s.getSpanStart(it), s.getSpanEnd(it)).toString() to it.style }
        assertEquals(mapOf("Down" to Typeface.BOLD, "5 min" to Typeface.ITALIC), got)
    }
}
