package me.itsjustin.clrd

import androidx.test.core.app.ApplicationProvider
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** "Remove cleared items" (#151): a cleared row leaves the phone at once, an open one never does. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class DropClearedTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val dao get() = app.db.dao()
    private fun row(id: String, state: String, at: Long) = Notif(id, "t", "T", "m", 3, "", null, "[]", state, null, null, at, at, 1)

    @After fun tearDown() { app.dropCleared = false }

    @Test fun offKeepsRecentHistoryOnKeepsNone() {
        val now = System.currentTimeMillis()
        listOf(row("a", Notif.DISMISSED, now), row("b", Notif.RESOLVED, now + 60_000), row("c", Notif.ACTIVE, now)).forEach(dao::put)
        app.pruneCleared(now)
        assertNotNull(dao.get("a")) // off by default: a fresh clear stays in the Cleared tab
        app.dropCleared = true
        app.pruneCleared(now)
        assertNull(dao.get("a"))
        assertNull(dao.get("b")) // even one stamped ahead of this phone's clock
        assertEquals(Notif.ACTIVE, dao.get("c")?.state)
    }
}
