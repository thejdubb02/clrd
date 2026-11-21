package me.itsjustin.clrd

import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.text.DateFormat
import java.util.Date

/** #151: older items show when they happened, and topic chips show how many each topic has. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class DisplayTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val now = 1_800_000_000_000L

    @Test fun recentItemsStayShort() {
        assertEquals("now", ago(app, now - 30_000, now))
        assertEquals("5m", ago(app, now - 5 * 60_000, now))
        assertEquals("23h", ago(app, now - 23 * 3_600_000, now))
    }

    @Test fun olderItemsShowFullDateAndTime() {
        val t = now - 3 * 86_400_000L
        assertEquals(DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(t)), ago(app, t, now))
    }

    @Test fun countsPerTopic() {
        fun n(id: String, topic: String) = Notif(id, topic, "", "", 3, "", null, "[]", Notif.ACTIVE, null, null, 0, 0, 0)
        assertEquals(mapOf("backups" to 2, "ci" to 1), topicCounts(listOf(n("a", "backups"), n("b", "ci"), n("c", "backups"))))
    }

    @Test fun stacksFoldAtTheNewestPlace() {
        fun n(id: String, topic: String) = Notif(id, topic, "", "", 3, "", null, "[]", Notif.ACTIVE, null, null, 0, 0, 0)
        val rows = listOf(n("a", "ci"), n("b", "backups"), n("c", "ci"), n("d", "ci"))
        val keys = { open: Set<String> -> stacked(rows, open).map { if (it is Stack) "[${it.topic} ${it.rows.size}]" else it.key } }
        assertEquals(listOf("[ci 3]", "b"), keys(emptySet()))
        assertEquals(listOf("[ci 3]", "a", "c", "d", "b"), keys(setOf("ci")))
    }
}
