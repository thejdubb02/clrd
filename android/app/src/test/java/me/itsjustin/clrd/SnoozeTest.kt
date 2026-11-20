package me.itsjustin.clrd

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class SnoozeTest {
    @Test fun labelsMatchTheWeb() {
        assertEquals("1 hour", snoozeLabel("1h"))
        assertEquals("2 days", snoozeLabel("48h"))
        assertEquals("90 min", snoozeLabel("90m"))
        assertEquals("90 min", snoozeLabel("1h30m"))
        assertEquals("1.5h", snoozeLabel("1.5h")) // anything else as typed
    }

    @Test fun typedTimesSendDaysAsHours() {
        assertEquals(listOf("15m", "48h", "4h"), Snooze.parse(" 15m, 2d,,4h "))
        assertEquals(listOf("99999999999999999999d"), Snooze.parse("99999999999999999999d")) // too big: left for the server to refuse
    }

    @Test fun aDefaultOutsideTheListIsIgnored() {
        assertNull(Snooze.from(JSONObject("""{"snooze_intervals":["1h"],"snooze_default":"4h"}""")))
        assertNull(Snooze.from(JSONObject("""{"render_formatting":true}""")))
        assertEquals("""{"for":"4h"}""", Snooze.body("4h"))
    }
}
