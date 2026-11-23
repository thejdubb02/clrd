package me.itsjustin.clrd

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextReplacement
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
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
import java.util.Collections

/** #617: topic subscriptions from the settings message, and the Topics page in Settings (SPEC 2.3). */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SubsTest {
    @get:Rule val rule = createComposeRule()
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val server = MockWebServer()
    private var listCode = 200
    @Volatile private var slow = false
    private val puts = Collections.synchronizedList(mutableListOf<String>())
    private val lists = Collections.synchronizedList(mutableListOf<String>())
    private val list = """{"filtered":true,"new_topics":true,"topics":[
        {"topic":"agents","subscribed":true,"open":1,"last_at":"2026-11-20T10:00:00Z"},
        {"topic":"chat","subscribed":true,"open":3,"last_at":"2026-11-20T10:00:00Z"},
        {"topic":"deploys","subscribed":false,"open":0,"last_at":null}]}"""

    @Before fun setUp() {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.path == "/api/v1/subscriptions" && request.method == "PUT" -> {
                    puts += request.body.readUtf8(); MockResponse().setBody(list).apply { if (slow) setBodyDelay(1, java.util.concurrent.TimeUnit.SECONDS) }
                }
                request.path == "/api/v1/subscriptions" -> MockResponse().setResponseCode(listCode).setBody(if (listCode == 200) list else """{"error":"not found"}""")
                request.path!!.startsWith("/api/v1/notifications?") -> {
                    lists += request.path!!
                    MockResponse().setBody("""{"seq":9,"epoch":"e","notifications":[{"id":"d1","topic":"deploys","title":"Deploy done","message":"web 4.2","seq":7}]}""")
                }
                else -> MockResponse().setBody("{}")
            }
        }
        server.start()
        app.serverUrl = server.url("/").toString()
        app.token = "tok"
    }

    @After fun tearDown() = server.shutdown()

    private fun settings(subs: String?) = app.saveSettings(JSONObject(if (subs == null) "{}" else """{"subscriptions":$subs}"""))
    private fun show() = rule.setContent { SettingsScreen(app, null, 0) {} }
    private fun search(text: String) = rule.onNodeWithTag("settings-search").performTextReplacement(text)
    private fun waitFor(tag: String) = rule.waitUntil(5000) { rule.onAllNodesWithTag(tag).fetchSemanticsNodes().isNotEmpty() }

    @Test fun parseRules() {
        assertNull(Subs.of(null)) // an older server: no Topics page
        val s = Subs.of(JSONObject("""{"new_topics":false,"topics":{"chat":true,"ops":false}}"""))!!
        assertTrue(s.filtered) // missing reads as true
        assertTrue(s.subscribed("chat"))
        assertFalse(s.subscribed("ops"))
        assertFalse(s.subscribed("never-seen")) // follows new_topics
        assertFalse(Subs.of(JSONObject("""{"filtered":false}"""))!!.filtered)
        val l = SubsList.from(JSONObject(list))
        assertEquals(listOf("agents", "chat", "deploys"), l.topics.map { it.topic })
        assertNull(l.topics[2].lastAt)
        assertEquals("""{"topics":{"chat":false}}""", topicBody("chat", false).toString())
    }

    @Test fun settingsMessageSetsAndClears() {
        settings("""{"filtered":true,"topics":{"chat":false}}""")
        assertEquals(false, app.subs.value?.subscribed("chat"))
        settings(null) // a whole settings view without the object
        assertNull(app.subs.value)
    }

    @Test fun olderServerHasNoTopicsPage() {
        settings(null)
        show()
        search("new topics")
        rule.onNodeWithText("No settings match \"new topics\"").assertExists()
    }

    @Test fun unfilteredTokenSaysWhy() {
        settings("""{"filtered":false}""")
        show()
        search("subscribe")
        rule.onNodeWithText(app.getString(R.string.subs_unfiltered)).assertExists()
        rule.onAllNodesWithTag("subs-new").assertCountEquals(0)
    }

    @Test fun switchSendsOnlyThatTopic() {
        settings("""{"filtered":true,"topics":{"deploys":false}}""")
        show()
        search("subscribe")
        waitFor("sub-chat")
        rule.onNodeWithTag("sub-deploys").performScrollTo().assertIsOff() // an unsubscribed topic can be turned back on
        rule.onNodeWithTag("sub-chat").performScrollTo().assertIsOn().performClick()
        rule.waitUntil(5000) { puts.isNotEmpty() }
        assertEquals(listOf("""{"topics":{"chat":false}}"""), puts.toList())
    }

    @Test fun secondTapOnASlowConnectionSendsTheOtherValue() {
        slow = true
        settings("""{"filtered":true}""")
        show()
        search("subscribe")
        waitFor("sub-chat")
        rule.onNodeWithTag("sub-chat").performScrollTo().performClick()
        rule.onNodeWithTag("sub-chat").assertIsOff() // before the server answers
        rule.onNodeWithTag("sub-chat").performClick()
        rule.waitUntil(10000) { puts.size == 2 }
        assertEquals(listOf("""{"topics":{"chat":false}}""", """{"topics":{"chat":true}}"""), puts.toList())
    }

    @Test fun readAnUnsubscribedTopicWithoutKeepingIt() { // #624
        settings("""{"filtered":true,"topics":{"deploys":false}}""")
        show()
        search("subscribe")
        waitFor("read-deploys")
        rule.onAllNodesWithTag("read-chat").assertCountEquals(0) // only an unsubscribed topic has Read
        rule.onNodeWithTag("read-deploys").performScrollTo().performClick()
        rule.waitUntil(5000) { rule.onAllNodesWithText("Deploy done").fetchSemanticsNodes().isNotEmpty() }
        assertTrue(lists.single(), lists.single().contains("&subscribed=all&topic=deploys"))
        val db = app.db.dao()
        assertTrue(app.io.submit<List<Notif>> { db.openNow() }.get().isEmpty()) // nothing saved, so nothing in the tray
        assertNull(org.robolectric.Shadows.shadowOf(app.getSystemService(android.app.NotificationManager::class.java)).getNotification("d1", 0))
        rule.onNodeWithTag("reader-subscribe").performClick()
        rule.waitUntil(5000) { puts.isNotEmpty() }
        assertEquals("""{"topics":{"deploys":true}}""", puts.single())
    }

    @Test fun readerClosesWhenAnotherDeviceSubscribes() { // #624 review
        settings("""{"filtered":true,"topics":{"deploys":false}}""")
        show()
        search("subscribe")
        waitFor("read-deploys")
        rule.onNodeWithTag("read-deploys").performScrollTo().performClick()
        waitFor("reader")
        rule.runOnIdle { settings("""{"filtered":true,"topics":{"deploys":true}}""") } // the settings message from that change
        rule.waitUntil(5000) { rule.onAllNodesWithTag("reader").fetchSemanticsNodes().isEmpty() }
    }

    @Test fun agentsAsksFirst() {
        settings("""{"filtered":true}""")
        show()
        search("subscribe")
        waitFor("sub-agents")
        rule.onNodeWithTag("sub-agents").performScrollTo().performClick()
        rule.onNodeWithText(app.getString(R.string.subs_agents_title, "agents")).assertExists()
        Thread.sleep(300)
        assertTrue(puts.isEmpty()) // nothing sent before the answer
        rule.onNodeWithText(app.getString(R.string.unsubscribe)).performClick()
        rule.waitUntil(5000) { puts.isNotEmpty() }
        assertEquals("""{"topics":{"agents":false}}""", puts.single())
    }

    @Test fun notFoundHidesThePage() {
        listCode = 404
        settings("""{"filtered":true}""")
        show()
        search("new topics")
        rule.waitUntil(5000) { rule.onAllNodesWithText("No settings match \"new topics\"").fetchSemanticsNodes().isNotEmpty() }
    }

    @Test fun snoozeEditsKeptWhileSearchHidesThem() { // #575 review
        show()
        search("snooze")
        rule.onNodeWithText(app.getString(R.string.snooze_times)).performScrollTo().performTextReplacement("7m, 3h")
        search("qr") // hides the snooze block
        rule.onAllNodesWithText(app.getString(R.string.snooze_times)).assertCountEquals(0)
        search("snooze")
        rule.onNodeWithText("7m, 3h").assertExists()
    }
}
