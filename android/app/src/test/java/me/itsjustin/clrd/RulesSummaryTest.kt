package me.itsjustin.clrd

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class RulesSummaryTest { // #119, reads as the web's change log does
    private val ctx = ApplicationProvider.getApplicationContext<Context>()

    @Test fun eachRuleInAFewWords() {
        assertEquals("No rules", rulesSummary(ctx, null))
        assertEquals("No rules", rulesSummary(ctx, JSONObject("""{"paused":false,"routes":{}}""")))
        assertEquals("paused, muted", rulesSummary(ctx, JSONObject("""{"paused":true,"muted":true}""")))
        assertEquals("priority 1 to 3", rulesSummary(ctx, JSONObject("""{"priority_max":3}""")))
        assertEquals("priority 2 to 5", rulesSummary(ctx, JSONObject("""{"priority_min":2}""")))
        assertEquals("any other topic to nowhere, ci to a, b",
            rulesSummary(ctx, JSONObject("""{"routes":{"ci":["a","b"],"*":[]}}""")))
    }
}
