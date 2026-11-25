package me.itsjustin.clrd

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.unit.LayoutDirection
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.util.concurrent.TimeUnit

/** #161: the server's swipe_start and swipe_end choose the buttons each swipe shows, on every device. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SwipesTest {
    @get:Rule val rule = createComposeRule()
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val server = MockWebServer()

    @Before fun setUp() {
        server.start()
        app.serverUrl = server.url("/").toString()
        app.token = "tok"
    }

    @After fun tearDown() = server.shutdown()

    private fun <T> onIo(f: () -> T): T = app.io.submit(f).get(30, TimeUnit.SECONDS)

    @Test fun theDefaultsMatchTheServers() {
        assertEquals(Swipes(listOf("clear", "snooze"), listOf("ack")), Swipes.parse(null))
        assertEquals(Swipes(), app.swipes.value) // nothing from a server yet: out of the box
    }

    @Test fun aSideTheMessageLeavesOutKeepsItsLastValue() {
        val old = Swipes(listOf("ack"), listOf("clear"))
        assertEquals(Swipes(emptyList(), listOf("clear")), Swipes.from(JSONObject("""{"swipe_start":[]}"""), old))
        assertEquals(old, Swipes.from(JSONObject("""{"swipe_start":null}"""), old))
        // A name this build does not know stays in the setting, so a save from here never strips the web's choice.
        assertEquals(listOf("resolve", "pin"), Swipes.from(JSONObject("""{"swipe_end":["resolve","pin"]}"""), old).end)
    }

    @Test fun aTapAddsMovesAndTakesOff() {
        val s = Swipes()
        assertEquals(Swipes(listOf("clear", "snooze", "ack"), emptyList()), s.toggle("ack", toStart = true)) // moved across
        assertEquals(Swipes(listOf("snooze"), listOf("ack")), s.toggle("clear", toStart = true))
        assertEquals(Swipes(listOf("snooze"), listOf("ack", "clear")), s.toggle("clear", toStart = false))
        assertNull(Swipes(listOf("clear", "snooze", "ack"), emptyList()).toggle("resolve", toStart = true)) // 3 at most
        assertNotNull(Swipes(listOf("clear", "snooze", "ack"), emptyList()).toggle("resolve", toStart = false))
    }

    @Test fun aCardShowsOnlyTheButtonsItCanTake() {
        val all = listOf("resolve", "ack", "snooze", "clear", "pin")
        assertEquals(listOf("ack", "snooze", "clear"), Swipes.shown(all, active = true, inbox = true))
        assertEquals(listOf("clear"), Swipes.shown(all, active = false, inbox = false)) // acknowledged, in Snoozed
        assertEquals(emptyList<String>(), Swipes.shown(emptyList(), active = true, inbox = true))
    }

    @Test fun theServersSettingsMessageIsKeptWithoutSnoozeFields() {
        onIo { app.saveSettings(JSONObject("""{"swipe_start":["ack"],"swipe_end":[]}""")) } // no snooze keys in it
        assertEquals(Swipes(listOf("ack"), emptyList()), app.swipes.value)
        onIo { app.saveSettings(JSONObject("""{"autolink":true}""")) } // an older server, or another key changed
        assertEquals(Swipes(listOf("ack"), emptyList()), app.swipes.value)
        assertEquals(Swipes(listOf("ack"), emptyList()), Swipes.parse(app.prefs.getString("swipes", null))) // offline after a restart
    }

    @Test fun aSaveSendsBothSides() {
        server.enqueue(MockResponse().setBody("{}"))
        val next = Swipes(listOf("snooze"), listOf("ack", "clear"))
        assertNull(onIo { app.saveSwipes(next) })
        val r = server.takeRequest(5, TimeUnit.SECONDS)!!
        assertEquals("PUT /api/v1/settings", "${r.method} ${r.bare}")
        val body = JSONObject(r.body.readUtf8())
        assertEquals("""["snooze"]""", body.getJSONArray("swipe_start").toString())
        assertEquals("""["ack","clear"]""", body.getJSONArray("swipe_end").toString())
        assertEquals(next, app.swipes.value)
        assertEquals(next, Swipes.parse(app.prefs.getString("swipes", null))) // kept for a restart before the echo
    }

    @Test fun aLimitedTokenSeesThemReadOnly() {
        onIo { app.saveSettings(JSONObject("""{"swipe_start":["clear"],"swipe_end":["ack"]}""")) }
        server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"forbidden","message":"limited token"}"""))
        assertNull(onIo { app.saveSwipes(Swipes(emptyList(), listOf("ack"))) }) // not an error line
        assertTrue(app.swipesLocked.value)
        assertEquals(Swipes(listOf("clear"), listOf("ack")), app.swipes.value) // back to the server's
        onIo { app.saveServer(app.serverUrl, "other-token") }
        assertFalse(app.swipesLocked.value) // another token may change them
        assertEquals(Swipes(), app.swipes.value) // and the old server's choice is not the new one's
    }

    @Test fun aRefusedSaveSaysWhyAndGoesBack() {
        server.enqueue(MockResponse().setResponseCode(400).setBody("""{"error":"swipe_end: bad"}"""))
        val err = onIo { app.saveSwipes(Swipes(emptyList(), listOf("ack", "clear"))) }
        assertEquals("HTTP 400: swipe_end: bad", err) // the server's reason, not "no connection"
        assertFalse(app.swipesLocked.value)
        assertEquals(Swipes(), app.swipes.value)
    }

    @Test fun anOldServersMessageAfterASwitchIsDropped() { // #568
        val old = RoomStore(app) // the old server's sync client, its settings message already in hand
        onIo { app.saveServer(app.serverUrl, "other-token") }
        onIo { old.settings(JSONObject("""{"swipe_start":["ack"],"swipe_end":[],"snooze_intervals":["7m"],"snooze_default":"7m"}""")) }
        assertEquals(Swipes(), app.swipes.value)
        assertNull(app.prefs.getString("swipes", null))
        assertEquals(Snooze(), app.snooze.value) // snooze shares the path
        onIo { RoomStore(app).settings(JSONObject("""{"swipe_start":["ack"],"swipe_end":[]}""")) } // the new server's
        assertEquals(Swipes(listOf("ack"), emptyList()), app.swipes.value)
    }

    @Test fun aFailedSaveGoesBackToAMessageThatLandedDuringIt() { // #568
        server.enqueue(MockResponse().setResponseCode(500).setBody("{}").setBodyDelay(1, TimeUnit.SECONDS))
        val save = app.io.submit<String?> { app.saveSwipes(Swipes(emptyList(), listOf("clear"))) }
        server.takeRequest(5, TimeUnit.SECONDS)
        RoomStore(app).settings(JSONObject("""{"swipe_start":["resolve"],"swipe_end":["ack"]}""")) // another device, while the save waits
        assertNotNull(save.get(30, TimeUnit.SECONDS))
        assertEquals(Swipes(listOf("resolve"), listOf("ack")), app.swipes.value)
    }

    @Test fun theLabelsFollowTheGesturesInRightToLeft() { // #568: a start swipe goes right there
        rule.setContent { CompositionLocalProvider(LocalLayoutDirection provides LayoutDirection.Rtl) { SettingsScreen(app, null, 0) {} } }
        rule.onNodeWithTag("settings-search").performTextReplacement("swipe")
        fun top(id: Int) = rule.onNodeWithText(app.getString(id)).fetchSemanticsNode().boundsInRoot.top
        assertTrue(top(R.string.swipe_right) < top(R.string.swipe_left)) // the start side is listed first
    }
}
