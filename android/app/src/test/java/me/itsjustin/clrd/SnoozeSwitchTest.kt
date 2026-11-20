package me.itsjustin.clrd

import androidx.test.core.app.ApplicationProvider
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** #222: another server's snooze times never carry over a switch in Settings. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SnoozeSwitchTest {
    private val app = ApplicationProvider.getApplicationContext<App>()

    @Test fun switchingServerForgetsTheOldSnoozeTimes() {
        app.saveSettings(JSONObject("""{"snooze_intervals":["15m","2h"],"snooze_default":"2h"}"""))
        assertEquals("2h", app.snooze.value.default)
        val t = Thread { app.saveServer("https://b.example", "tok") }.apply { start() } // Room refuses the main thread
        t.join(60_000)
        assertEquals(Snooze(), app.snooze.value)
        assertEquals(null, app.prefs.getString("snooze", null))
    }
}
