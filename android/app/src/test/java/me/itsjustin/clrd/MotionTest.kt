package me.itsjustin.clrd

import android.os.SystemClock
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** Card motion (#598, SPEC 10.1): only a live change seen while the app is in front moves a card. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class MotionTest {
    @get:Rule val rule = createComposeRule()

    private fun n(id: String, topic: String = "t") = Notif(id, topic, "T $id", "m", 3, "", null, "[]", Notif.ACTIVE, null, null, 1, 0, 1)
    private fun ones(vararg ids: String) = ids.map { One(n(it)) }
    private fun mark(at: Long = 1000, arrive: Boolean = false, state: String = Notif.DISMISSED) = Moved(at, arrive, state, null)
    private val right = { _: String -> 1 }

    @Test fun aMarkedClearLeavesAtItsOldPlace() {
        val m = motion(ones("a", "b", "c"), ones("a", "c"), 1500, { if (it == "b") mark() else null }, right)
        assertEquals(listOf("b"), m.gone.map { it.entry.key })
        assertEquals(1, m.gone[0].index)
        assertEquals(0, m.gone[0].wait)
    }

    @Test fun anUnmarkedOrStaleChangeMovesNothing() { // a first load, a catch-up or a change made while away
        assertTrue(motion(ones("a", "b"), ones("a"), 1500, { null }, right).gone.isEmpty())
        assertTrue(motion(ones("a", "b"), ones("a"), 1000 + FRESH_MS + 1, { mark() }, right).gone.isEmpty())
        assertTrue(motion(ones("a"), ones("b", "a"), 1500, { mark(arrive = false) }, right).arrive.isEmpty()) // a clear elsewhere, not a new one
    }

    @Test fun clearAllStaggers40msUpTo8Steps() {
        val all = ones(*Array(10) { "n$it" })
        val m = motion(all, emptyList(), 1500, { mark() }, right)
        assertEquals(listOf(0, 40, 80, 120, 160, 200, 240, 280, 320, 320), m.gone.map { it.wait })
    }

    @Test fun aStackLeavesOnlyWhenAllItsRowsDo() {
        val stack = Stack("t", listOf(n("a"), n("b"), n("c")))
        val smaller = Stack("t", listOf(n("a"), n("b")))
        assertTrue(motion(listOf(stack), listOf(smaller), 1500, { mark() }, right).gone.isEmpty())
        assertEquals(1, motion(listOf(stack), emptyList(), 1500, { mark() }, right).gone.size)
        // a stack that unfolds is the same rows, so nothing leaves
        assertTrue(motion(listOf(stack), ones("a", "b", "c"), 1500, { mark() }, right).gone.isEmpty())
    }

    @Test fun onlyNewRowsArriveNewestFirst() {
        val m = motion(ones("a"), ones("c", "b", "a"), 1500, { mark(arrive = true) }, right)
        assertEquals(listOf("c", "b"), m.arrive)
        // a repeat of a card already shown does not arrive again
        assertTrue(motion(ones("a", "b"), ones("b", "a"), 1500, { mark(arrive = true) }, right).arrive.isEmpty())
    }

    @Test fun theFirstListNeverMovesAndUndoBringsTheCardBack() {
        val s = MotionState()
        s.step(ones("a", "b"), 1500, { mark(arrive = true) }, right)
        assertTrue(s.ghosts.isEmpty() && s.arrived.isEmpty())
        s.step(ones("a"), 1500, { mark() }, right)
        assertEquals(listOf("a", "b"), s.shown(ones("a")).map { if (it is Ghost) it.entry.key else (it as Entry).key })
        s.step(ones("a", "b"), 1600, { mark() }, right) // Undo during the hold
        assertTrue(s.ghosts.isEmpty())
    }

    @Test fun anArrivalMarkDoesNotMoveACardOut() { // a replayed clear right after a live arrival
        assertTrue(motion(ones("a", "b"), ones("a"), 1500, { mark(arrive = true, state = Notif.ACTIVE) }, right).gone.isEmpty())
    }

    @Test fun twoLeavingAtOnceKeepTheirOrderAndAnUnseenOneIsDropped() {
        val s = MotionState()
        s.step(ones("a", "b", "c"), 1000, { null }, right)
        s.step(ones("b", "c"), 1100, { mark() }, right)
        s.step(ones("c"), 1200, { mark() }, right)
        assertEquals(listOf("a", "b", "c"), s.shown(ones("c")).map { if (it is Ghost) it.entry.key else (it as Entry).key })
        s.step(ones("c"), 1200 + HOLD_MS + SLIDE_MS + GAP_MS + 1001, { null }, right) // never drawn, so never done
        assertTrue(s.ghosts.isEmpty())
    }

    @Test fun nothingIsMarkedWhileTheAppIsAway() {
        val app = ApplicationProvider.getApplicationContext<App>()
        app.front = false
        app.moving(listOf("x" to mark()))
        assertFalse(app.moved.containsKey("x"))
        app.front = true
        app.moving(listOf("x" to mark()))
        assertTrue(app.moved.containsKey("x"))
        app.front = false
        app.moved.clear()
    }

    @Test fun aLeavingCardSaysWhyThenGoes() {
        var done = 0L
        rule.mainClock.autoAdvance = false
        rule.setContent { Gone(Ghost(One(n("a")), 0, 0, mark(), 1, SystemClock.uptimeMillis()), false, { done = rule.mainClock.currentTime }) { why -> why() } }
        val start = rule.mainClock.currentTime
        rule.mainClock.advanceTimeBy(1000)
        rule.onNodeWithText("Cleared").assertExists()
        assertEquals(0L, done)
        rule.mainClock.advanceTimeBy(1000)
        assertTrue("done at ${done - start}", done - start in HOLD_MS + SLIDE_MS + GAP_MS..HOLD_MS + SLIDE_MS + GAP_MS + 100)
    }

    @Test fun withRemoveAnimationsItStillHoldsButDoesNotSlide() {
        var done = 0L
        rule.mainClock.autoAdvance = false
        rule.setContent { Gone(Ghost(One(n("a")), 0, 0, Moved(0, false, Notif.RESOLVED, "Ops"), 1, SystemClock.uptimeMillis()), true, { done = rule.mainClock.currentTime }) { why -> why() } }
        val start = rule.mainClock.currentTime
        rule.mainClock.advanceTimeBy(500)
        rule.onNodeWithText("Resolved by Ops").assertExists()
        rule.mainClock.advanceTimeBy(1500)
        assertTrue("done at ${done - start}", done - start in HOLD_MS..HOLD_MS + 50)
    }
}
