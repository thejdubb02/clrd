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

    /** Room refuses the main thread, which is the Robolectric test thread. */
    private fun <T> bg(f: () -> T): T {
        var r: Result<T>? = null
        Thread { r = runCatching(f) }.apply { start(); join(60_000) } // the first Room open in a JVM is slow on CI
        return checkNotNull(r) { "background step still running after 60 s" }.getOrThrow()
    }

    private fun drainIo() {
        app.io.submit {}.get(10, TimeUnit.SECONDS)
        app.acts.submit {}.get(10, TimeUnit.SECONDS)
    }
    private fun tray(id: String): Notification? = shadowOf(nm).getNotification(id, 0)
    private fun row(id: String, state: String, seq: Long) =
        Notif(id, "t", "T", "m", 3, "", null, "[]", state, null, null, seq, System.currentTimeMillis(), seq) // recent, or prune drops it
    private fun buttons(n: Notification) = n.actions.orEmpty().map { it.title.toString() }

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
        app.sendBroadcast(Intent(app, ActionReceiver::class.java).setAction(Tray.ACTION_DISMISS).putExtra(Tray.EXTRA_ID, "a"))
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
        assertEquals(Notif.ACKNOWLEDGED, bg { dao.openNow().single().state })
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
}
