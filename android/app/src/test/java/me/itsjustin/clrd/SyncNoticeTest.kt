package me.itsjustin.clrd

import android.app.Notification
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** #144: the sync notice must not share a group with alerts, or Android bundles alerts under it and shows them twice. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SyncNoticeTest {
    private val app = ApplicationProvider.getApplicationContext<App>()

    @Test fun syncNoticeHasItsOwnGroupAndRealStatus() {
        val n = SyncService.notice(app, SyncClient.Status.CONNECTED)
        assertEquals(Tray.SERVICE_CHANNEL, n.group)
        assertEquals("In sync", n.extras.getCharSequence(Notification.EXTRA_TEXT).toString())
    }
}
