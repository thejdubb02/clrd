package me.itsjustin.clrd

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.junit4.createComposeRule
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
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** #575: Settings in the SPEC 9.1 sections, with search across label, description and keywords. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SettingsSearchTest {
    @get:Rule val rule = createComposeRule()
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val server = MockWebServer()
    private var admin = false
    @Volatile private var sendersAsked = false
    private var reads = true
    @Volatile private var settingsAsked = false

    @Before fun setUp() {
        app.status.value = SyncClient.Status.OFFLINE
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.bare == "/api/v1/settings" && !reads -> { settingsAsked = true; MockResponse().setResponseCode(403).setBody("""{"error":"forbidden"}""") }
                request.bare == "/api/v1/senders" && !admin -> { sendersAsked = true; MockResponse().setResponseCode(403).setBody("""{"error":"forbidden"}""") }
                request.bare == "/api/v1/senders" -> MockResponse().setBody("""{"senders":[]}""")
                else -> MockResponse().setBody("{}")
            }
        }
        server.start()
        app.serverUrl = server.url("/").toString()
        app.token = "tok"
    }

    @After fun tearDown() = server.shutdown()

    private fun show() = rule.setContent { SettingsScreen(app, null, 0) {} }
    private fun search(text: String) = rule.onNodeWithTag("settings-search").performTextReplacement(text)

    @Test fun sectionsInSpecOrder() {
        assertEquals(
            listOf(R.string.section_notifications, R.string.section_inbox, R.string.section_devices, R.string.section_integrations),
            SETTINGS.map { it.section }.distinct(),
        )
        assertEquals(3, SETTINGS.map { it.section }.zipWithNext().count { (a, b) -> a != b }) // each section in one run
    }

    @Test fun matchRule() {
        assertTrue(settingMatches("  QR ", "Link a phone", null, listOf("qr", "scan")))
        assertFalse(settingMatches("qr", "Sound", "What this phone plays", listOf("chime")))
        assertTrue(settingMatches("plays", "Sound", "What this phone plays", emptyList()))
        assertTrue(settingMatches("   ", "Sound", null, emptyList()))
    }

    @Test fun keywordFindsSettingInAnotherSection() {
        show()
        rule.onNodeWithText("Sound").assertExists()
        search("qr") // a keyword of Link a phone, under Devices
        rule.onNodeWithText("Link a phone").assertExists()
        rule.onNodeWithText("DEVICES").assertExists()
        rule.onAllNodesWithText("NOTIFICATIONS").assertCountEquals(0)
        rule.onAllNodesWithText("INBOX").assertCountEquals(0)
        rule.onAllNodesWithText("Sound").assertCountEquals(0)
        rule.onAllNodesWithText("Server").assertCountEquals(0)
    }

    @Test fun nothingMatchesShowsEmptyStateAndClears() {
        show()
        search(" zzz ")
        rule.onNodeWithText("No settings match \"zzz\"").assertExists()
        rule.onNodeWithText("Clear search").performScrollTo().performClick()
        rule.onNodeWithText("Sound").assertExists()
        rule.onAllNodesWithText("No settings match \"zzz\"").assertCountEquals(0)
    }

    @Test fun sendersOnlyForAFullAdmin() {
        show()
        search("sender")
        rule.waitUntil(5000) { sendersAsked }
        // Give the 403 time to land; the section must never appear.
        val appeared = runCatching { rule.waitUntil(2000) { rule.onAllNodesWithText("INTEGRATIONS").fetchSemanticsNodes().isNotEmpty() } }.isSuccess
        assertFalse(appeared)
        rule.onNodeWithText("No settings match \"sender\"").assertExists() // left out, not greyed
    }

    @Test fun sendersShowForAFullAdmin() {
        admin = true
        show()
        search("sender")
        rule.waitUntil(5000) { rule.onAllNodesWithText("INTEGRATIONS").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("Senders").assertExists()
    }

    @Test fun snoozeAndDevicesWaitForATokenThatReads() { // #604: a publish-only token
        reads = false
        show()
        search("postpone") // a keyword of Snooze times only
        rule.waitUntil(5000) { settingsAsked }
        val appeared = runCatching { rule.waitUntil(2000) { rule.onAllNodesWithText("Snooze times").fetchSemanticsNodes().isNotEmpty() } }.isSuccess
        assertFalse(appeared)
        search("unlink") // a keyword of Linked devices only
        rule.onNodeWithText("No settings match \"unlink\"").assertExists()
    }

    @Test fun snoozeShowsOnceTheServerTakesTheToken() {
        show()
        search("postpone")
        rule.waitUntil(5000) { rule.onAllNodesWithText("Snooze times").fetchSemanticsNodes().isNotEmpty() }
    }

    @Test fun aRejectedTokenHidesSendersAtOnce() { // #604: not only after the next fetch
        admin = true
        show()
        search("sender")
        rule.waitUntil(5000) { rule.onAllNodesWithText("Senders").fetchSemanticsNodes().isNotEmpty() }
        app.status.value = SyncClient.Status.AUTH_FAILED
        rule.waitForIdle()
        rule.onAllNodesWithText("Senders").assertCountEquals(0)
    }

    @Test fun aLiveStreamShowsSnoozeAfterAnOfflineStart() { // #604 review: the probe failed, the stream came back
        server.shutdown() // the probe cannot reach the server
        show()
        search("postpone")
        rule.waitForIdle()
        rule.onAllNodesWithText("Snooze times").assertCountEquals(0)
        app.status.value = SyncClient.Status.CONNECTED
        rule.waitUntil(5000) { rule.onAllNodesWithText("Snooze times").fetchSemanticsNodes().isNotEmpty() }
        app.status.value = SyncClient.Status.AUTH_FAILED
        rule.waitForIdle()
        rule.onAllNodesWithText("Snooze times").assertCountEquals(0)
    }
}
