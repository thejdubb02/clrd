package me.itsjustin.clrd

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** #271: only a swipe with no other row leaving the shade near it counts as a clear. */
class LoneSwipeTest {
    @Test fun aLoneSwipeCounts() {
        assertTrue(lone(listOf(5000L), 5000))
        assertTrue(lone(listOf(1000L, 5000L), 5000)) // an earlier swipe long before
    }

    @Test fun aBurstOrAStackNeverCounts() {
        assertFalse(lone(listOf(5000L, 5000L), 5000)) // Clear all on two rows
        assertFalse(lone(listOf(5000L, 5000L + App.LONE_MS), 5000)) // another left within the wait
        assertFalse(lone(listOf(5000L - App.LONE_MS, 5000L), 5000)) // or just before
    }
}
