package me.itsjustin.clrd

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import com.github.takahirom.roborazzi.captureRoboImage
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.After
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.util.Locale
import java.util.TimeZone

/**
 * JVM screenshot tests for the main Android screens, light and dark (#658).
 *
 * Record/refresh PNGs in android/screenshots/:
 *   ./gradlew recordRoborazziDebug --tests "me.itsjustin.clrd.ScreensTest"
 *
 * Verify against committed PNGs (runs in CI):
 *   ./gradlew verifyRoborazziDebug
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], qualifiers = "w390dp-h844dp-xhdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ScreensTest {
    @get:Rule val rule = createComposeRule()

    private val app = ApplicationProvider.getApplicationContext<App>()
    private val server = MockWebServer()
    private lateinit var prevLocale: Locale
    private lateinit var prevZone: TimeZone

    @Before fun setUp() {
        prevLocale = Locale.getDefault()
        prevZone = TimeZone.getDefault()
        Locale.setDefault(Locale.US)
        TimeZone.setDefault(TimeZone.getTimeZone("UTC"))

        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when (request.bare) {
                "/api/v1/settings" -> MockResponse().setBody("""{"sound":"chime"}""")
                "/api/v1/devices" -> MockResponse().setBody("""{"devices":[]}""")
                "/api/v1/devices/log" -> MockResponse().setBody("""{"events":[]}""")
                "/api/v1/senders" -> MockResponse().setBody("""{"senders":[]}""")
                else -> MockResponse().setBody("{}")
            }
        }
        server.start()

        app.serverUrl = server.url("/").toString()
        app.token = "clrd_demo_token"
        app.stack = false
        app.status.value = SyncClient.Status.CONNECTED
        app.soundNow.value = "chime"
        app.subs.value = null

        // Offset by 30 s past each minute mark so ago() prints the same age even across a slow frame.
        val now = System.currentTimeMillis()
        val demo = listOf(
            Notif(
                id = "demo-1",
                topic = "backups",
                title = "Nightly backup failed",
                message = "restic exited 1 after 4m 12s: repository is locked by another process.",
                priority = 5,
                tags = "restic,nas",
                click = null,
                actions = "[]",
                state = Notif.ACTIVE,
                stateBy = null,
                snoozeUntil = null,
                createdAt = now - 2 * 60_000L - 30_000L,
                updatedAt = now - 2 * 60_000L - 30_000L,
                seq = 4,
            ),
            Notif(
                id = "demo-2",
                topic = "agents",
                title = "Merge pull request 212?",
                message = "All 48 checks pass on **thejdubb02/clrd**. The agent is waiting for your answer.",
                priority = 4,
                tags = "",
                click = null,
                actions = """[{"label":"Approve","reply":"yes"},{"label":"Reject","reply":"no"}]""",
                state = Notif.ACTIVE,
                stateBy = null,
                snoozeUntil = null,
                createdAt = now - 5 * 60_000L - 30_000L,
                updatedAt = now - 5 * 60_000L - 30_000L,
                seq = 3,
            ),
            Notif(
                id = "demo-3",
                topic = "infra",
                title = "Disk 91% on web-1",
                message = "/var is filling at 2 GB an hour.",
                priority = 4,
                tags = "disk",
                click = null,
                actions = "[]",
                state = Notif.ACTIVE,
                stateBy = null,
                snoozeUntil = null,
                createdAt = now - 12 * 60_000L - 30_000L,
                updatedAt = now - 12 * 60_000L - 30_000L,
                seq = 2,
            ),
            Notif(
                id = "demo-4",
                topic = "infra",
                title = "Load high on db-1",
                message = "load average 14 for 10 minutes",
                priority = 3,
                tags = "",
                click = null,
                actions = "[]",
                state = Notif.ACKNOWLEDGED,
                stateBy = "pixel-9",
                snoozeUntil = null,
                createdAt = now - 28 * 60_000L - 30_000L,
                updatedAt = now - 28 * 60_000L - 30_000L,
                seq = 1,
            ),
        )
        bg {
            app.db.dao().forget()
            app.db.dao().replaceAll(demo, 4L, sameDb = false)
        }
    }

    @After fun tearDown() {
        server.shutdown()
        Locale.setDefault(prevLocale)
        TimeZone.setDefault(prevZone)
    }

    private fun renderHome(appearance: String) {
        app.pickAppearance(appearance)
        rule.setContent {
            ClrdTheme(appearance) {
                Home(app, battery = null, resumes = 0, scan = {})
            }
        }
        rule.waitUntil(5_000) {
            rule.onAllNodesWithText("Nightly backup failed").fetchSemanticsNodes().isNotEmpty()
        }
        rule.waitForIdle()
    }

    private fun captureInbox(appearance: String) {
        renderHome(appearance)
        rule.onRoot().captureRoboImage("inbox-$appearance.png")
    }

    private fun captureCardOpen(appearance: String) {
        renderHome(appearance)
        rule.onNodeWithText("Merge pull request 212?").performClick()
        rule.waitUntil(5_000) {
            rule.onAllNodesWithText("Approve").fetchSemanticsNodes().isNotEmpty()
        }
        rule.waitForIdle()
        rule.onRoot().captureRoboImage("card-open-$appearance.png")
    }

    private fun captureSettings(appearance: String) {
        renderHome(appearance)
        rule.onNodeWithText("Settings").performClick()
        rule.waitUntil(5_000) {
            rule.onAllNodesWithText("NOTIFICATIONS").fetchSemanticsNodes().isNotEmpty()
        }
        rule.waitForIdle()
        rule.onRoot().captureRoboImage("settings-$appearance.png")
    }

    @Test fun inboxLight() = captureInbox("light")

    @Test fun inboxDark() = captureInbox("dark")

    @Test fun cardOpenLight() = captureCardOpen("light")

    @Test fun cardOpenDark() = captureCardOpen("dark")

    @Test fun settingsLight() = captureSettings("light")

    @Test fun settingsDark() = captureSettings("dark")
}
