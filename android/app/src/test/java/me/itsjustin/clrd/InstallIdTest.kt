package me.itsjustin.clrd

import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** #769: the install id is made once and kept, one per server, so linking again replaces this phone's row. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class InstallIdTest {
    @Test fun madeOnceAndKept() {
        val app = ApplicationProvider.getApplicationContext<App>()
        val first = installId(app, "https://a.example/")
        assertTrue(first.matches(Regex("[A-Za-z0-9_-]{16,64}"))) // what the server accepts
        assertEquals(first, installId(app, "https://a.example")) // kept, and a trailing slash is the same server
        assertNotEquals(first, installId(app, "https://b.example")) // two servers cannot match up the phone
        assertTrue(deviceDetail().contains("Android"))
    }
}
