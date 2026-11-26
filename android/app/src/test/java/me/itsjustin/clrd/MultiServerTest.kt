package me.itsjustin.clrd

import java.util.concurrent.CountDownLatch
import android.Manifest
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Looper
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.io.File
import java.util.concurrent.TimeUnit

/** More than one server (#78): account 0 looks exactly as before, and each account stays out of the others' way. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class MultiServerTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val nm = app.getSystemService(NotificationManager::class.java)

    @Before fun setUp() {
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        app.serverUrl = "https://a.example"
    }

    private fun row(id: String, topic: String = "t") =
        Notif(id, topic, "T", "m", 3, "", null, "[]", Notif.ACTIVE, null, null, 1, System.currentTimeMillis(), 1)
    private fun drain() {
        shadowOf(Looper.getMainLooper()).idle()
        app.io.submit {}.get(10, TimeUnit.SECONDS)
        app.acts.submit {}.get(10, TimeUnit.SECONDS)
    }
    private fun clearIntent(tag: String) = shadowOf(nm).getNotification(tag, 0)!!.actions
        .single { shadowOf(it.actionIntent).savedIntent.action == Tray.ACTION_CLEAR }.actionIntent
    private fun clearUri(tag: String) = shadowOf(clearIntent(tag)).savedIntent.dataString
    private fun clearTap(data: String, id: String = "a") {
        app.sendBroadcast(Intent(app, ActionReceiver::class.java).setAction(Tray.ACTION_CLEAR).setData(Uri.parse(data)).putExtra(Tray.EXTRA_ID, id))
        drain()
    }

    /** Entries posted by the build before #78 keep working after the update, so account 0 posts them byte for byte as it did. */
    @Test fun accountZeroPostsAsBefore() {
        app.stack = true
        app.addAccount("https://b.example", "")
        bg { RoomStore(app, app.account).apply(listOf(row("a"), row("b")), 1) }
        val n = shadowOf(nm).getNotification("a", 0)!!
        assertEquals("topic:t", n.group)
        assertEquals("clrd://n/a/me.itsjustin.clrd.CLEAR?server=https%3A%2F%2Fa.example", clearUri("a"))
        val summary = shadowOf(nm).getNotification("topic:t", Tray.SUMMARY_ID)!!
        assertEquals("clrd://stack/t?server=https%3A%2F%2Fa.example", shadowOf(summary.deleteIntent).savedIntent.dataString)
        assertEquals(setOf("a" to 0, "b" to 0, "topic:t" to 1), nm.activeNotifications.map { it.tag to it.id }.toSet())
    }

    /** A tap on another server's entry or stack names that server, so the app can open its inbox; account 0's is unchanged. */
    @Test fun aTapOpensTheServerItCameFrom() {
        app.stack = true
        val b = app.addAccount("https://b.example", "")
        bg { RoomStore(app, app.account).apply(listOf(row("a"), row("b")), 1) }
        bg { RoomStore(app, b).apply(listOf(row("a"), row("c")), 1) }
        fun open(tag: String, id: Int = 0) = shadowOf(shadowOf(nm).getNotification(tag, id)!!.contentIntent).savedIntent
        assertNull(open("a").data)
        assertNull(open("topic:t", Tray.SUMMARY_ID).data)
        assertEquals("${b.id}", open("a${b.id}:a").data?.getQueryParameter(Tray.QUERY_ACCOUNT))
        assertEquals("${b.id}", open("topic:${b.id}:t", Tray.SUMMARY_ID).data?.getQueryParameter(Tray.QUERY_ACCOUNT))
    }

    @Test fun anotherAccountTagsItsEntriesAndLeavesAccountZerosAlone() {
        app.stack = true
        val b = app.addAccount("https://b.example", "")
        bg { RoomStore(app, app.account).apply(listOf(row("a")), 1) }
        bg { RoomStore(app, b).apply(listOf(row("a"), row("c")), 1) } // the same id and topic on the other server
        assertEquals("topic:${b.id}:t", shadowOf(nm).getNotification("a${b.id}:a", 0)!!.group)
        assertEquals("clrd://n/a/me.itsjustin.clrd.CLEAR?server=https%3A%2F%2Fb.example&account=${b.id}", clearUri("a${b.id}:a"))
        val summary = shadowOf(nm).getNotification("topic:${b.id}:t", Tray.SUMMARY_ID)!!
        assertEquals(listOf("a", "c"), shadowOf(summary.deleteIntent).savedIntent.getStringArrayExtra(Tray.EXTRA_IDS)!!.sorted())
        assertNull("one row is no stack", shadowOf(nm).getNotification("topic:t", Tray.SUMMARY_ID))

        app.tray.reconcile(emptyList(), emptySet()) // account 0 has nothing open now
        assertNull(shadowOf(nm).getNotification("a", 0))
        assertNotNull("account 0's sweep never takes another server's entry", shadowOf(nm).getNotification("a${b.id}:a", 0))
        assertNotNull(shadowOf(nm).getNotification("topic:${b.id}:t", Tray.SUMMARY_ID))
    }

    /** A tap posted before #78, or by account 0, has no account in its URI: it acts on account 0, not on the other server. */
    @Test fun anOldTapActsOnAccountZero() {
        val b = app.addAccount("https://a.example", "") // even with the same address
        bg { RoomStore(app, app.account).apply(listOf(row("a")), 1); RoomStore(app, b).apply(listOf(row("a")), 1) }
        clearTap("clrd://n/a/me.itsjustin.clrd.CLEAR?server=https%3A%2F%2Fa.example")
        assertEquals(listOf("dismiss"), bg { app.db.dao().pending().map { it.action } })
        assertEquals(emptyList<Pending>(), bg { b.db.dao().pending() })
        clearTap("clrd://n/a/me.itsjustin.clrd.CLEAR?server=https%3A%2F%2Fa.example&account=${b.id}")
        assertEquals(listOf("dismiss"), bg { b.db.dao().pending().map { it.action } })
        b.db.close()
    }

    @Test fun aSettingsMessageForAnotherServerLeavesAccountZeroAlone() {
        app.stack = true
        bg { RoomStore(app, app.account).apply(listOf(row("a").copy(priority = 4)), 1) }
        val b = app.addAccount("https://b.example", "")
        val before = app.account.prefs.all.toMap() // account 0 shares the phone's file, which now lists the new server
        val o = JSONObject("""{"sound":"chime","render_formatting":false,"swipe_start":"ack","snooze_intervals":["15m"],"snooze_default":"15m","subscriptions":{"new_topics":false,"topics":{"x":true}}}""")
        bg { RoomStore(app, b).settings(o) }
        drain()
        assertFalse(b.formatting.value)
        assertEquals("chime", b.sound)
        assertTrue(app.formatting.value)
        assertEquals(Snooze(), app.snooze.value)
        assertEquals(Swipes(), app.swipes.value)
        assertNull(app.subs.value)
        assertEquals(Sounds.DEFAULT, app.sound)
        assertEquals(before, app.account.prefs.all.toMap())
        // The other server's sound must not take account 0's channels, or its entries with them.
        assertNotNull(nm.getNotificationChannel("p4"))
        assertEquals("p4", shadowOf(nm).getNotification("a", 0)!!.channelId)
        b.db.close()
    }

    /** Two servers syncing at once: each sees only its own account's token. */
    @Test fun eachServerSeesOnlyItsOwnToken() {
        val seen = listOf(MockWebServer(), MockWebServer()).onEach {
            it.dispatcher = object : Dispatcher() { override fun dispatch(request: RecordedRequest) = MockResponse().setResponseCode(503) }
            it.start()
        }
        try {
            app.token = "token-a"
            app.serverUrl = seen[0].url("/").toString()
            val svc = Robolectric.buildService(SyncService::class.java).create().startCommand(0, 1)
            try {
                val first = app.account.sync!!
                app.addAccount(seen[1].url("/").toString(), "token-b") // added while the service runs
                svc.startCommand(0, 2)
                assertTrue("account 0 keeps its connection", app.account.sync === first)
                assertEquals("Bearer token-a", seen[0].takeRequest(10, TimeUnit.SECONDS)!!.getHeader("Authorization"))
                assertEquals("Bearer token-b", seen[1].takeRequest(10, TimeUnit.SECONDS)!!.getHeader("Authorization"))
                repeat(3) { _ -> seen.forEachIndexed { i, s -> s.takeRequest(200, TimeUnit.MILLISECONDS)?.let { assertEquals("Bearer token-${"ab"[i]}", it.getHeader("Authorization")) } } }
                assertTrue(app.accounts.all { it.sync != null })
            } finally {
                svc.destroy()
            }
            assertTrue(app.accounts.all { it.sync == null })
        } finally {
            app.token = ""
            seen.forEach { it.shutdown() }
        }
    }

    @Test fun removeThenAddGivesANewIdAndForgetsTheOld() {
        val b = app.addAccount("https://a.example", "tb") // the same address as account 0, so only the account id can stop the old tap
        bg { RoomStore(app, b).apply(listOf(row("a")), 1) }
        val tag = "a${b.id}:a"
        val tap = clearIntent(tag)
        val prefsFile = File(app.applicationInfo.dataDir, "shared_prefs/clrd-${b.id}.xml")
        val dbFile = app.getDatabasePath("clrd-${b.id}.db")
        b.prefs.edit().commit()
        assertTrue(prefsFile.exists() && dbFile.exists())

        app.removeAccount(b.id)
        drain()
        assertFalse(prefsFile.exists())
        assertFalse(dbFile.exists())
        assertNull(shadowOf(nm).getNotification(tag, 0))
        assertEquals(listOf(app.account), app.accounts)
        assertNull(app.account(b.id))
        bg { RoomStore(app, b).reconcile(); b.tray.repost("a") } // work still queued for the removed server

        val c = app.addAccount("https://c.example", "tc")
        assertNotEquals(b.id, c.id)
        tap.send() // the old entry's tap, for the removed server
        drain()
        assertEquals(emptyList<Pending>(), bg { c.db.dao().pending() })
        assertEquals(emptyList<Pending>(), bg { app.db.dao().pending() })
        assertFalse("nothing brings the old files back", prefsFile.exists() || dbFile.exists())
        c.db.close()
    }

    @Test fun removingAccountZeroKeepsThePhonesChoices() {
        app.token = "t0"
        app.stack = true
        app.account.prefs.edit().putString("snooze", "{}").putString("some_later_key", "x").commit()
        bg { RoomStore(app, app.account).apply(listOf(row("a")), 1) }
        app.removeAccount(0)
        drain()
        assertTrue(app.stack)
        assertEquals(emptySet<String>(), app.prefs.all.keys - App.PHONE_KEYS)
        assertFalse(app.configured)
        assertEquals(emptyList<Notif>(), bg { app.db.dao().openNow() })
        assertNull(shadowOf(nm).getNotification("a", 0))
    }

    /** An install from before #78: only the old "clrd" file, no server list. It is account 0 alone, with no setup. */
    @Test fun anOldInstallIsAccountZeroAlone() {
        app.getSharedPreferences("clrd", Context.MODE_PRIVATE).edit().putString("url", "https://home.example").putString("token", "tk").commit()
        assertFalse(app.prefs.contains("servers"))
        assertEquals(listOf(app.account), app.accounts)
        assertEquals("https://home.example", app.accounts.single().serverUrl)
        assertTrue(app.anyConfigured)
    }

    /** Removal is marked in the same write that drops the server, before its cleanup is queued. */
    @Test fun aRemovalIsMarkedBeforeItsCleanup() {
        val b = app.addAccount("https://b.example", "tb")
        val gate = CountDownLatch(1)
        app.io.execute { gate.await(10, TimeUnit.SECONDS) }
        app.removeAccount(b.id)
        assertEquals("${b.id}", app.prefs.getString("removing", null))
        gate.countDown()
        drain()
        assertEquals("", app.prefs.getString("removing", null))
    }

    /** The process died after the removal was saved and before its cleanup ran: the next start finishes it. */
    @Test fun aRemovalTheProcessDidNotFinishIsFinishedOnTheNextStart() {
        val b = app.addAccount("https://b.example", "tb")
        bg { RoomStore(app, b).apply(listOf(row("a")), 1) }
        val tag = "a${b.id}:a"
        val prefsFile = File(app.applicationInfo.dataDir, "shared_prefs/clrd-${b.id}.xml")
        val dbFile = app.getDatabasePath("clrd-${b.id}.db")
        b.prefs.edit().commit()
        bg { b.close() }
        app.prefs.edit().putString("servers", "").putString("removing", "${b.id}").commit() // what the removal saved
        assertTrue(prefsFile.exists() && dbFile.exists() && shadowOf(nm).getNotification(tag, 0) != null)

        app.purgeRemoved()
        drain()
        assertFalse(prefsFile.exists())
        assertFalse(dbFile.exists())
        assertNull(shadowOf(nm).getNotification(tag, 0))
        assertEquals("", app.prefs.getString("removing", null))
    }
}
