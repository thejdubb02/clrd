package me.itsjustin.clrd

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material3.Text
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipe
import androidx.compose.ui.test.swipeLeft
import androidx.compose.ui.test.swipeRight
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** A swipe on a card only shows its buttons; nothing is cleared until one is tapped (#161). */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class RevealRowTest {
    @get:Rule val rule = createComposeRule()
    private var cleared = 0
    private var scrolled: androidx.compose.foundation.ScrollState? = null

    private fun show(fast: Boolean = false, scroll: Boolean = false, rtl: Boolean = false) = rule.setContent {
        val s = rememberScrollState().also { scrolled = it }
        val col = if (scroll) Modifier.verticalScroll(s) else Modifier
        CompositionLocalProvider(LocalLayoutDirection provides if (rtl) LayoutDirection.Rtl else LayoutDirection.Ltr) { Box(Modifier.width(360.dp)) {
            Column(Modifier.height(400.dp).then(col)) {
                RevealRow(listOf(RevealAction("Clear", Icons.Filled.Check, Color.Green, "reveal_clear") { cleared++ }), emptyList(), fast) {
                    Text("card", Modifier.fillMaxWidth().height(96.dp).testTag("card"))
                }
                if (scroll) Box(Modifier.height(2000.dp)) // room to scroll
            }
        } }
    }

    @Test fun rightToLeftMirrorsTheSwipe() {
        show(rtl = true)
        rule.onNodeWithTag("card").performTouchInput { swipeRight(left + 1, right - 1) } // toward the start in Arabic or Hebrew
        rule.waitForIdle()
        rule.onNodeWithTag("reveal_clear").assertExists()
    }

    private fun swipeAcross() = rule.onNodeWithTag("card").performTouchInput { swipeLeft(right - 1, left + 1) }

    @Test fun shortDragClearsNothingAndCloses() {
        show()
        rule.onNodeWithTag("card").performTouchInput { swipeLeft(centerX, centerX - 30f) }
        rule.waitForIdle()
        assertEquals(0, cleared)
        rule.onNodeWithTag("reveal_clear").assertDoesNotExist()
    }

    @Test fun fullSwipeOnlyShowsTheButtonsByDefault() {
        show()
        swipeAcross()
        rule.waitForIdle()
        assertEquals(0, cleared)
        rule.onNodeWithTag("reveal_clear").assertExists()
    }

    @Test fun tapOnRevealedClearClears() {
        show()
        swipeAcross()
        rule.onNodeWithTag("reveal_clear").performClick()
        rule.waitForIdle()
        assertEquals(1, cleared)
    }

    @Test fun fullSwipeClearsWhenFastPathIsOn() {
        show(fast = true)
        swipeAcross()
        rule.waitForIdle()
        assertEquals(1, cleared)
    }

    @Test fun mostlyVerticalDragScrollsAndOpensNothing() {
        show(scroll = true)
        rule.onNodeWithTag("card").performTouchInput { swipe(center, Offset(centerX - 40f, centerY - 250f)) }
        rule.waitForIdle()
        assertEquals(0, cleared)
        rule.onNodeWithTag("reveal_clear").assertDoesNotExist()
        assertTrue("the list scrolled", scrolled!!.value > 0) // not swallowed by the row
    }
}
