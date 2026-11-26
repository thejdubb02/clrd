package me.itsjustin.clrd

import android.Manifest
import android.app.Notification
import android.app.NotificationManager
import android.os.Looper
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

/** #516: what a refused typed answer leaves behind, for the tray and for a reply box still open. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class ReplyOutcomeTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val nm = app.getSystemService(NotificationManager::class.java)
    private val typed = Pending(notifId = "a", action = "reply", body = """{"reply":"note","text":"Back at 5","seq":1}""")

    @Before fun setUp() {
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        app.serverUrl = "https://a.example"
        app.token = "" // not configured, so nothing here touches the network
    }

    private fun row(seq: Long) = Notif("a", "t", "T", "m", 3, "", null, "[]", Notif.ACTIVE, null, null, seq, System.currentTimeMillis(), seq)
    private fun subText(): String? = shadowOf(nm).getNotification("a", 0)?.extras?.getCharSequence(Notification.EXTRA_SUB_TEXT)?.toString()

    @Test fun aRefusedTrayAnswerPointsToTheAppBox() { // a tray reply cannot be prefilled, so the tray says where the text is
        val store = RoomStore(app)
        bg { store.apply(listOf(row(1)), 1) }
        assertEquals("t", subText())
        bg { store.replyOutcome(typed, "bad_request") }
        bg { store.apply(listOf(row(2)), 2) } // the server's row, fetched after the refusal
        assertEquals("Back at 5", app.drafts["a"])
        assertEquals(app.getString(R.string.tray_draft), subText())
    }

    @Test fun aChangedQuestionTellsAnOpenBox() { // the box closes when this count moves
        val store = RoomStore(app)
        app.drafts["a"] = "older"
        bg { store.replyOutcome(typed, "conflict") }
        shadowOf(Looper.getMainLooper()).idle()
        assertEquals(1, app.account.changed["a"])
        assertNull(app.drafts["a"])
        bg { store.replyOutcome(typed, "bad_request") }
        shadowOf(Looper.getMainLooper()).idle()
        assertEquals("a refusal is not a changed question", 1, app.account.changed["a"])
    }
}
