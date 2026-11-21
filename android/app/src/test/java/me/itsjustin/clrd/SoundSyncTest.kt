package me.itsjustin.clrd

import android.Manifest
import android.app.NotificationManager
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.json.JSONObject
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

/** #289: the sound choice is kept per device on the server, in both directions. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SoundSyncTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val nm = app.getSystemService(NotificationManager::class.java)
    private val server = MockWebServer()

    @Before fun setUp() {
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        server.start()
        app.serverUrl = server.url("/").toString()
        app.token = "tok"
    }

    @After fun tearDown() = server.shutdown()

    private fun <T> onIo(f: () -> T): T = app.io.submit(f).get(30, TimeUnit.SECONDS)

    @Test fun aPickGoesToTheServer() {
        server.enqueue(MockResponse().setBody("{}"))
        assertNull(onIo { app.pickSound("bell") })
        val r = server.takeRequest(5, TimeUnit.SECONDS)!!
        assertEquals("PUT /api/v1/settings", "${r.method} ${r.path}")
        assertEquals("bell", JSONObject(r.body.readUtf8()).getString("sound"))
        assertNull(app.soundPending)
        assertEquals("bell", app.sound)
    }

    @Test fun theServersChoicePlaysHere() {
        onIo { app.saveSettings(JSONObject("""{"sound":"alert"}""")) } // an agent set it over MCP; the sync thread in the app
        assertEquals("alert", app.sound) // at once, before the next event is handled
        assertEquals("alert", app.soundNow.value)
        assertNotNull(nm.getNotificationChannel("p3-alert"))
        assertNull(nm.getNotificationChannel("p3"))
        onIo { app.saveSettings(JSONObject("""{"sound":"from-the-future"}""")) } // a name this build does not know
        assertEquals(Sounds.DEFAULT, app.sound)
    }

    @Test fun aRefusedPickStaysAndIsSentAgain() {
        server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"limited token"}"""))
        assertNotNull(onIo { app.pickSound("bell") })
        assertEquals("bell", app.soundPending)
        server.takeRequest(5, TimeUnit.SECONDS)
        server.enqueue(MockResponse().setBody("{}"))
        onIo { app.saveSettings(JSONObject("""{"sound":"pop"}""")) } // the next connect: ours wins and goes out again
        onIo {} // the resend it queued
        assertEquals("bell", app.sound)
        assertEquals("bell", JSONObject(server.takeRequest(5, TimeUnit.SECONDS)!!.body.readUtf8()).getString("sound"))
        assertNull(app.soundPending)
    }

    @Test fun aSwitchToAnotherServerDropsTheRefusedChoice() {
        app.prefs.edit().putString("sound_pending", "bell").commit()
        Thread { app.saveServer("https://b.example", "tok2") }.apply { start() }.join(60_000) // Room refuses the main thread
        assertNull(app.soundPending)
    }
}
