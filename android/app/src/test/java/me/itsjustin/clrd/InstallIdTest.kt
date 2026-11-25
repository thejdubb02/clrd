package me.itsjustin.clrd

import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** #769: the install id is made once and kept, so linking again replaces this phone's row. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class InstallIdTest {
    @Test fun madeOnceAndKept() {
        val app = ApplicationProvider.getApplicationContext<App>()
        val first = installId(app)
        assertTrue(first.matches(Regex("[A-Za-z0-9_-]{16,64}"))) // what the server accepts
        assertEquals(first, installId(app))
        assertEquals(first, app.prefs.getString("install_id", null))
        assertTrue(deviceDetail().contains("Android"))
    }
}
