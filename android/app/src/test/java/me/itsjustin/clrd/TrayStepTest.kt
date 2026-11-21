package me.itsjustin.clrd

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Event JSON in, tray step out. The cancel cases are the headline feature. */
class TrayStepTest {
    private fun event(state: String, topic: String = "backups", actions: String = "[]", reply: String = "") = Notif.fromJson(
        JSONObject(
            """{"id":"n1","topic":"$topic","title":"Backup failed","message":"disk full","priority":4,
               "tags":["warning","disk"],"click":null,"actions":$actions,"state":"$state","state_by":"phone",
               "snooze_until":null,"reply":"$reply","created_at":"2026-11-19T10:00:00Z","updated_at":"2026-11-19T10:05:00Z","seq":7}""",
        ),
    )

    @Test fun activeShowsLoud() = assertEquals(TrayStep.Show(event("active"), silent = false), TrayStep.plan(event("active"), emptySet()))

    @Test fun acknowledgedStaysQuietly() = assertEquals(TrayStep.Show(event("acknowledged"), silent = true), TrayStep.plan(event("acknowledged"), emptySet()))

    @Test fun dismissedElsewhereCancels() = assertEquals(TrayStep.Cancel("n1"), TrayStep.plan(event("dismissed"), emptySet()))

    @Test fun resolvedCancels() = assertEquals(TrayStep.Cancel("n1"), TrayStep.plan(event("resolved"), emptySet()))

    @Test fun snoozedCancels() = assertEquals(TrayStep.Cancel("n1"), TrayStep.plan(event("snoozed"), emptySet()))

    /** #328: the server's silenced flag is read; a missing one (older event) means not silenced. */
    @Test fun readsSilenced() {
        assertTrue(Notif.fromJson(JSONObject("""{"id":"n1","state":"active","silenced":true}""")).silenced)
        assertEquals(false, Notif.fromJson(JSONObject("""{"id":"n1","state":"active"}""")).silenced)
    }

    @Test fun mutedTopicNeverShows() = assertEquals(TrayStep.Cancel("n1"), TrayStep.plan(event("active", "noisy"), setOf("noisy")))

    @Test fun parsesEveryField() {
        val n = event("acknowledged")
        assertEquals("n1", n.id)
        assertEquals("backups", n.topic)
        assertEquals("Backup failed", n.title)
        assertEquals("disk full", n.message)
        assertEquals(4, n.priority)
        assertEquals("warning,disk", n.tags)
        assertNull(n.click) // JSON null must not become the string "null"
        assertNull(n.snoozeUntil)
        assertEquals("phone", n.stateBy)
        assertEquals(1795082400000L, n.createdAt)
        assertEquals(1795082700000L, n.updatedAt)
        assertEquals(7L, n.seq)
        assertEquals("", n.reply)
        assertTrue(!n.isFinal)
    }

    private val ours = mapOf(R.string.tray_ack to "Ack", R.string.tray_snooze to "Snooze", R.string.tray_clear to "Clear", R.string.tray_open to "Open")
    private fun TrayButton.text() = if (res != 0) ours.getValue(res) else label
    private fun labels(state: String, actions: String = "[]") = TrayButton.pick(event(state, actions = actions)).map { it.text() }
    private fun links(n: Int) = (1..n).joinToString(",", "[", "]") { """{"label":"L$it","url":"https://x.example/$it"}""" }

    @Test fun noSenderLinksKeepsOurThree() = assertEquals(listOf("Ack", "Snooze", "Clear"), labels("active"))

    @Test fun ackedHasNoAck() = assertEquals(listOf("Snooze", "Clear"), labels("acknowledged"))

    @Test fun oneLinkDropsSnooze() = assertEquals(listOf("L1", "Ack", "Clear"), labels("active", links(1)))

    @Test fun twoLinksAtMostThenAck() = assertEquals(listOf("L1", "L2", "Ack"), labels("active", links(3)))

    @Test fun twoLinksOnAnAckedOneThenClear() = assertEquals(listOf("L1", "L2", "Clear"), labels("acknowledged", links(2)))

    @Test fun onlyWebLinksBecomeButtons() {
        val actions = """[{"label":"Bad","url":"intent://x#Intent;end"},{"label":"","url":"HTTPS://x.example"},{"label":"No url"},{"label":"Huge","url":"https://x.example/${"a".repeat(2048)}"}]"""
        val b = TrayButton.pick(event("active", actions = actions))
        assertEquals(listOf("Open", "Ack", "Clear"), b.map { it.text() })
        assertEquals("https://x.example", b[0].url)
    }

    private val yesNo = """[{"label":"Yes","reply":"yes"},{"label":"","reply":"no"},{"label":"L1","url":"https://x.example/1"}]"""

    @Test fun repliesComeFirst() {
        val b = TrayButton.pick(event("active", actions = yesNo))
        assertEquals(listOf("Yes", "no", "L1"), b.map { it.text() }) // an empty label shows the reply itself
        assertEquals(listOf("yes", "no", null), b.map { it.reply })
    }

    @Test fun threeRepliesAtMost() {
        val four = (1..4).joinToString(",", "[", "]") { """{"label":"R$it","reply":"r$it"}""" }
        assertEquals(listOf("R1", "R2", "R3"), labels("active", four))
    }

    /** #366: an answer approves the whole text, so a text the tray may cut is answered in the app, where it shows in full. */
    @Test fun aTextTooLongForTheTrayIsAnsweredInTheApp() {
        val short = event("active", actions = yesNo)
        val long = short.copy(message = "x".repeat(TrayButton.TRAY_CHARS + 1))
        val tall = short.copy(message = "-a\n+b\n".repeat(TrayButton.TRAY_LINES))
        assertEquals(listOf("Yes", "no", "L1"), TrayButton.pick(short).map { it.text() })
        assertEquals(listOf("L1", "Ack", "Clear"), TrayButton.pick(long).map { it.text() })
        assertEquals("many short lines, as a diff is", listOf("L1", "Ack", "Clear"), TrayButton.pick(tall).map { it.text() })
        assertEquals("the app card still offers them", listOf("yes", "no"), TrayButton.replies(long).map { it.reply })
    }

    @Test fun anAnsweredOneShowsNoReplies() {
        val n = event("acknowledged", actions = yesNo, reply = "yes")
        assertEquals("yes", n.reply)
        assertEquals(listOf("L1", "Snooze", "Clear"), TrayButton.pick(n).map { it.text() })
    }
}
