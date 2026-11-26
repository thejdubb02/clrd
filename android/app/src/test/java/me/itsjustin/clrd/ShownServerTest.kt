package me.itsjustin.clrd

import android.Manifest
import android.app.Notification
import android.app.NotificationManager
import android.os.Looper
import androidx.test.core.app.ApplicationProvider
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.util.concurrent.TimeUnit

/** The server the screens show (#78): the switch, where links go, and that the background never follows it. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class ShownServerTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val nm = app.getSystemService(NotificationManager::class.java)

    @Before fun setUp() {
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        app.account.serverUrl = "https://a.example" // through the account, so the shown server is not read yet
        app.account.token = "ta"
    }

    private fun row(id: String) = Notif(id, "t", "T", "m", 3, "", null, "[]", Notif.ACTIVE, null, null, 1, System.currentTimeMillis(), 1)
    private fun drain() {
        shadowOf(Looper.getMainLooper()).idle()
        app.io.submit {}.get(10, TimeUnit.SECONDS)
    }
    private fun subText(tag: String) = shadowOf(nm).getNotification(tag, 0)!!.extras.getCharSequence(Notification.EXTRA_SUB_TEXT)?.toString()

    @Test fun switchingMovesTheScreensAndIsRemembered() {
        val b = app.addAccount("https://b.example", "tb")
        assertEquals("https://a.example", app.serverUrl)
        app.show(b.id)
        assertEquals("https://b.example", app.serverUrl)
        assertEquals("tb", app.token)
        assertSame(b.db, app.db)
        assertSame(b.status, app.status)
        assertSame(b.swipes, app.swipes)
        assertEquals(b.id, app.prefs.getInt("shown_server", 0))
        b.db.close()
    }

    @Test fun theRememberedServerIsShownOnStart() {
        val b = app.addAccount("https://b.example", "tb")
        app.prefs.edit().putInt("shown_server", b.id).commit() // as a last run left it; [App.shown] is read first here
        assertEquals(b.id, app.shown.value.id)
    }

    @Test fun aRememberedServerThatIsGoneShowsAccountZero() {
        app.prefs.edit().putInt("shown_server", 7).commit()
        assertEquals(0, app.shown.value.id)
    }

    @Test fun removingTheShownServerFallsBackToAccountZero() {
        val b = app.addAccount("https://b.example", "tb")
        app.show(b.id)
        app.removeAccount(b.id)
        assertEquals("at once, before its database closes", 0, app.shown.value.id)
        drain()
        assertEquals("https://a.example", app.serverUrl)
        assertEquals(0, app.prefs.getInt("shown_server", -1))
    }

    /** Removing account 0 with another server left must not strand the screens on an empty server, after a restart too. */
    @Test fun removingAccountZeroShowsTheServerLeft() {
        val b = app.addAccount("https://b.example", "tb")
        app.removeAccount(0)
        assertEquals(b.id, app.shown.value.id)
        drain()
        assertEquals("the choice is the phone's, not account 0's server key", b.id, app.prefs.getInt("shown_server", 0))
        b.db.close()
    }

    /** A settings message for account 0 lands on account 0 while another server is shown: the service holds its own. */
    @Test fun theSyncServiceKeepsItsOwnServerAfterASwitch() {
        val ws = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse =
                    if (request.path.orEmpty().startsWith("/api/v1/ws")) MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) {
                            webSocket.send("""{"type":"settings","settings":{"swipe_start":["ack"]}}""")
                            webSocket.send("""{"type":"ready","seq":0}""")
                        }
                        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { webSocket.close(code, null) }
                    }) else MockResponse().setBody("""{"notifications":[],"seq":0}""")
            }
            start()
        }
        try {
            app.account.serverUrl = ws.url("/").toString()
            val b = app.addAccount("https://b.example", "") // shown, not set up, so the service runs account 0 alone
            app.show(b.id)
            val svc = Robolectric.buildService(SyncService::class.java).create().startCommand(0, 1)
            try {
                val until = System.nanoTime() + 10_000_000_000L
                while (app.account.swipes.value.start != listOf("ack") && System.nanoTime() < until) Thread.sleep(20)
                assertEquals(listOf("ack"), app.account.swipes.value.start)
                assertEquals(Swipes(), b.swipes.value)
                assertEquals(Swipes(), app.swipes.value) // the screens still show the other server's
            } finally {
                svc.destroy()
            }
            b.db.close()
        } finally {
            ws.shutdown()
        }
    }

    @Test fun trayEntriesNameTheirServerOnlyWithMoreThanOne() {
        bg { RoomStore(app, app.account).apply(listOf(row("a")), 1) }
        assertEquals("t", subText("a"))
        assertNull("account 0's tap is today's", shadowOf(shadowOf(nm).getNotification("a", 0)!!.contentIntent).savedIntent.data)

        val b = app.saveTo("https://b.example", "tb")
        drain() // the entries already up are drawn again with their server
        assertEquals("a.example · t", subText("a"))
        bg { RoomStore(app, b).apply(listOf(row("c")), 1) }
        assertEquals("b.example · t", subText("a${b.id}:c"))

        // A tap on the other server's entry opens the app on it.
        val open = shadowOf(shadowOf(nm).getNotification("a${b.id}:c", 0)!!.contentIntent).savedIntent
        assertEquals("clrd://open?account=${b.id}", open.dataString)
        app.show(0)
        assertEquals(b.id, openedAccount(open))
        Robolectric.buildActivity(MainActivity::class.java, open).create()
        assertEquals(b.id, app.shown.value.id)
        val zero = shadowOf(shadowOf(nm).getNotification("a", 0)!!.contentIntent).savedIntent
        assertNull("still today's intent", zero.data)
        Robolectric.buildActivity(MainActivity::class.java, zero).create()
        assertEquals("account 0's tap shows account 0", 0, app.shown.value.id)

        app.removeAccount(b.id)
        drain()
        assertEquals("one server again: as before", "t", subText("a"))
    }

    @Test fun aLinkForAServerThePhoneHasGoesToIt() {
        val b = app.addAccount("https://b.example", "tb")
        val got = bg { app.saveTo("https://B.example/", "tb2") }
        assertSame(b, got)
        assertEquals("tb2", b.token)
        assertEquals(b.id, app.shown.value.id)
        assertEquals(2, app.accounts.size)
        assertEquals("ta", app.account.token)
        b.db.close()
    }

    @Test fun aLinkForANewServerAddsItBesideTheOthers() {
        val c = bg { app.saveTo("https://c.example", "tc") }
        assertEquals(listOf(0, c.id), app.accounts.map { it.id })
        assertTrue(c.id > 0)
        assertEquals("https://c.example", c.serverUrl)
        assertEquals("the first server is not replaced", "https://a.example", app.account.serverUrl)
        assertEquals("ta", app.account.token)
        assertEquals(c.id, app.shown.value.id)
        drain()
        c.db.close()
    }

    @Test fun aLinkOnAnEmptyPhoneSetsUpAccountZeroAsBefore() {
        app.account.serverUrl = ""
        app.account.token = ""
        val got = bg { app.saveTo("https://c.example", "tc") }
        assertSame(app.account, got)
        assertEquals(listOf(0), app.accounts.map { it.id })
        assertEquals("https://c.example", app.serverUrl)
    }

    /** Save and Test writes the address, then the token, under the lock: a reader in between waits for both. */
    @Test fun addressAndTokenAreReadAsOnePair() {
        val a = app.account
        val inside = java.util.concurrent.CountDownLatch(1)
        val release = java.util.concurrent.CountDownLatch(1)
        val w = kotlin.concurrent.thread { synchronized(a.configLock) { a.serverUrl = "https://b.example"; inside.countDown(); release.await(5, TimeUnit.SECONDS); a.token = "tb" } }
        inside.await(5, TimeUnit.SECONDS)
        var got: Pair<String, String>? = null
        val r = kotlin.concurrent.thread { got = a.creds() }
        r.join(300) // without the lock it has read the new address with the old token by now
        release.countDown()
        r.join()
        w.join()
        assertEquals("https://b.example" to "tb", got)
    }
}
