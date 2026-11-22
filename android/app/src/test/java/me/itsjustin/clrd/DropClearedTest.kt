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
import java.util.concurrent.TimeUnit

/** "Remove cleared items" (#151): a cleared row leaves the phone at once, an open one never does. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class DropClearedTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val dao get() = app.db.dao()
    private fun row(id: String, state: String, at: Long, seq: Long = 1) = Notif(id, "t", "T", "m", 3, "", null, "[]", state, null, null, at, at, seq)

    @After fun tearDown() { app.dropCleared = false }

    private fun onIo(f: () -> Unit) { app.io.submit { f() }.get(10, TimeUnit.SECONDS) } // Room refuses the main thread

    @Test fun offKeepsRecentHistoryOnKeepsNone() = onIo {
        val now = System.currentTimeMillis()
        dao.putMeta(Meta("seq", 3))
        listOf(row("a", Notif.DISMISSED, now), row("b", Notif.RESOLVED, now + 60_000), row("c", Notif.ACTIVE, now)).forEach(dao::put)
        app.pruneCleared(now)
        assertNotNull(dao.get("a")) // off by default: a fresh clear stays in the Cleared tab
        app.dropCleared = true
        app.pruneCleared(now)
        assertNull(dao.get("a"))
        assertNull(dao.get("b")) // even one stamped ahead of this phone's clock
        assertEquals(Notif.ACTIVE, dao.get("c")?.state)
    }

    @Test fun keptUntilSafeToForget() = onIo {
        val now = System.currentTimeMillis()
        app.dropCleared = true
        dao.putMeta(Meta("seq", 3))
        dao.put(row("ahead", Notif.DISMISSED, now, seq = 5)) // a clear the stream has not caught up to: its seq still guards it
        app.pruneCleared(now)
        assertNotNull(dao.get("ahead"))
        dao.put(row("queued", Notif.DISMISSED, now))
        dao.addPending(Pending(notifId = "queued", action = "dismiss")) // offline: an Undo must still find it
        app.pruneCleared(now)
        assertNotNull(dao.get("queued"))
        dao.clearPending()
        dao.putMeta(Meta("seq", 5))
        app.pruneCleared(now)
        assertNull(dao.get("queued"))
        assertNull(dao.get("ahead"))
    }
}
