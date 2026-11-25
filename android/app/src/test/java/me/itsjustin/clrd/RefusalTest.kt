package me.itsjustin.clrd

import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** #767: a phone that is not allowed to change a device setting is told so in plain words. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class RefusalTest {
    private val app = ApplicationProvider.getApplicationContext<App>()

    @Test fun notAllowedSaysWhoCan() =
        assertEquals(app.getString(R.string.device_not_allowed), refusal(app, HttpError(403, "only an allowed device can change where clears apply", reason = "approve_denied")))

    @Test fun anythingElseKeepsTheServersWords() =
        assertEquals("Not changed: HTTP 404: no such device", refusal(app, HttpError(404, "no such device", reason = "not_found")))
}
