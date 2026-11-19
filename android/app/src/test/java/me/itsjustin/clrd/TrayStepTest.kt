package me.itsjustin.clrd

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Event JSON in, tray step out. The cancel cases are the headline feature. */
class TrayStepTest {
    private fun event(state: String, topic: String = "backups", actions: String = "[]") = Notif.fromJson(
        JSONObject(
            """{"id":"n1","topic":"$topic","title":"Backup failed","message":"disk full","priority":4,
               "tags":["warning","disk"],"click":null,"actions":$actions,"state":"$state","state_by":"phone",
               "snooze_until":null,"created_at":"2026-11-19T10:00:00Z","updated_at":"2026-11-19T10:05:00Z","seq":7}""",
        ),
    )

    @Test fun activeShowsLoud() = assertEquals(TrayStep.Show(event("active"), silent = false), TrayStep.plan(event("active"), emptySet()))

    @Test fun acknowledgedStaysQuietly() = assertEquals(TrayStep.Show(event("acknowledged"), silent = true), TrayStep.plan(event("acknowledged"), emptySet()))

    @Test fun dismissedElsewhereCancels() = assertEquals(TrayStep.Cancel("n1"), TrayStep.plan(event("dismissed"), emptySet()))

    @Test fun resolvedCancels() = assertEquals(TrayStep.Cancel("n1"), TrayStep.plan(event("resolved"), emptySet()))

    @Test fun snoozedCancels() = assertEquals(TrayStep.Cancel("n1"), TrayStep.plan(event("snoozed"), emptySet()))

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
        assertTrue(!n.isFinal)
    }

    private fun labels(state: String, actions: String = "[]") = TrayButton.pick(event(state, actions = actions)).map { it.label }
    private fun links(n: Int) = (1..n).joinToString(",", "[", "]") { """{"label":"L$it","url":"https://x.example/$it"}""" }

    @Test fun noSenderLinksKeepsOurThree() = assertEquals(listOf("Ack", "Snooze 1h", "Clear"), labels("active"))

    @Test fun ackedHasNoAck() = assertEquals(listOf("Snooze 1h", "Clear"), labels("acknowledged"))

    @Test fun oneLinkDropsSnooze() = assertEquals(listOf("L1", "Ack", "Clear"), labels("active", links(1)))

    @Test fun twoLinksAtMostThenAck() = assertEquals(listOf("L1", "L2", "Ack"), labels("active", links(3)))

    @Test fun twoLinksOnAnAckedOneThenClear() = assertEquals(listOf("L1", "L2", "Clear"), labels("acknowledged", links(2)))

    @Test fun onlyWebLinksBecomeButtons() {
        val actions = """[{"label":"Bad","url":"intent://x#Intent;end"},{"label":"","url":"HTTPS://x.example"},{"label":"No url"}]"""
        val b = TrayButton.pick(event("active", actions = actions))
        assertEquals(listOf("Open", "Ack", "Clear"), b.map { it.label })
        assertEquals("https://x.example", b[0].url)
    }
}
