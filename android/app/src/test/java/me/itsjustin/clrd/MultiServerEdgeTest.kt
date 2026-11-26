package me.itsjustin.clrd

import android.Manifest
import android.app.Notification
import android.app.NotificationManager
import android.content.Intent
import android.net.Uri
import android.os.Looper
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
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

/** Two servers on one phone (#860): neither one's sound, notice, drafts, sends or old taps reach the other. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class MultiServerEdgeTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val nm = app.getSystemService(NotificationManager::class.java)

    @Before fun setUp() {
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        app.serverUrl = "https://a.example"
    }

    private fun row(id: String) = Notif(id, "t", "T", "m", 3, "", null, "[]", Notif.ACTIVE, null, null, 1, System.currentTimeMillis(), 1)
    private fun drain() {
        shadowOf(Looper.getMainLooper()).idle()
        app.io.submit {}.get(10, TimeUnit.SECONDS)
        app.acts.submit {}.get(10, TimeUnit.SECONDS)
    }

    /** Server B is between making its new channels and saving its new sound when account 0 redoes its own channels. */
    @Test fun twoServersChangingSoundKeepEachOthersChannels() {
        val a = app.account
        val b = app.addAccount("https://b.example", "")
        bg { b.saveSound("ding") }
        val bTray = b.tray
        lateinit var bt: Thread
        lateinit var at: Thread
        synchronized(bTray) { // B's sound change parks in its tray's notice check, after its new channels exist
            bt = Thread { b.saveSound("chime") }.apply { start() }
            while (bt.isAlive && bt.state != Thread.State.BLOCKED) Thread.sleep(5)
            at = Thread { a.tray.createChannels() }.apply { start() }
            at.join(2000)
        }
        bt.join(10_000)
        at.join(10_000)
        assertEquals("chime", b.sound)
        assertNotNull("B's new channels survive account 0's change", nm.getNotificationChannel(Sounds.channel(3, "chime")))
        assertNotNull(nm.getNotificationChannel(Sounds.channel(3, Sounds.DEFAULT)))
        b.db.close()
    }

    /** A sync on account 0, whose Urgent channel still has the pass, leaves the note server B raised. */
    @Test fun oneServersDndNoticeOutlivesAnothersSync() {
        val b = app.addAccount("https://b.example", "")
        bg { b.saveSound("ding") }
        nm.getNotificationChannel(Sounds.channel(5, Sounds.DEFAULT)).setBypassDnd(true)
        nm.getNotificationChannel(Sounds.channel(5, "ding")).setBypassDnd(true)
        assertTrue("B's change took its pass", bg { b.saveSound("chime") })
        b.tray.dndNotice()
        app.tray.reconcile(emptyList(), emptySet()) // account 0's next sync
        assertNotNull(shadowOf(nm).getNotification("dnd${b.id}", Tray.DND_ID))
        assertTrue(b.tray.dndLost())
        b.db.close()
    }

    /** A refused typed answer on server B marks only B's entry as a draft, not account 0's with the same id. */
    @Test fun aDraftBelongsToItsServer() {
        val b = app.addAccount("https://b.example", "")
        val typed = Pending(notifId = "a", action = "reply", body = """{"reply":"note","text":"Back at 5","seq":1}""")
        bg { RoomStore(app, b).replyOutcome(typed, "bad_request") }
        bg { RoomStore(app).apply(listOf(row("a")), 1) }
        val sub = shadowOf(nm).getNotification("a", 0)!!.extras.getCharSequence(Notification.EXTRA_SUB_TEXT)?.toString()
        assertEquals("t", sub)
        shadowOf(Looper.getMainLooper()).idle() // the toast
        b.db.close()
    }

    /** Pointing a server slot at another sign-in drops the old one's typed answers: its row ids are not the new one's. */
    @Test fun aNewSignInDropsTheOldDrafts() {
        bg { app.account.saveServer("https://a.example", "old") }
        app.account.drafts["42"] = "Back at 5"
        bg { app.account.saveServer("https://a.example", "new") }
        assertEquals(null, app.account.drafts["42"])
    }

    /** A tray tap from before the token was replaced at the same address does nothing; one posted after it acts. */
    @Test fun aTapFromAnOldSignInIsRefused() {
        bg { app.account.saveServer("https://a.example", "old") }
        bg { RoomStore(app).apply(listOf(row("a")), 1) }
        fun clearUri() = shadowOf(shadowOf(nm).getNotification("a", 0)!!.actions
            .single { shadowOf(it.actionIntent).savedIntent.action == Tray.ACTION_CLEAR }.actionIntent).savedIntent.dataString!!
        val old = clearUri()
        bg { app.account.saveServer("https://a.example", "") } // a new sign-in at the same address
        bg { RoomStore(app).apply(listOf(row("a")), 1) }
        val fresh = clearUri()
        fun tap(data: String) {
            app.sendBroadcast(Intent(app, ActionReceiver::class.java).setAction(Tray.ACTION_CLEAR).setData(Uri.parse(data)).putExtra(Tray.EXTRA_ID, "a").putExtra(Tray.EXTRA_SERVER, "https://a.example"))
            drain()
        }
        tap(old)
        assertEquals(emptyList<Pending>(), bg { app.db.dao().pending() })
        tap(fresh)
        assertEquals(listOf("dismiss"), bg { app.db.dao().pending().map { it.action } })
        assertEquals("a restart reads the same sign-in, so its tray taps still act", app.account.configGen, Account(app, 0).configGen)
    }

    /** Removing the shown server moves the screens to one still listed, even with its token cleared, not to an emptied account 0. */
    @Test fun removingTheShownServerShowsAListedOne() {
        app.token = "ta"
        val b = app.addAccount("https://b.example", "")
        app.removeAccount(0)
        drain()
        assertEquals(b.id, app.shown.value.id)
        b.db.close()
    }

    /** Removing account 0 keeps its sign-in number, so a tap from before the removal never matches a later sign-in. */
    @Test fun removingAccountZeroKeepsItsSignInNumber() {
        bg { app.account.saveServer("https://a.example", "t") }
        val gen = app.account.configGen
        val b = app.addAccount("https://b.example", "")
        app.removeAccount(0)
        drain()
        assertTrue(app.account.configGen > gen)
        assertEquals("a restart reads the number the removal left", app.account.configGen, Account(app, 0).configGen)
        b.db.close()
    }

    /** Two servers that both lost the Urgent pass each keep a notice; the pass back on one takes only its own (#921). */
    @Test fun eachServerKeepsItsOwnDndNotice() {
        val b = app.addAccount("https://b.example", "")
        bg { b.saveSound("ding") }
        app.tray.dndNotice()
        app.account.prefs.edit().putBoolean("dnd_lost", true).apply() // set with its notice, as dndLost does
        b.tray.dndNotice()
        b.prefs.edit().putBoolean("dnd_lost", true).apply()
        nm.getNotificationChannel(Sounds.channel(5, "ding")).setBypassDnd(true)
        assertFalse(b.tray.dndLost())
        assertNull(shadowOf(nm).getNotification("dnd${b.id}", Tray.DND_ID))
        assertNotNull("account 0's notice stays", shadowOf(nm).getNotification(null, Tray.DND_ID))
        b.db.close()
    }

    /** A notice server B raised before #921 had no tag; B's pass coming back takes it down, unless account 0 has its own up (#943). */
    @Test fun anUntaggedNoticeFromBeforeTheUpgradeGoesWithItsServer() {
        val b = app.addAccount("https://b.example", "")
        bg { b.saveSound("ding") }
        nm.getNotificationChannel(Sounds.channel(5, "ding")).setBypassDnd(true)
        fun restoreB() { b.prefs.edit().putBoolean("dnd_lost", true).apply(); assertFalse(b.tray.dndLost()) }
        app.tray.dndNotice() // the old untagged entry
        app.account.prefs.edit().putBoolean("dnd_lost", true).apply()
        restoreB()
        assertNotNull("account 0's own notice stays", shadowOf(nm).getNotification(null, Tray.DND_ID))
        app.account.prefs.edit().putBoolean("dnd_lost", false).apply()
        restoreB()
        assertNull(shadowOf(nm).getNotification(null, Tray.DND_ID))
        b.db.close()
    }

    /** A swipe checked under an old sign-in hides nothing once a new one has started (#943). */
    @Test fun aStaleSwipeHidesNothing() {
        val gen = app.account.configGen
        bg { app.account.saveServer("https://a.example", "new") }
        assertFalse(app.account.hide(listOf("a"), gen))
        assertEquals(emptySet<String>(), app.account.trayHidden)
        assertTrue(app.account.hide(listOf("a"), app.account.configGen))
        assertEquals(setOf("a"), app.account.trayHidden)
    }

    /** A changed question on server B moves only B's count, not account 0's entry with the same id (#921). */
    @Test fun aChangedQuestionBelongsToItsServer() {
        val b = app.addAccount("https://b.example", "")
        val typed = Pending(notifId = "a", action = "reply", body = """{"reply":"note","text":"Back at 5","seq":1}""")
        bg { RoomStore(app, b).replyOutcome(typed, "conflict") }
        shadowOf(Looper.getMainLooper()).idle()
        assertEquals(1, b.changed["a"])
        assertNull(app.account.changed["a"])
        b.db.close()
    }

    /** A lone swipe acts a second later, and only for the sign-in it was posted under (#921). */
    @Test fun aSwipeBeforeANewSignInIsRefused() {
        app.shadeClears = true
        bg { app.account.saveServer("https://a.example", "old") }
        bg { RoomStore(app).apply(listOf(row("a")), 1) }
        val data = shadowOf(shadowOf(nm).getNotification("a", 0)!!.deleteIntent).savedIntent.dataString!!
        app.sendBroadcast(Intent(app, ActionReceiver::class.java).setAction(Tray.ACTION_DISMISS).setData(Uri.parse(data)).putExtra(Tray.EXTRA_ID, "a").putExtra(Tray.EXTRA_SERVER, "https://a.example"))
        shadowOf(Looper.getMainLooper()).idle() // the tap arrives under the old sign-in
        bg { app.account.saveServer("https://a.example", "new") } // a new sign-in inside the wait
        bg { RoomStore(app).apply(listOf(row("a")), 1) } // the new server's row with the same id
        shadowOf(Looper.getMainLooper()).idleFor(java.time.Duration.ofMillis(App.LONE_MS + 100))
        drain()
        assertEquals(emptyList<Pending>(), bg { app.db.dao().pending() })
    }

    /** With no sync running, one server's send that hangs leaves another server's action free to go (#921). */
    @Test fun aStalledSendWithoutSyncLeavesTheOtherServerFree() {
        val arrived = CountDownLatch(1)
        val release = CountDownLatch(1)
        val slow = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse { arrived.countDown(); release.await(20, TimeUnit.SECONDS); return MockResponse().setBody("{}") }
            }
            start()
        }
        val fast = MockWebServer().apply { dispatcher = object : Dispatcher() { override fun dispatch(request: RecordedRequest) = MockResponse().setBody("{}") }; start() }
        try {
            bg { app.account.saveServer(slow.url("/").toString().trimEnd('/'), "t") }
            val b = app.addAccount(fast.url("/").toString().trimEnd('/'), "t")
            app.account.actAll(listOf("a"), "ack", "{}", null, App.VIA_APP) {}
            assertTrue(arrived.await(10, TimeUnit.SECONDS))
            b.actAll(listOf("a"), "ack", "{}", null, App.VIA_APP) {}
            assertNotNull("B's send waited on account 0's", fast.takeRequest(5, TimeUnit.SECONDS))
            b.db.close()
        } finally {
            release.countDown()
            slow.shutdown()
            fast.shutdown()
        }
    }

    /** One server's send that hangs does not hold back another server's queue. */
    @Test fun oneServersSlowSendLeavesTheOtherFree() {
        val arrived = CountDownLatch(1)
        val release = CountDownLatch(1)
        val slow = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse { arrived.countDown(); release.await(20, TimeUnit.SECONDS); return MockResponse().setBody("{}") }
            }
            start()
        }
        val fast = MockWebServer().apply { dispatcher = object : Dispatcher() { override fun dispatch(request: RecordedRequest) = MockResponse().setBody("{}") }; start() }
        val b = app.addAccount(fast.url("/").toString(), "")
        try {
            bg { app.db.dao().addPending(Pending(notifId = "a", action = "ack", body = "{}")); b.db.dao().addPending(Pending(notifId = "a", action = "ack", body = "{}")) }
            val st = Thread { flushPending(RoomStore(app), Api(app.http, slow.url("/").toString().trimEnd('/'), "t")) }.apply { start() }
            assertTrue(arrived.await(10, TimeUnit.SECONDS))
            val ft = Thread { flushPending(RoomStore(app, b), Api(b.http, fast.url("/").toString().trimEnd('/'), "t")) }.apply { start() }
            ft.join(5000)
            assertFalse("B's send waited on account 0's", ft.isAlive)
            assertEquals(emptyList<Pending>(), bg { b.db.dao().pending() })
            release.countDown()
            st.join(10_000)
        } finally {
            release.countDown()
            slow.shutdown()
            fast.shutdown()
            b.db.close()
        }
    }
}
