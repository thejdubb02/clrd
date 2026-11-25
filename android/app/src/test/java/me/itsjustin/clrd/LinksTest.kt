package me.itsjustin.clrd

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.requiredWidth
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.click
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.swipeLeft
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/** Linked text on a card (#107): the words only, the site's icon beside them, tap opens, long press shows the address. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
@GraphicsMode(GraphicsMode.Mode.NATIVE) // real text positions
class LinksTest {
    @get:Rule val rule = createComposeRule()

    private val url = "https://example.com/a/very/long/address"
    private val msg = "See [Ticket 42]($url) now"
    private val opened = mutableListOf<String>()
    private val n = Notif("a", "t", "Build failed", msg, 3, "", null, "[]", Notif.ACTIVE, null, null, 1, 1, 1, format = "markdown")

    private fun card() = rule.setContent {
        CompositionLocalProvider(LocalUriHandler provides object : UriHandler { override fun openUri(uri: String) { opened += uri } }) {
            Box(Modifier.requiredWidth(380.dp)) { Card(n) }
        }
    }

    /** Where [word] is drawn in the message, in the message's own coordinates. */
    private fun at(word: String): Offset {
        val out = mutableListOf<TextLayoutResult>()
        rule.onNodeWithText("Ticket", substring = true, useUnmergedTree = true).fetchSemanticsNode().config[SemanticsActions.GetTextLayoutResult].action!!(out)
        val l = out.single()
        val i = l.layoutInput.text.text.indexOf(word)
        return l.getBoundingBox(i).center
    }

    @Test fun showsOnlyTheWordsWithAnIconSlot() {
        assertEquals("See ​Ticket 42 now", Md.render(msg, icons = true).text)
        assertEquals("See Ticket 42 now", Md.render(msg).text) // the tray and icons off: no slot
        assertEquals("example.com", Md.host(url))
    }

    @Test fun tapOpensTheLink() {
        card()
        rule.onNodeWithText("Ticket", substring = true, useUnmergedTree = true).performTouchInput { click(at("Ticket")) }
        rule.waitForIdle()
        assertEquals(listOf(url), opened)
        rule.onAllNodesWithText(url).assertCountEquals(0)
    }

    @Test fun longPressShowsTheAddressAndOpensNothing() {
        card()
        rule.onNodeWithText("Ticket", substring = true, useUnmergedTree = true).performTouchInput { longClick(at("Ticket")) }
        rule.waitForIdle()
        rule.onAllNodesWithText(url).assertCountEquals(1)
        assertEquals(emptyList<String>(), opened)
        rule.onNodeWithText("Open").performClick()
        assertEquals(listOf(url), opened)
    }

    @Test fun aCancelledPressOpensNothing() { // a call comes in while a finger rests on the link
        card()
        rule.onNodeWithText("Ticket", substring = true, useUnmergedTree = true).performTouchInput { down(at("Ticket")); cancel() }
        rule.waitForIdle()
        assertEquals(emptyList<String>(), opened)
    }

    @Test fun longPressBesideALinkShowsNothing() {
        card()
        rule.onNodeWithText("Ticket", substring = true, useUnmergedTree = true).performTouchInput { longClick(at("See")) }
        rule.waitForIdle()
        rule.onAllNodesWithText(url).assertCountEquals(0)
        assertEquals(emptyList<String>(), opened)
    }

    @Test fun aSwipeThatStartsOnALinkStillSwipes() {
        val wide = n.copy(message = "Words first, then the link: [Ticket 42]($url)")
        rule.setContent {
            CompositionLocalProvider(LocalUriHandler provides object : UriHandler { override fun openUri(uri: String) { opened += uri } }) {
                Box(Modifier.requiredWidth(380.dp)) {
                    RevealRow(listOf(RevealAction("Clear", Icons.Filled.Check, Color.Green, "reveal_clear") {}), emptyList(), false) { Card(wide) }
                }
            }
        }
        val from = at("Ticket")
        rule.onNodeWithText("Ticket", substring = true, useUnmergedTree = true).performTouchInput { swipeLeft(from.x, 1f) }
        rule.waitForIdle()
        rule.onNodeWithTag("reveal_clear").assertExists()
        assertEquals(emptyList<String>(), opened)
        rule.onAllNodesWithText(url).assertCountEquals(0)
    }

    @Test fun longPressWorksOnAnOpenCardToo() {
        card()
        rule.onNodeWithText("Build failed").performClick() // opens the card: its text is now selectable
        rule.onNodeWithText("Ticket", substring = true, useUnmergedTree = true).performTouchInput { longClick(at("Ticket")) }
        rule.waitForIdle()
        rule.onAllNodesWithText(url).assertCountEquals(1)
        assertEquals(emptyList<String>(), opened)
    }
}
