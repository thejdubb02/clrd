package me.itsjustin.clrd

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** #523: a Send box rebuilt after a rotation sees the send in flight, so a second tap cannot send twice. */
class SenderTest {
    private val queued = mutableListOf<Runnable>()
    private val s = Sender { queued += it }

    @Test fun aSecondTapWhileSendingSendsNothing() {
        var posts = 0
        assertTrue(s.send({ posts++ }) { "no" })
        assertFalse("the box after a rotation taps again", s.send({ posts++ }) { "no" })
        assertTrue(s.busy)
        queued.forEach { it.run() }
        assertEquals(1, posts)
        assertFalse(s.busy)
        assertEquals("Home sees the result even from a new box", 1, s.sent)
        assertNull(s.err)
    }

    @Test fun aFailureKeepsTheReasonAndAllowsAnotherTry() {
        s.send({ throw IllegalStateException("topic not allowed") }) { "Not sent: ${it.message}" }
        queued.removeAt(0).run()
        assertEquals("Not sent: topic not allowed", s.err)
        assertEquals(0, s.sent)
        assertTrue("try again", s.send({}) { "no" })
        assertNull("a new try clears the old reason", s.err)
    }
}
