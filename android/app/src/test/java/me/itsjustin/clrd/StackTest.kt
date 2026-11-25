package me.itsjustin.clrd

import android.Manifest
import android.app.Notification
import android.app.NotificationManager
import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.util.concurrent.TimeUnit

/** Tray stacks by topic (#108): one summary per topic with 2 or more rows, and swiping it only hides those rows on this phone (#265). */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class StackTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val nm = app.getSystemService(NotificationManager::class.java)
    private val dao get() = app.db.dao()

    @Before fun setUp() {
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        app.serverUrl = "https://a.example"
        app.token = "" // not configured, so nothing here touches the network
        app.stack = true
    }

    @After fun tearDown() { app.prefs.edit().remove("stack").commit() }

    /** #465: off until a person turns it on; a stored choice is kept. */
    @Test fun offByDefaultAndAChoiceIsKept() {
        app.prefs.edit().remove("stack").commit()
        assertEquals(false, app.stack)
        app.tray.reconcile(listOf(row("a", "disk"), row("b", "disk")), emptySet())
        assertNull("no stack until turned on", summary("disk"))
        assertEquals("silent", shadowOf(nm).getNotification("a", 0).group) // NotificationCompat's own, not ours
        app.stack = true
        assertEquals(true, app.stack)
    }

    private fun row(id: String, topic: String) =
        Notif(id, topic, "T", "m", 3, "", null, "[]", Notif.ACTIVE, null, null, 1, System.currentTimeMillis(), 1)
    private fun summary(topic: String): Notification? = shadowOf(nm).getNotification(Tray.GROUP + topic, Tray.SUMMARY_ID)

    @Test fun twoRowsOfOneTopicGetOneSummary() {
        app.tray.reconcile(listOf(row("a", "disk"), row("b", "disk"), row("c", "cpu")), emptySet())
        val s = summary("disk")
        assertNotNull(s)
        s!!
        assertEquals(Notification.FLAG_GROUP_SUMMARY, s.flags and Notification.FLAG_GROUP_SUMMARY)
        assertEquals(Tray.GROUP + "disk", shadowOf(nm).getNotification("a", 0).group)
        assertNull("one row needs no stack", summary("cpu"))
    }

    @Test fun theSummaryGoesOnlyWithItsLastRow() {
        app.tray.reconcile(listOf(row("a", "disk"), row("b", "disk")), emptySet())
        app.tray.apply(TrayStep.Cancel("b"))
        // On a phone, cancelling a summary also cancels the rows under it, so it stays while "a" does.
        assertNotNull(summary("disk"))
        assertNotNull(shadowOf(nm).getNotification("a", 0))
        app.tray.apply(TrayStep.Cancel("a"))
        assertNull(summary("disk"))
    }

    @Test fun stackingOffPostsNoGroups() {
        app.stack = false
        app.tray.reconcile(listOf(row("a", "disk"), row("b", "disk")), emptySet())
        // NotificationCompat puts a silent post in its own "silent" group; only ours must be absent.
        assertEquals("silent", shadowOf(nm).getNotification("a", 0).group)
        assertNull(summary("disk"))
    }

    @Test fun turningFormattingOffRedrawsTheTray() { // #809: rows already showing take the new look at once
        val n = row("a", "disk").copy(message = "**bold** text")
        bg { dao.applyAll(listOf(n), 1) }
        app.tray.reconcile(listOf(n), emptySet())
        fun text() = shadowOf(nm).getNotification("a", 0).extras.getCharSequence(Notification.EXTRA_TEXT).toString()
        assertEquals("bold text", text())
        try {
            bg { RoomStore(app).settings(org.json.JSONObject("""{"render_formatting":false}""")) }
            assertEquals("**bold** text", text())
        } finally {
            app.prefs.edit().remove("render_formatting").commit()
            app.formatting.value = true
        }
    }

    @Test fun swipingTheStackOnlyHidesItsRowsHere() { // #265: a bundle swipe must not clear everywhere
        bg { dao.applyAll(listOf(row("a", "disk"), row("b", "disk")), 1) }
        app.tray.reconcile(listOf(row("a", "disk"), row("b", "disk")), emptySet())
        val ids = shadowOf(summary("disk")!!.deleteIntent).savedIntent.getStringArrayExtra(Tray.EXTRA_IDS)!!.toList()
        assertEquals(setOf("a", "b"), ids.toSet())
        val intent = Intent(app, ActionReceiver::class.java).setAction(Tray.ACTION_DISMISS_STACK)
            .putExtra(Tray.EXTRA_IDS, ids.toTypedArray()).putExtra(Tray.EXTRA_SERVER, "https://a.example")
        app.sendBroadcast(intent)
        app.sendBroadcast(Intent(app, ActionReceiver::class.java).setAction(Tray.ACTION_DISMISS).putExtra(Tray.EXTRA_ID, "a")) // the row's own swipe too
        nm.cancelAll() // what the shade did
        shadowOf(android.os.Looper.getMainLooper()).idle()
        app.acts.submit {}.get(10, TimeUnit.SECONDS)
        assertEquals(emptyList<Pending>(), bg { dao.pending() })
        assertEquals(setOf("a", "b"), app.trayHidden)
        app.tray.reconcile(listOf(row("a", "disk"), row("b", "disk")), emptySet())
        assertNull(shadowOf(nm).getNotification("a", 0))
        assertNull(summary("disk"))
    }

    @Test fun clearAllOnAListStackClearsEveryRowEverywhere() { // in the app list, unlike a shade swipe
        bg { dao.applyAll(listOf(row("a", "disk"), row("b", "disk"), row("c", "cpu")), 1) }
        app.act(listOf("a", "b"), "dismiss")
        app.acts.submit {}.get(10, TimeUnit.SECONDS)
        assertEquals(listOf("a" to "dismiss", "b" to "dismiss"), bg { dao.pending() }.map { it.notifId to it.action })
        assertEquals(listOf(Notif.DISMISSED, Notif.DISMISSED, Notif.ACTIVE), bg { listOf("a", "b", "c").map { dao.get(it)!!.state } })
    }
}
