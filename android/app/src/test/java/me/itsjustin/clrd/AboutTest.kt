package me.itsjustin.clrd

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
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

/** #580: About in Settings, What's new from the server or the app's own copy, and the after-update note. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class AboutTest {
    @get:Rule val rule = createComposeRule()
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val server = MockWebServer()
    @Volatile private var version: String? = null

    @Before fun setUp() {
        app.status.value = SyncClient.Status.OFFLINE
        app.prefs.edit().remove("seen_version").remove("whats_new").apply()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.bare == "/api/v1/version" && version != null -> MockResponse().setBody(
                    """{"version":"$version","commit":"0123456789abcdef","date":"2026-11-23T10:00:00Z",
                    "history":[{"version":"$version","date":"2026-11-30","changes":[{"section":"Added","items":["Server: a new thing"]}]}],
                    "full_history":"https://example.com/history"}""",
                )
                request.bare == "/api/v1/version" -> MockResponse().setResponseCode(502)
                else -> MockResponse().setBody("{}")
            }
        }
        server.start()
        app.serverUrl = server.url("/").toString()
        app.token = "tok"
    }

    @After fun tearDown() = server.shutdown()

    @Test fun changelogReadsLikeTheServer() {
        val md = """
            # Changelog

            Text before the first version.

            ## Unreleased

            ### Fixed

            - Android: a fix that wraps
              onto a second line.

            ## 0.2.0 - 2026-12-01

            ### Added

            - One.

            ### Fixed

            - Two.

            ### Added

            - Three, merged into the first Added.

            ## 0.1.0 - 2026-11-23

            ### Security
        """.trimIndent()
        val r = parseChangelog(md)
        assertEquals(listOf("Unreleased", "0.2.0"), r.map { it.version }) // 0.1.0 has no items
        assertEquals(listOf("Fixed" to listOf("Android: a fix that wraps onto a second line.")), r[0].changes)
        assertEquals("2026-12-01", r[1].date)
        assertEquals(listOf("Added" to listOf("One.", "Three, merged into the first Added."), "Fixed" to listOf("Two.")), r[1].changes)
    }

    @Test fun theAppCarriesOnlyReleasedVersions() {
        val h = bundledHistory(app)
        assertTrue(h.isNotEmpty())
        assertFalse(h.any { it.version == "Unreleased" })
        assertTrue(h.any { it.version == "0.1.0" })
    }

    @Test fun versionsCompareAsNumbers() {
        assertTrue(compareVersions("0.9.0", "0.10.0")!! < 0)
        assertTrue(compareVersions("v1.2.0", "1.1.9")!! > 0)
        assertEquals(0, compareVersions("1.2", "1.2.0"))
        assertEquals(0, compareVersions("1.2.0-rc1", "1.2.0"))
        assertNull(compareVersions("dev", "1.0.0"))
        assertEquals(app.getString(R.string.about_server_newer), versionGap(app, "0.1.0", "0.2.0"))
        assertEquals(app.getString(R.string.about_app_newer), versionGap(app, "0.3.0", "0.2.0"))
        assertEquals(app.getString(R.string.about_differ), versionGap(app, "dev", "0.2.0"))
        assertNull(versionGap(app, "0.2.0", "0.2.0"))
        assertNull(versionGap(app, "0.2.0", null))
    }

    @Test fun aServerAnswerWithNoVersionIsNotTrusted() {
        assertNull(parseVersions(JSONObject("{}")))
        assertEquals("https://example.com/h", parseVersions(JSONObject("""{"version":"1.0.0","full_history":"https://example.com/h"}"""))!!.full)
    }

    @Test fun theNoteShowsOnceAfterAnUpdate() {
        assertFalse(updateNoteDue(app, "0.1.0", updated = false)) // fresh install: quiet
        assertFalse(updateNoteDue(app, "0.1.0", updated = true))
        assertTrue(updateNoteDue(app, "0.2.0", updated = true))
        app.seenVersion("0.2.0") // dismissed or opened
        assertFalse(updateNoteDue(app, "0.2.0"))
        app.whatsNew = false
        assertFalse(updateNoteDue(app, "0.3.0", updated = true)) // off: the new version is recorded quietly
        app.whatsNew = true
        assertFalse(updateNoteDue(app, "0.3.0", updated = true))
    }

    @Test fun anUpdateFromABuildBeforeTheNoteShowsIt() {
        assertTrue(updateNoteDue(app, "0.2.0", updated = true)) // nothing recorded, but not a fresh install
    }

    @Test fun theNoteSettingsOutliveRemovingTheServer() {
        assertTrue("whats_new" in App.PHONE_KEYS && "seen_version" in App.PHONE_KEYS)
    }

    private fun about() {
        rule.setContent { SettingsScreen(app, null, 0) {} }
        rule.onNodeWithTag("settings-search").performTextReplacement("what's new")
    }

    @Test fun aboutShowsBothVersionsAndTheServersHistory() {
        version = "99.0.0"
        about()
        rule.waitUntil(5_000) { runCatching { rule.onNodeWithText("Server: clrd 99.0.0 · 0123456 · 2026-11-23").assertExists() }.isSuccess }
        rule.onNodeWithText("This app: clrd ${BuildConfig.VERSION_NAME}").assertExists()
        rule.onNodeWithText("The server runs a newer version than this app.").assertExists()
        rule.onNodeWithText("What's new").performScrollTo().performClick()
        rule.onNodeWithText("\u2022 Server: a new thing").assertExists()
        rule.onAllNodesWithTag("whats-new").assertCountEquals(1)
    }

    @Test fun anUnreachableServerShowsTheAppsOwnCopy() {
        about()
        rule.waitUntil(5_000) { runCatching { rule.onNodeWithText("Server: not reached").assertExists() }.isSuccess }
        rule.onNodeWithText("What's new").performScrollTo().performClick()
        rule.onNodeWithText("The server could not be reached, so this is the copy built into the app.").assertExists()
        rule.onNodeWithText("0.1.0  2026-11-23").assertExists()
    }

    @Test fun aReconnectReadsTheServerAgain() {
        about()
        rule.waitUntil(5_000) { runCatching { rule.onNodeWithText("Server: not reached").assertExists() }.isSuccess }
        version = "99.0.0"
        app.status.value = SyncClient.Status.CONNECTED
        rule.waitUntil(5_000) { runCatching { rule.onNodeWithText("Server: clrd 99.0.0 · 0123456 · 2026-11-23").assertExists() }.isSuccess }
    }

    @Test fun theNoteOpensWhatsNewAndGoesAway() {
        version = "99.0.0"
        app.seenVersion("0.0.1")
        rule.setContent { UpdateNote(app) }
        rule.onNodeWithText("Updated to clrd ${BuildConfig.VERSION_NAME}").assertExists()
        rule.onNodeWithText("What's new").performClick()
        rule.waitUntil(5_000) { runCatching { rule.onNodeWithText("\u2022 Server: a new thing").assertExists() }.isSuccess }
        rule.onNodeWithText("OK").performClick()
        rule.onAllNodesWithTag("update-note").assertCountEquals(0)
        assertEquals(BuildConfig.VERSION_NAME, app.prefs.getString("seen_version", null))
    }
}
