package me.itsjustin.clrd

import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.os.Looper
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.json.JSONArray
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowToast
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** ntfy's copy, HTTP and broadcast buttons run on the phone. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class DeviceActionTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val dao get() = app.db.dao()
    private val web = MockWebServer()

    @Before fun setUp() { web.start(); app.serverUrl = "https://a.example"; app.token = "" }
    @After fun tearDown() = web.shutdown()

    private fun row(actions: JSONArray, seq: Long = 1) =
        Notif("a", "home", "T", "m", 3, "", null, actions.toString(), Notif.ACTIVE, null, null, 1, 1, seq)

    private fun http(clear: Boolean = false) = JSONObject().put("label", "Off").put("clear", clear).put(
        "http", JSONObject().put("method", "put").put("url", web.url("/light").toString())
            .put("headers", JSONObject().put("X-Key", "k1")).put("body", "off"),
    )

    private fun runIt(n: Notif, index: Int, seq: Long = n.seq) {
        bg { dao.applyAll(listOf(n), n.seq) }
        val done = CountDownLatch(1)
        DeviceAction.run(app, app.account, n.id, index, seq, "https://a.example") { done.countDown() }
        assert(done.await(10, TimeUnit.SECONDS))
        app.acts.submit {}.get(10, TimeUnit.SECONDS)
        shadowOf(Looper.getMainLooper()).idle()
    }

    @Test fun trayShowsThemWithTheirIndex() {
        val acts = JSONArray().put(JSONObject().put("label", "Docs").put("url", "https://x.example")).put(http())
        val b = TrayButton.pick(row(acts))
        assertEquals(listOf(null, 1), b.take(2).map { it.device })
        assertEquals("Off", b[1].label)
    }

    @Test fun copyAppearsOnTheCardAndTrayAtTheSameIndex() {
        val n = row(JSONArray().put(JSONObject().put("label", "Docs").put("url", "https://x.example"))
            .put(JSONObject().put("label", "Copy code").put("copy", "code")))
        val tray = TrayButton.pick(n).first { it.device != null }
        assertEquals(1, tray.device)
        assertEquals("Copy code", tray.label)
        assertEquals(listOf(tray), TrayButton.copies(n))
        assertTrue(TrayButton.copies(n.copy(state = Notif.DISMISSED)).isEmpty())
    }

    @Test fun copiesTheExactValuePrivatelyAndClearsOnlyWhenAsked() {
        val clipboard = app.getSystemService(ClipboardManager::class.java)
        val value = "  one-time code\n"
        val action = JSONObject().put("copy", value)
        runIt(row(JSONArray().put(action)), 0)
        val clip = clipboard.primaryClip!!
        assertEquals(value, clip.getItemAt(0).text)
        assertTrue(clip.description.extras!!.getBoolean(ClipDescription.EXTRA_IS_SENSITIVE))
        assertEquals(Notif.ACTIVE, bg { dao.get("a") }!!.state)

        runIt(row(JSONArray().put(action.put("clear", true)), seq = 2), 0)
        assertEquals(Notif.DISMISSED, bg { dao.get("a") }!!.state)
        assertEquals(0, web.requestCount)
    }

    @Test fun aStaleCopyDoesNotChangeTheClipboardOrClear() {
        val clipboard = app.getSystemService(ClipboardManager::class.java)
        clipboard.setPrimaryClip(ClipData.newPlainText("", "previous"))
        val action = JSONObject().put("copy", "new").put("clear", true)
        runIt(row(JSONArray().put(action), seq = 2), 0, seq = 1)
        assertEquals("previous", clipboard.primaryClip!!.getItemAt(0).text)
        assertEquals(Notif.ACTIVE, bg { dao.get("a") }!!.state)
    }

    @Test fun aQueuedClearDoesNotDismissANewerVersion() {
        val n = row(JSONArray(), seq = 2)
        bg { dao.applyAll(listOf(n), n.seq) }
        val done = CountDownLatch(1)
        app.account.actAll(listOf(n.id), "dismiss", "{}", "https://a.example", App.VIA_APP, expectedSeq = 1) { done.countDown() }
        assertTrue(done.await(10, TimeUnit.SECONDS))
        assertEquals(Notif.ACTIVE, bg { dao.get(n.id) }!!.state)
        assertTrue(bg { dao.pending() }.isEmpty())
    }

    @Test fun requestIsBuiltAsSent() {
        val r = DeviceAction.request(http())!!
        assertEquals("PUT", r.method)
        assertEquals("k1", r.header("X-Key"))
        assertEquals("POST", DeviceAction.request(JSONObject().put("http", JSONObject().put("url", "https://x.example")))!!.method)
        assertNull(DeviceAction.request(JSONObject().put("http", JSONObject().put("url", "file:///etc/passwd"))))
        assertNull(DeviceAction.request(JSONObject().put("http", JSONObject().put("url", "https://x.example").put("method", "TRACE"))))
    }

    @Test fun broadcastUsesNtfysDefaultIntent() {
        val i = DeviceAction.intent(JSONObject().put("broadcast", JSONObject().put("extras", JSONObject().put("cmd", "lights"))))!!
        assertEquals(DeviceAction.DEFAULT_INTENT, i.action)
        assertEquals("lights", i.getStringExtra("cmd"))
    }

    @Test fun aTapSendsTheRequestAndClearsWhenAsked() {
        web.enqueue(MockResponse().setResponseCode(200))
        runIt(row(JSONArray().put(http(clear = true))), 0)
        val got = web.takeRequest(5, TimeUnit.SECONDS)!!
        assertEquals("PUT", got.method)
        assertEquals("off", got.body.readUtf8())
        assertEquals("k1", got.getHeader("X-Key"))
        assertNull("the clrd token never goes to a sender's address", got.getHeader("Authorization"))
        assertEquals(Notif.DISMISSED, bg { dao.get("a") }!!.state)
    }

    @Test fun aFailureSaysSoAndKeepsTheRow() {
        web.enqueue(MockResponse().setResponseCode(500))
        runIt(row(JSONArray().put(http(clear = true))), 0)
        assertEquals(app.getString(R.string.device_failed, "Off"), ShadowToast.getTextOfLatestToast())
        assertEquals(Notif.ACTIVE, bg { dao.get("a") }!!.state)
    }

    @Test fun aRedirectIsNotFollowed() { // it would carry the sender's headers to wherever it points
        web.enqueue(MockResponse().setResponseCode(302).setHeader("Location", web.url("/elsewhere").toString()))
        web.enqueue(MockResponse().setResponseCode(200))
        runIt(row(JSONArray().put(http(clear = true))), 0)
        assertEquals(1, web.requestCount)
        assertEquals(Notif.ACTIVE, bg { dao.get("a") }!!.state)
    }

    @Test fun aTapOnAnOlderVersionRunsNothing() {
        runIt(row(JSONArray().put(http()), seq = 2), 0, seq = 1)
        assertEquals(0, web.requestCount)
        assertEquals(app.getString(R.string.device_changed), ShadowToast.getTextOfLatestToast())
    }
}
