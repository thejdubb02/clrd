package me.itsjustin.clrd

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.requiredWidth
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/** A card's Clear never wraps: the buttons before it move to the More menu when the row is narrow (#734). */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
@GraphicsMode(GraphicsMode.Mode.NATIVE) // real text widths
class CardRowTest {
    @get:Rule val rule = createComposeRule()

    private val n = Notif("a", "t", "Build failed", "m", 3, "", null, "[]", Notif.ACTIVE, null, null, 1, 1, 1)

    private fun card(width: Dp, fontScale: Float) = rule.setContent {
        CompositionLocalProvider(LocalDensity provides Density(2.625f, fontScale)) { // 420 dpi
            Box(Modifier.requiredWidth(width)) { // required: the test screen is narrower than some rows here
                Card(n, actions = listOf(CardAction("Ack", {}), CardAction("Snooze 1 hour", {}), CardAction("Clear", {}, strong = true)))
            }
        }
    }

    private fun whole(label: String): Boolean { // one line, nothing cut off
        val out = mutableListOf<TextLayoutResult>()
        rule.onNodeWithText(label).fetchSemanticsNode().config[SemanticsActions.GetTextLayoutResult].action!!(out)
        return out.single().let { it.lineCount == 1 && it.size.width >= it.multiParagraph.maxIntrinsicWidth.toInt() }
    }

    @Test fun clearIsWholeOnA1080PxPhoneAtLargeText() {
        card(379.dp, 1.3f) // 1080 px at 420 dpi, less the list's 16 dp sides
        assertTrue(whole("Clear"))
        rule.onAllNodesWithText("Ack").assertCountEquals(0) // in the More menu, which is closed
    }

    @Test fun clearIsWholeOnA360DpPhoneAtLargeText() {
        card(328.dp, 1.3f)
        assertTrue(whole("Clear"))
    }

    @Test fun aWideRowShowsEveryButton() {
        card(600.dp, 1f)
        listOf("Ack", "Snooze 1 hour", "Clear").forEach { assertTrue(whole(it)) }
    }

    @Test fun theLastButtonsThatFitStay() {
        assertEquals(3, fitsFromEnd(300, listOf(100, 100, 100)))
        assertEquals(2, fitsFromEnd(299, listOf(100, 100, 100)))
        assertEquals(1, fitsFromEnd(10, listOf(100, 100, 100))) // Clear stays even with no room
        assertEquals(0, fitsFromEnd(10, emptyList()))
    }
}
