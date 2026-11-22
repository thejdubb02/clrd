package me.itsjustin.clrd

import android.Manifest
import android.app.Notification
import android.app.NotificationManager
import android.content.Intent
import android.os.Looper
import androidx.room.Room
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.mockwebserver.SocketPolicy
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.json.JSONObject
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.io.File
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

    private fun drainIo() {
        app.io.submit {}.get(10, TimeUnit.SECONDS)
        app.acts.submit {}.get(10, TimeUnit.SECONDS)
    }
    private fun tray(id: String): Notification? = shadowOf(nm).getNotification(id, 0)
    private fun row(id: String, state: String, seq: Long) =
        Notif(id, "t", "T", "m", 3, "", null, "[]", state, null, null, seq, System.currentTimeMillis(), seq) // recent, or prune drops it
    private fun buttons(n: Notification) = n.actions.orEmpty().map { it.title.toString() }

    @Test fun undoAfterClearKeepsASnooze() { // #425: back as it was, not active
        val until = System.currentTimeMillis() + 3_600_000
        val snoozed = row("s", Notif.SNOOZED, 1).copy(snoozeUntil = until)
        val open = row("o", Notif.ACTIVE, 2)
        bg { dao.applyAll(listOf(snoozed, open), 2) }
        app.act(listOf("s", "o"), "dismiss", App.VIA_APP_BULK)
        app.undoClear(listOf(snoozed, open))
        drainIo()
        val q = bg { dao.pending() }
        assertEquals(listOf("s" to "dismiss", "o" to "dismiss", "o" to "reopen", "s" to "reopen"), q.map { it.notifId to it.action }) // #508: no follow-up snooze
        assertEquals(java.time.Instant.ofEpochMilli(until).toString(), JSONObject(q[3].body).getString("until")) // #473: the reopen itself snoozes
        assertEquals(Notif.SNOOZED, bg { dao.get("s")?.state })
        assertEquals(until, bg { dao.get("s")?.snoozeUntil })
        assertEquals(Notif.ACTIVE, bg { dao.get("o")?.state })
    }

    @Test fun aSnoozeShowsItsTimeAtOnce() { // #473: the Snoozed list has the time before the server answers
        bg { dao.applyAll(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        val before = System.currentTimeMillis()
        app.act("a", "snooze", Snooze.body("1h30m"))
        drainIo()
        val got = bg { dao.get("a") }!!
        assertEquals(Notif.SNOOZED, got.state)
        assertTrue(got.snoozeUntil!! in before + 5_400_000..System.currentTimeMillis() + 5_400_000)
        val queued = bg { dao.pending() }.single().body // #508: a late send ends at the time shown, not later
        val late = got.snoozeUntil!! - 600_000 // sent 80 minutes after the tap
        val sent = JSONObject(snoozeWire(queued, late))
        assertEquals("600000ms", sent.getString("for"))
        assertTrue(!sent.has(ENDS)) // the server never sees the phone's clock
        assertEquals("1000ms", JSONObject(snoozeWire(queued, got.snoozeUntil!! + 1)).getString("for")) // ran out offline: still stops a repeat
    }

    @Test fun anyServerDurationShowsItsTime() { // #508: not only whole h, m and s
        bg { dao.applyAll(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        val before = System.currentTimeMillis()
        app.act("a", "snooze", Snooze.body("1.5h"))
        drainIo()
        assertTrue(bg { dao.get("a") }!!.snoozeUntil!! in before + 5_400_000..System.currentTimeMillis() + 5_400_000)
    }

    @Test fun anOlderEventNeverChangesTheTray() {
        val store = RoomStore(app)
        bg { store.apply(listOf(row("a", Notif.ACKNOWLEDGED, 10)), 10) }
        assertEquals(listOf("Snooze", "Clear"), buttons(tray("a")!!))
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 5)), 11) } // replayed late, Room drops it
        assertEquals(Notif.ACKNOWLEDGED, bg { dao.openNow().single().state })
        assertEquals(listOf("Snooze", "Clear"), buttons(tray("a")!!)) // still no Ack button: the tray did not go back
    }

    @Test fun aQuietSaveDoesNotBuzz() {
        val store = RoomStore(app)
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 1)), 1, quiet = true) } // part of a long replay
        bg { store.apply(listOf(row("b", Notif.ACTIVE, 2)), 2) }
        assertEquals(Notification.GROUP_ALERT_SUMMARY, tray("a")!!.groupAlertBehavior) // how a silent notification is built
        assertEquals(Notification.GROUP_ALERT_ALL, tray("b")!!.groupAlertBehavior)
    }

    /** #328: a silenced message shows without a sound, and still brings back one swiped away on this phone (it is a new alert). */
    @Test fun aSilencedMessageShowsQuietly() {
        val store = RoomStore(app)
        app.trayHidden = setOf("a")
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 1).copy(silenced = true), row("b", Notif.ACTIVE, 1)), 1) }
        assertEquals(Notification.GROUP_ALERT_SUMMARY, tray("a")!!.groupAlertBehavior)
        assertEquals(Notification.GROUP_ALERT_ALL, tray("b")!!.groupAlertBehavior)
    }

    @Test fun aRepeatRingsWhileShowing() {
        val store = RoomStore(app)
        val once = { id: String -> tray(id)!!.flags and Notification.FLAG_ONLY_ALERT_ONCE != 0 }
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 2)), 2) } // an edit stays quiet
        assertTrue(once("a"))
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 3)), 3, again = true) }
        assertTrue(!once("a"))
        bg { store.apply(listOf(row("b", Notif.ACKNOWLEDGED, 4)), 4, again = true) } // acked: never rings
        assertEquals(Notification.GROUP_ALERT_SUMMARY, tray("b")!!.groupAlertBehavior)
    }

    @Test fun aRepeatBehindOurOwnMarkStillRings() { // #500: the mark's answer (seq 5) lands before the repeat (seq 4)
        val store = RoomStore(app)
        val once = { id: String -> tray(id)!!.flags and Notification.FLAG_ONLY_ALERT_ONCE != 0 }
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 5).copy(createdAt = 1, marks = "[{\"mark\":\"seen\"}]")), 0, quiet = true) }
        assertTrue(once("a"))
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 4).copy(createdAt = 1)), 4, again = true) }
        assertTrue(!once("a"))
        assertEquals(5L, bg { dao.get("a")!!.seq }) // the newer row stays
        bg { store.apply(listOf(row("c", Notif.ACKNOWLEDGED, 7)), 7) }
        bg { store.apply(listOf(row("c", Notif.ACTIVE, 6)), 7, again = true) } // acked since: no ring, no going back
        assertEquals(Notification.GROUP_ALERT_SUMMARY, tray("c")!!.groupAlertBehavior)
    }

    @Test fun aLateRepeatAfterClearAndUndoStaysQuiet() { // #504: the Clear (5) and Undo (6) answers land before a repeat sent at 4
        val store = RoomStore(app)
        val once = { id: String -> tray(id)!!.flags and Notification.FLAG_ONLY_ALERT_ONCE != 0 }
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 1).copy(createdAt = 1, stateBy = "web")), 1) }
        bg { store.apply(listOf(row("a", Notif.DISMISSED, 5).copy(createdAt = 1, stateBy = "phone")), 0, quiet = true) }
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 6).copy(createdAt = 1, stateBy = "phone")), 0, quiet = true) }
        assertTrue(once("a"))
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 4).copy(createdAt = 1, stateBy = "web")), 4, again = true) }
        assertTrue(once("a")) // the user's own Undo stays quiet
        assertEquals(6L, bg { dao.get("a")!!.seq })
    }

    @Test fun aLateRepeatStaysQuietWhenThisPhoneMadeEveryChange() { // #550: this phone published it, cleared it and undid it
        val store = RoomStore(app)
        val once = { id: String -> tray(id)!!.flags and Notification.FLAG_ONLY_ALERT_ONCE != 0 }
        val due = """{"every":"5m","left":8,"count":2,"next":"2026-11-22T05:05:00Z"}"""
        val stopped = """{"every":"5m","left":8,"count":2,"next":null}"""
        val a = row("a", Notif.ACTIVE, 1).copy(createdAt = 1, stateBy = "phone")
        bg { store.apply(listOf(a.copy(repeat = due)), 1) }
        bg { store.apply(listOf(a.copy(state = Notif.DISMISSED, seq = 5, repeat = stopped)), 0, quiet = true) }
        bg { store.apply(listOf(a.copy(seq = 6, repeat = stopped)), 0, quiet = true) }
        assertTrue(once("a"))
        bg { store.apply(listOf(a.copy(seq = 4, repeat = due)), 4, again = true) }
        assertTrue(once("a"))
        assertEquals(6L, bg { dao.get("a")!!.seq })
    }

    @Test fun repeatIsReadFromTheServer() {
        val o = org.json.JSONObject("""{"id":"a","repeat":{"every":"5m","left":8,"count":2,"next":null}}""")
        assertEquals("""{"every":"5m","left":8,"count":2,"next":null}""", Notif.fromJson(o).repeat)
        assertEquals("", Notif.fromJson(org.json.JSONObject("""{"id":"a","repeat":null}""")).repeat)
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

    @Test fun resetOnAnotherEpochKeepsNothingOld() {
        val store = RoomStore(app)
        bg { store.saveEpoch("old"); store.apply(listOf(row("a", Notif.DISMISSED, 5)), 5) }
        bg { store.replaceAll(listOf(row("a", Notif.ACTIVE, 3)), 9, "new") } // seq is ahead, but it is another database
        assertEquals(Notif.ACTIVE, bg { dao.openNow().single().state })
        assertEquals("new", store.epoch())
    }

    @Test fun resetUpdatesAShowingItem() {
        val store = RoomStore(app)
        bg { store.apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        assertEquals(listOf("Ack", "Snooze", "Clear"), buttons(tray("a")!!))
        bg { store.replaceAll(listOf(row("a", Notif.ACKNOWLEDGED, 5)), 5) } // the ack event itself was lost
        assertEquals(listOf("Snooze", "Clear"), buttons(tray("a")!!))
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

    @Test fun aReopenFromTheInboxDoesNotBuzz() {
        val store = RoomStore(app)
        bg {
            store.apply(listOf(row("a", Notif.DISMISSED, 1)), 1)
            dao.addPending(Pending(notifId = "a", action = "reopen", body = "{}"))
            dao.setState("a", Notif.ACTIVE)
            store.removePending(dao.pending().single()) // sent
        }
        assertEquals(Notification.GROUP_ALERT_SUMMARY, tray("a")!!.groupAlertBehavior)
    }

    @Test fun theEventOfOurOwnReopenDoesNotBuzz() {
        val store = RoomStore(app)
        bg {
            store.apply(listOf(row("a", Notif.DISMISSED, 1)), 1)
            dao.addPending(Pending(notifId = "a", action = "reopen", body = "{}"))
            store.apply(listOf(row("a", Notif.ACTIVE, 2)), 2) // the live event, before the send's answer
        }
        assertEquals(Notification.GROUP_ALERT_SUMMARY, tray("a")!!.groupAlertBehavior)
    }

    @Test fun aTapOnATrayItemFromBeforeTheServerExtraIsSaved() {
        bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        app.sendBroadcast(Intent(app, ActionReceiver::class.java).setAction(Tray.ACTION_CLEAR).putExtra(Tray.EXTRA_ID, "a"))
        shadowOf(Looper.getMainLooper()).idle()
        drainIo()
        assertEquals(listOf("dismiss"), bg { dao.pending().map { it.action } })
    }

    @Test fun aBusyServerHoldsBackTheNextInlineSend() {
        val server = MockWebServer()
        server.enqueue(MockResponse().setResponseCode(429).setHeader("Retry-After", "60"))
        server.enqueue(MockResponse().setBody("{}"))
        server.start()
        try {
            app.serverUrl = server.url("/").toString().trimEnd('/')
            app.token = "t" // configured, but no sync service, so act sends by itself
            bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1), row("b", Notif.ACTIVE, 1)), 1) }
            app.act("a", "dismiss", server = app.serverUrl)
            assertTrue(server.takeRequest(10, TimeUnit.SECONDS) != null) // answered 429, wait 60 s
            app.act("b", "dismiss", server = app.serverUrl)
            assertNull(server.takeRequest(2, TimeUnit.SECONDS)) // the next tap waits too
            assertEquals(listOf("a", "b"), bg { dao.pending().map { it.notifId } })
        } finally {
            app.token = ""
            server.shutdown()
        }
    }

    /** #360: a stack's Clear in the app goes out as one bulk clear, logged as such. */
    @Test fun aStackClearIsOneBulkClear() {
        val server = MockWebServer()
        server.enqueue(MockResponse().setBody("{}"))
        server.start()
        try {
            app.serverUrl = server.url("/").toString().trimEnd('/')
            app.token = "t"
            bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1), row("b", Notif.ACTIVE, 1)), 1) }
            app.act(listOf("a", "b"), "dismiss", App.VIA_APP_BULK)
            val r = server.takeRequest(10, TimeUnit.SECONDS)!!
            assertEquals("/api/v1/notifications/clear", r.path)
            assertEquals("""{"ids":["a","b"],"via":"app_bulk"}""", r.body.readUtf8())
            assertTrue(bg { dao.openNow().none { it.state == Notif.ACTIVE } })
        } finally {
            app.token = ""
            server.shutdown()
        }
    }

    private fun clearTap(id: String) {
        tray(id)!!.actions.single { it.actionIntent.let { pi -> shadowOf(pi).savedIntent.action == Tray.ACTION_CLEAR } }.actionIntent.send()
        shadowOf(Looper.getMainLooper()).idle()
        drainIo()
    }

    @Test fun aClearTapAfterServerSwitchIsDropped() {
        bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        app.serverUrl = "https://b.example" // the tray item still belongs to server a
        clearTap("a")
        assertTrue(bg { dao.pending().isEmpty() })
        assertEquals(Notif.ACTIVE, bg { dao.openNow().single().state })
    }

    @Test fun aClearTapOnTheSameServerIsSaved() {
        bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        clearTap("a")
        val p = bg { dao.pending().single() }
        assertEquals("dismiss", p.action)
        assertEquals(App.VIA_TRAY_BUTTON, JSONObject(p.body).getString("via"))
    }

    @Test fun aTapIsSavedWhileIoIsBusy() {
        bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        val gate = CountDownLatch(1)
        app.io.execute { gate.await() } // a slow request on io, for example the Settings check
        try {
            val saved = CountDownLatch(1)
            app.act("a", "dismiss", server = app.serverUrl) { saved.countDown() }
            assertTrue(saved.await(5, TimeUnit.SECONDS)) // goAsync() finishes without waiting for io
            assertEquals(1, bg { dao.pending().size })
        } finally {
            gate.countDown()
        }
    }

    @Test fun aHungSendDoesNotHoldBackTheNextTap() {
        val server = MockWebServer()
        server.enqueue(MockResponse().setSocketPolicy(SocketPolicy.NO_RESPONSE)) // the first send hangs
        server.start()
        try {
            app.serverUrl = server.url("/").toString().trimEnd('/')
            app.token = "t" // configured, but no sync service, so act sends by itself
            bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1), row("b", Notif.ACTIVE, 1)), 1) }
            app.act("a", "dismiss", server = app.serverUrl)
            assertTrue(server.takeRequest(10, TimeUnit.SECONDS) != null) // the hung send is under way
            val saved = CountDownLatch(1)
            app.act("b", "dismiss", server = app.serverUrl) { saved.countDown() }
            assertTrue(saved.await(5, TimeUnit.SECONDS))
            assertEquals(listOf("a", "b"), bg { dao.pending().map { it.notifId } }) // both saved while the first send hangs
        } finally {
            app.token = ""
            server.shutdown()
        }
    }

    @Test fun anInboxTapQueuedAcrossASwitchIsDropped() {
        bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        val gate = CountDownLatch(1)
        app.acts.execute { gate.await() } // an earlier tap still being saved
        app.act("a", "dismiss") // from the inbox
        app.configGen++ // Settings switched server or token (same URL) before the save ran
        gate.countDown()
        drainIo()
        assertTrue(bg { dao.pending().isEmpty() })
    }

    @Test fun aSendStopsAtAServerSwitch() {
        val server = MockWebServer()
        val release = CountDownLatch(1)
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                release.await(10, TimeUnit.SECONDS)
                return MockResponse().setBody("{}")
            }
        }
        server.start()
        try {
            app.serverUrl = server.url("/").toString().trimEnd('/')
            app.token = "t"
            bg {
                RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1)
                dao.addPending(Pending(notifId = "b", action = "dismiss", body = "{}")) // queued earlier
            }
            app.act("a", "dismiss", server = app.serverUrl)
            assertTrue(server.takeRequest(10, TimeUnit.SECONDS) != null) // b is on its way
            app.configGen++ // Settings switched server while it was in flight
            release.countDown()
            assertNull(server.takeRequest(2, TimeUnit.SECONDS)) // a is not sent to the old server afterwards
        } finally {
            app.token = ""
            server.shutdown()
        }
    }

    private fun tap(id: String, label: String) {
        tray(id)!!.actions.single { it.title == label }.actionIntent.send()
        shadowOf(Looper.getMainLooper()).idle()
        drainIo()
    }

    private val yesNo = row("a", Notif.ACTIVE, 4).copy(actions = """[{"label":"Yes","reply":"yes"},{"label":"No","reply":"no"}]""")

    @Test fun aReplyTapIsQueuedWithTheVersionShown() {
        bg { RoomStore(app).apply(listOf(yesNo), 4) }
        assertEquals(listOf("Yes", "No", "Ack"), buttons(tray("a")!!))
        tap("a", "Yes") // the first of two: each reply button keeps its own value
        val p = bg { dao.pending().single() }
        assertEquals("reply", p.action)
        assertEquals("yes", JSONObject(p.body).getString("reply"))
        assertEquals(4L, JSONObject(p.body).getLong("seq")) // so the server refuses it if the question changed meanwhile
        assertEquals(App.VIA_TRAY_BUTTON, JSONObject(p.body).getString("via"))
        assertEquals(Notif.ACKNOWLEDGED, bg { dao.openNow().single().state })
    }

    @Test fun aSingleSwipeOnlyHidesItHere() { // #265: clearing happens in the app, never by a swipe
        bg { RoomStore(app).apply(listOf(row("a", Notif.ACTIVE, 1)), 1) }
        tray("a")!!.deleteIntent.send()
        nm.cancel("a", 0) // what the shade did
        shadowOf(Looper.getMainLooper()).idle()
        drainIo()
        assertEquals(emptyList<Pending>(), bg { dao.pending() })
        assertEquals(Notif.ACTIVE, bg { dao.openNow().single().state })
        app.tray.reconcile(bg { dao.openNow() }, emptySet())
        assertNull("a resync must not bring it back", tray("a"))
    }

    @Test fun oneGestureThatClearsManyOnlyHidesThemHere() { // #265: one bundle swipe fired 6 deletes across 3 topics
        val rows = (1..6).map { row("n$it", Notif.ACTIVE, 1).copy(topic = "t${it % 3}") }
        bg { RoomStore(app).apply(rows, 1) }
        rows.forEach { tray(it.id)!!.deleteIntent.send() }
        nm.cancelAll() // what the shade did
        shadowOf(Looper.getMainLooper()).idle()
        drainIo()
        assertEquals(emptyList<Pending>(), bg { dao.pending() }) // nothing dismissed on the server
        assertEquals(6, bg { dao.openNow() }.size) // still open in the inbox
        app.tray.reconcile(bg { dao.openNow() }, emptySet())
        assertTrue("a resync must not bring them back", rows.all { tray(it.id) == null })
        bg { RoomStore(app).apply(listOf(rows[0].copy(seq = 2, updatedAt = 2)), 2) } // a new alert for one
        assertNotNull(tray("n1"))
    }

    @Test fun anOlderPostKeepsItsOwnVersion() {
        bg { RoomStore(app).apply(listOf(yesNo), 4) }
        val old = tray("a")!!.actions.first().actionIntent
        bg { RoomStore(app).apply(listOf(yesNo.copy(message = "changed", seq = 5, updatedAt = 5)), 5) }
        old.send() // tapped just as the new version was posted
        shadowOf(Looper.getMainLooper()).idle()
        drainIo()
        assertEquals(4L, JSONObject(bg { dao.pending().single() }.body).getLong("seq"))
    }

    @Test fun aTapThatRacedADismissKeepsItCleared() {
        bg { RoomStore(app).apply(listOf(yesNo.copy(state = Notif.DISMISSED, seq = 5)), 5) } // dismissed on another device
        app.act("a", "reply", """{"reply":"yes","seq":4}""", "https://a.example")
        drainIo()
        assertTrue(bg { dao.openNow().isEmpty() })
        assertNull(tray("a"))
    }

    @Test fun reopenStillBringsBackACleared() {
        bg { RoomStore(app).apply(listOf(row("a", Notif.DISMISSED, 5)), 5) }
        app.act("a", "reopen")
        drainIo()
        assertEquals(Notif.ACTIVE, bg { dao.openNow().single().state })
    }

    @Test fun aReplyTapAfterServerSwitchIsDropped() {
        bg { RoomStore(app).apply(listOf(yesNo), 4) }
        app.serverUrl = "https://b.example"
        tap("a", "Yes")
        assertTrue(bg { dao.pending().isEmpty() })
    }

    /** An app update must keep the rows and the queue of a version 1 database. */
    @Test fun aVersion1DatabaseIsMigrated() {
        val v1 = JSONObject(File("schemas/me.itsjustin.clrd.Db/1.json").readText()).getJSONObject("database")
        val path = app.getDatabasePath("old.db").apply { parentFile!!.mkdirs() }
        android.database.sqlite.SQLiteDatabase.openOrCreateDatabase(path, null).use { db ->
            val tables = v1.getJSONArray("entities")
            for (i in 0 until tables.length()) {
                val t = tables.getJSONObject(i)
                db.execSQL(t.getString("createSql").replace("\${TABLE_NAME}", t.getString("tableName")))
            }
            val setup = v1.getJSONArray("setupQueries")
            for (i in 0 until setup.length()) db.execSQL(setup.getString(i))
            db.execSQL("INSERT INTO notif VALUES ('a','t','T','m',3,'',NULL,'[]','active',NULL,NULL,1,1,1)")
            db.execSQL("INSERT INTO pending (notifId, action, body) VALUES ('a','dismiss','{}')")
            db.version = 1
        }
        val db = Room.databaseBuilder(app, Db::class.java, "old.db").build()
        try {
            assertEquals("", bg { db.dao().openNow().single().reply })
            assertEquals(false, bg { db.dao().openNow().single().silenced })
            assertEquals(listOf("dismiss"), bg { db.dao().pending().map { it.action } })
        } finally {
            db.close()
        }
    }

    /** A tray picture loads after the post. When it arrives the tray is redrawn from Room, so a row cleared meanwhile stays gone. */
    @Test fun aLatePictureNeverBringsBackAClearedRow() {
        MockWebServer().use { s ->
            val gate = CountDownLatch(1)
            val png = java.util.Base64.getDecoder().decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
            s.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    gate.await(10, TimeUnit.SECONDS)
                    return MockResponse().setBody(okio.Buffer().write(png))
                }
            }
            val icon = s.url("/i.png").toString()
            val store = RoomStore(app)
            bg { store.apply(listOf(row("a", Notif.ACTIVE, 1).copy(icon = icon), row("b", Notif.ACTIVE, 1).copy(icon = icon)), 1) }
            assertNull(tray("a")!!.getLargeIcon()) // posted at once, without waiting for the picture
            bg { store.apply(listOf(row("a", Notif.DISMISSED, 2).copy(icon = icon)), 2) }
            assertNull(tray("a"))
            gate.countDown()
            val until = System.currentTimeMillis() + 10_000
            while (tray("b")?.getLargeIcon() == null && System.currentTimeMillis() < until) Thread.sleep(50)
            assertTrue("the picture reached the row still open", tray("b")?.getLargeIcon() != null)
            drainIo()
            assertNull(tray("a"))
        }
    }

    @Test fun clearedRowsFollowTheServerKeep() {
        val day = 24 * 3600_000L
        fun aged(id: String, topic: String, days: Int) = Notif(id, topic, "T", "m", 3, "", null, "[]", "dismissed", null, null, 1,
            System.currentTimeMillis() - days * day, 1)
        listOf(aged("ci3", "ci", 3), aged("ci1", "ci", 1), aged("o3", "other", 3), aged("o8", "other", 8)).forEach { bg { dao.put(it) } }
        app.saveSettings(org.json.JSONObject("""{"keep_cleared_days":30,"topic_keep_cleared_days":{"ci":2}}"""))
        drainIo()
        val left = listOf("ci3", "ci1", "o3", "o8").filter { bg { dao.get(it) } != null }
        assertEquals(listOf("ci1", "o3"), left) // ci keeps 2 days; the global 30 is capped at the phone's 7
    }
}
