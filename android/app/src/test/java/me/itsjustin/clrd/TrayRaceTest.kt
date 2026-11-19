package me.itsjustin.clrd

import android.Manifest
import android.app.Notification
import android.app.NotificationManager
import android.os.Looper
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** Room and the tray together: what the user sees must never go backwards, and taps reach the right server. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class TrayRaceTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val nm = app.getSystemService(NotificationManager::class.java)
    private val dao get() = app.db.dao()

    @Before fun setUp() {
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        app.serverUrl = "https://a.example"
        app.token = "" // not configured, so nothing here touches the network
    }

    /** Room refuses the main thread, which is the Robolectric test thread. */
    private fun <T> bg(f: () -> T): T {
        var r: Result<T>? = null
        Thread { r = runCatching(f) }.apply { start(); join(60_000) } // the first Room open in a JVM is slow on CI
        return checkNotNull(r) { "background step still running after 60 s" }.getOrThrow()
    }

    private fun drainIo() = app.io.submit {}.get(10, TimeUnit.SECONDS)
    private fun tray(id: String): Notification? = shadowOf(nm).getNotification(id, 0)
    private fun row(id: String, state: String, seq: Long) =
        Notif(id, "t", "T", "m", 3, "", null, "[]", state, null, null, seq, System.currentTimeMillis(), seq) // recent, or prune drops it
    private fun buttons(n: Notification) = n.actions.orEmpty().map { it.title.toString() }

    @Test fun anOlderEventNeverChangesTheTray() {
        val store = RoomStore(app)
        bg { store.apply(listOf(row("a", Notif.ACKNOWLEDGED, 10)), 10) }
        assertEquals(listOf("Snooze 1h", "Clear"), buttons(tray("a")!!))
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 5)), 11) } // replayed late, Room drops it
        assertEquals(Notif.ACKNOWLEDGED, bg { dao.openNow().single().state })
        assertEquals(listOf("Snooze 1h", "Clear"), buttons(tray("a")!!)) // still no Ack button: the tray did not go back
    }

    @Test fun aQuietSaveDoesNotBuzz() {
        val store = RoomStore(app)
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 1)), 1, quiet = true) } // part of a long replay
        bg { store.apply(listOf(row("b", Notif.ACTIVE, 2)), 2) }
        assertEquals(Notification.GROUP_ALERT_SUMMARY, tray("a")!!.groupAlertBehavior) // how a silent notification is built
        assertEquals(Notification.GROUP_ALERT_ALL, tray("b")!!.groupAlertBehavior)
    }

    @Test fun reconcileWaitsForSyncWrites() {
        bg { dao.applyAll(listOf(row("a", Notif.ACTIVE, 1), row("b", Notif.ACTIVE, 1)), 1) }
        val store = RoomStore(app)
        val done = CountDownLatch(1)
        bg {
            synchronized(app.configLock) {
                Thread { store.reconcile(); done.countDown() }.start()
                assertTrue(!done.await(300, TimeUnit.MILLISECONDS)) // blocked, so it cannot read rows that are about to change
                assertTrue(shadowOf(nm).allNotifications.isEmpty())
                store.apply(listOf(row("a", Notif.DISMISSED, 2)), 2) // dismissed on another device meanwhile
            }
            assertTrue(done.await(10, TimeUnit.SECONDS))
        }
        assertNull(tray("a"))
        assertTrue(tray("b") != null)
    }

    @Test fun userActionWaitsForSyncWrites() {
        bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        bg {
            synchronized(app.configLock) {
                app.act("a", "dismiss")
                Thread.sleep(300)
                assertTrue(dao.pending().isEmpty()) // waits, so a sync write in progress cannot repost after this cancel
            }
        }
        drainIo()
        assertNull(tray("a"))
        assertEquals(1, bg { dao.pending().size })
    }

    @Test fun reconcileKeepsAQueuedDismissHidden() {
        bg {
            dao.applyAll(listOf(row("a", Notif.ACTIVE, 1), row("b", Notif.ACTIVE, 1)), 1)
            dao.addPending(Pending(notifId = "a", action = "dismiss", body = "{}")) // not sent yet
            RoomStore(app).reconcile() // for example after a topic is unmuted
        }
        assertNull(tray("a"))
        assertTrue(tray("b") != null)
    }

    @Test fun resetListNeverUndoesANewerRow() {
        val store = RoomStore(app)
        bg { store.apply(listOf(row("a", Notif.DISMISSED, 5)), 4) } // a send's answer, saved while the list was on its way
        bg { store.replaceAll(listOf(row("a", Notif.ACTIVE, 3), row("b", Notif.ACTIVE, 3)), 4) }
        assertNull(tray("a"))
        assertTrue(tray("b") != null)
        assertEquals(listOf("b"), bg { dao.openNow().map { it.id } })
    }

    @Test fun resetAfterAServerRestoreKeepsNothingOld() {
        val store = RoomStore(app)
        bg { store.apply(listOf(row("a", Notif.DISMISSED, 100)), 100) }
        bg { store.replaceAll(listOf(row("a", Notif.ACTIVE, 3)), 4) } // the server's seq went back to 4
        assertEquals(Notif.ACTIVE, bg { dao.openNow().single().state })
        assertTrue(tray("a") != null)
    }

    @Test fun resetUpdatesAShowingItem() {
        val store = RoomStore(app)
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        assertEquals(listOf("Ack", "Snooze 1h", "Clear"), buttons(tray("a")!!))
        bg { store.replaceAll(listOf(row("a", Notif.ACKNOWLEDGED, 5)), 5) } // the ack event itself was lost
        assertEquals(listOf("Snooze 1h", "Clear"), buttons(tray("a")!!))
    }

    @Test fun aSentActionRevealsARowReopenedMeanwhile() {
        val store = RoomStore(app)
        bg {
            store.apply(listOf(row("a", Notif.ACTIVE, 1)), 1)
            dao.addPending(Pending(notifId = "a", action = "dismiss", body = "{}"))
            store.apply(listOf(row("a", Notif.ACTIVE, 6)), 6) // reopened on another device after our dismiss
        }
        assertNull(tray("a")) // still hidden while our dismiss is queued
        bg { store.removePending(dao.pending().single()) }
        assertTrue(tray("a") != null)
    }

    private fun swipe(id: String) {
        tray(id)!!.deleteIntent.send()
        shadowOf(Looper.getMainLooper()).idle()
        drainIo()
    }

    @Test fun swipeAfterServerSwitchIsDropped() {
        bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        app.serverUrl = "https://b.example" // the tray item still belongs to server a
        swipe("a")
        assertTrue(bg { dao.pending().isEmpty() })
        assertEquals(Notif.ACTIVE, bg { dao.openNow().single().state })
    }

    @Test fun swipeOnTheSameServerIsSaved() {
        bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        swipe("a")
        assertEquals(listOf("dismiss"), bg { dao.pending().map { it.action } })
    }
}
