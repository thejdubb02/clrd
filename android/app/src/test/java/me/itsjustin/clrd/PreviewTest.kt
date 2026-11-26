package me.itsjustin.clrd

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.util.concurrent.TimeUnit

/** #107: the display switches in Settings, and the link preview card on an opened message. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class PreviewTest {
    @get:Rule val rule = createComposeRule()
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val server = MockWebServer()

    @Before fun setUp() {
        server.start()
        app.serverUrl = server.url("/").toString()
        app.token = "tok"
    }

    @After fun tearDown() {
        server.shutdown()
        onIo { app.saveServer(app.serverUrl, "") } // back to the defaults for the next test
    }

    private val seen = java.util.concurrent.CopyOnWriteArrayList<okhttp3.mockwebserver.RecordedRequest>()
    private fun previews(): Int { while (true) { val r = server.takeRequest(200, TimeUnit.MILLISECONDS) ?: break; if (r.requestUrl!!.encodedPath == "/api/v1/preview") seen += r }; return seen.size }
    private fun <T> onIo(f: () -> T): T = app.io.submit(f).get(30, TimeUnit.SECONDS)
    private fun row(msg: String) = Notif("p1", "alerts", "T", msg, 3, "", null, "[]", Notif.ACTIVE, null, null, 1, System.currentTimeMillis(), 1)

    @Test fun previewsAreOffUntilTheServerSaysAndATopicCanDiffer() {
        assertFalse(app.linkPreviews.value) // out of the box: no page is fetched
        onIo { app.saveSettings(JSONObject("""{"link_previews":true,"topic_previews":{"alerts":false,"odd":"x"}}""")) }
        assertTrue(app.linkPreviews.value)
        assertEquals(mapOf("alerts" to false), app.topicPreviews.value)
        onIo { app.saveSettings(JSONObject("""{"swipe_start":[]}""")) } // a message without them keeps them
        assertEquals(mapOf("alerts" to false), app.topicPreviews.value)
        onIo { app.saveServer(app.serverUrl, "other-token") }
        assertFalse(app.linkPreviews.value)
        assertEquals(emptyMap<String, Boolean>(), app.topicPreviews.value)
    }

    @Test fun aSwitchSendsOnlyItsOwnField() {
        server.enqueue(MockResponse().setBody("{}"))
        assertNull(onIo { app.saveDisplay("link_previews", true) })
        val r = server.takeRequest(5, TimeUnit.SECONDS)!!
        assertEquals("PUT /api/v1/settings", "${r.method} ${r.bare}")
        assertEquals("""{"link_previews":true}""", r.body.readUtf8())
        assertTrue(app.linkPreviews.value)
        assertTrue(app.prefs.getBoolean("link_previews", false)) // kept for a restart before the echo
    }

    @Test fun aRefusedSwitchGoesBackAndALimitedTokenLocksThem() {
        server.enqueue(MockResponse().setResponseCode(500).setBody("""{"error":"boom"}"""))
        assertEquals("HTTP 500: boom", onIo { app.saveDisplay("autolink", false) })
        assertTrue(app.autolink.value)
        server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"forbidden"}"""))
        assertNull(onIo { app.saveDisplay("favicons", false) })
        assertTrue(app.favicons.value)
        assertTrue(app.swipesLocked.value)
    }

    @Test fun anOpenedCardShowsThePreviewTheServerFetched() {
        onIo { app.saveSettings(JSONObject("""{"link_previews":true}""")) }
        val body = """{"url":"https://x.example/a","title":"Build 42 failed","description":"3 tests","site_name":"CI","image":"data:text/html,<b>"}"""
        server.dispatcher = object : okhttp3.mockwebserver.Dispatcher() { // a site icon may be asked for too
            override fun dispatch(request: okhttp3.mockwebserver.RecordedRequest) = if (request.requestUrl!!.encodedPath == "/api/v1/preview") MockResponse().setBody(body) else MockResponse().setResponseCode(404)
        }
        rule.setContent { Card(row("See https://x.example/a now")) }
        rule.onNodeWithText("Build 42 failed").assertDoesNotExist() // nothing is fetched for a folded card
        assertEquals(0, previews()) // nothing is fetched for a folded card
        rule.onNodeWithText("T").performClick()
        rule.waitUntil(5_000) { previews() > 0 }; val r = seen.first()
        assertEquals("/api/v1/preview", r.requestUrl!!.encodedPath)
        assertEquals("https://x.example/a", r.requestUrl!!.queryParameter("url"))
        assertEquals("alerts", r.requestUrl!!.queryParameter("topic"))
        rule.waitUntil(5_000) { rule.onAllNodesWithTagCount("preview") == 1 }
        rule.onNodeWithText("Build 42 failed").assertExists()
        rule.onNodeWithText("CI").assertExists()
    }

    @Test fun aTopicTurnedOffFetchesNothing() {
        onIo { app.saveSettings(JSONObject("""{"link_previews":true,"topic_previews":{"alerts":false}}""")) }
        rule.setContent { Card(row("See https://x.example/b")) }
        rule.onNodeWithText("T").performClick()
        rule.waitForIdle()
        assertEquals(0, previews())
        rule.onNodeWithTag("preview").assertDoesNotExist()
    }

    @Test fun anotherAccountDoesNotGetACachedPreview() {
        server.dispatcher = object : okhttp3.mockwebserver.Dispatcher() {
            override fun dispatch(request: okhttp3.mockwebserver.RecordedRequest) = MockResponse().setBody("""{"title":"Mine"}""")
        }
        assertEquals("Mine", onIo { LinkPreview.load(app.shown.value, "https://x.example/c", "alerts") }?.title)
        onIo { LinkPreview.load(app.shown.value, "https://x.example/c", "alerts") }
        assertEquals(1, server.requestCount) // the same account asks once
        app.token = "someone-else"
        onIo { LinkPreview.load(app.shown.value, "https://x.example/c", "alerts") }
        assertEquals(2, server.requestCount)
    }

    @Test fun aSettingsMessageDuringTheSaveWinsOverWhatWasSent() { // #818
        server.dispatcher = object : okhttp3.mockwebserver.Dispatcher() {
            override fun dispatch(request: okhttp3.mockwebserver.RecordedRequest): MockResponse {
                app.saveSettings(JSONObject("""{"link_previews":false}""")) // the web turned it off before this reply came back
                return MockResponse().setBody("{}")
            }
        }
        assertNull(onIo { app.saveDisplay("link_previews", true) })
        assertFalse(app.linkPreviews.value)
        assertFalse(app.prefs.getBoolean("link_previews", true))
    }

    @Test fun aRedirectedPreviewIsNotFollowed() { // #818
        server.dispatcher = object : okhttp3.mockwebserver.Dispatcher() {
            override fun dispatch(request: okhttp3.mockwebserver.RecordedRequest) =
                if (request.requestUrl!!.encodedPath == "/api/v1/preview") MockResponse().setResponseCode(302).setHeader("Location", server.url("/elsewhere"))
                else MockResponse().setBody("""{"title":"Followed"}""")
        }
        assertNull(onIo { LinkPreview.load(app.shown.value, "https://x.example/r", "alerts") })
        assertEquals(1, server.requestCount)
    }

    @Test fun anOpenCardAsksAgainAfterASave() { // #818: the first ask beat the switch to the server
        onIo { app.saveSettings(JSONObject("""{"link_previews":true}""")) }
        val on = java.util.concurrent.atomic.AtomicBoolean()
        val refused = java.util.concurrent.atomic.AtomicInteger()
        server.dispatcher = object : okhttp3.mockwebserver.Dispatcher() {
            override fun dispatch(request: okhttp3.mockwebserver.RecordedRequest) = when (request.requestUrl!!.encodedPath) {
                "/api/v1/settings" -> { on.set(true); MockResponse().setBody("{}") }
                "/api/v1/preview" -> if (on.get()) MockResponse().setBody("""{"title":"Now on"}""") else { refused.incrementAndGet(); MockResponse().setResponseCode(403) }
                else -> MockResponse().setResponseCode(404)
            }
        }
        rule.setContent { Card(row("See https://x.example/d")) }
        rule.onNodeWithText("T").performClick()
        rule.waitUntil(5_000) { refused.get() > 0 } // the first ask was answered "off" before the save
        rule.onNodeWithTag("preview").assertDoesNotExist()
        onIo { app.saveDisplay("link_previews", true) }
        rule.waitUntil(5_000) { rule.onAllNodesWithTagCount("preview") == 1 }
        rule.onNodeWithText("Now on").assertExists()
    }

    @Test fun onlyADataImageIsUsed() {
        assertNull(LinkPreview.picture("https://x.example/p.png"))
        assertNull(LinkPreview.picture("data:image/png,raw")) // not base64
    }

    private fun androidx.compose.ui.test.junit4.ComposeContentTestRule.onAllNodesWithTagCount(tag: String) =
        onAllNodes(androidx.compose.ui.test.hasTestTag(tag)).fetchSemanticsNodes().size

    /** A card asks the server it was opened on, even when another is shown before the fetch runs (#78). */
    @Test fun aCardAsksItsOwnServerAfterASwitch() {
        server.dispatcher = object : okhttp3.mockwebserver.Dispatcher() {
            override fun dispatch(request: okhttp3.mockwebserver.RecordedRequest) = MockResponse().setBody("""{"title":"Mine"}""")
        }
        val mine = app.shown.value
        val b = app.addAccount("https://b.example", "tb")
        app.show(b.id)
        assertEquals("Mine", onIo { LinkPreview.load(mine, "https://x.example/d", "alerts") }?.title)
        app.show(mine.id)
        b.db.close()
    }
}
