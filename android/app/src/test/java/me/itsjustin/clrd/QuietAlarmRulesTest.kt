package me.itsjustin.clrd

import android.content.Context
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextReplacement
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.util.concurrent.TimeUnit

/** #119 part 3: the quiet alarm's two fields on a sender's rules, each saved on its own. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class QuietAlarmRulesTest {
    @get:Rule val rule = createComposeRule()
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val server = MockWebServer()
    private var changed = 0

    @Before fun setUp() {
        server.start()
        app.serverUrl = server.url("/").toString()
        app.token = "tok"
    }

    @After fun tearDown() = server.shutdown()

    private fun show(sender: String) = rule.setContent {
        Column(Modifier.verticalScroll(rememberScrollState())) { SenderRules(app, JSONObject(sender)) { changed++ } }
    }

    @Test fun eachFieldSendsOnlyItself() {
        server.enqueue(MockResponse().setBody("{}"))
        show("""{"id":"s1","rules":{"paused":true,"alert_topic":"ops"}}""")
        rule.onNodeWithText("Save quiet alarm").performScrollTo().assertIsNotEnabled() // nothing typed yet
        rule.onNodeWithTag("rule-expect").performScrollTo().performTextReplacement(" 25h ")
        rule.onNodeWithText("Save quiet alarm").performScrollTo().performClick()
        val r = server.takeRequest(5, TimeUnit.SECONDS)!!
        assertEquals("PUT /api/v1/senders/s1", "${r.method} ${r.path}")
        assertEquals("""{"expect":"25h"}""", r.body.readUtf8()) // not paused, not the alarm topic
        rule.waitUntil(5000) { changed == 1 }
    }

    @Test fun anEmptiedTopicTurnsItBackToTheLastTopic() {
        server.enqueue(MockResponse().setBody("{}"))
        show("""{"id":"s1","rules":{"expect":"25h","alert_topic":"ops"}}""")
        rule.onNodeWithTag("rule-alert_topic").performScrollTo().performTextReplacement("")
        rule.onNodeWithText("Save alarm topic").performScrollTo().performClick()
        assertEquals("""{"alert_topic":""}""", server.takeRequest(5, TimeUnit.SECONDS)!!.body.readUtf8())
    }

    @Test fun aRefusalShowsTheServersReasonAndKeepsTheText() {
        server.enqueue(MockResponse().setResponseCode(400).setBody("""{"error":"expect: must be 1m to 8784h"}"""))
        show("""{"id":"s1","rules":{}}""")
        rule.onNodeWithTag("rule-expect").performScrollTo().performTextReplacement("30s")
        rule.onNodeWithText("Save quiet alarm").performScrollTo().performClick()
        server.takeRequest(5, TimeUnit.SECONDS)!!
        rule.waitUntil(5000) { rule.onAllNodesWithText("expect: must be 1m to 8784h", substring = true).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("Could not change it: HTTP 400: expect: must be 1m to 8784h").performScrollTo()
        rule.onNodeWithText("30s").performScrollTo() // still there to fix
        assertEquals(0, changed)
    }

    @Test fun anOpenAlarmShows() {
        show("""{"id":"s1","alarm":"ops","rules":{"expect":"25h"}}""")
        rule.onNodeWithText("Quiet alarm open in ops").performScrollTo()
    }

    @Test fun theSummaryNamesTheAlarm() {
        val ctx = ApplicationProvider.getApplicationContext<Context>()
        assertEquals("muted, alarm if quiet for 25h, alarm to ops",
            rulesSummary(ctx, JSONObject("""{"muted":true,"expect":"25h","alert_topic":"ops"}""")))
        assertEquals("No rules", rulesSummary(ctx, JSONObject("""{"expect":"","alert_topic":""}"""))) // off
    }
}
