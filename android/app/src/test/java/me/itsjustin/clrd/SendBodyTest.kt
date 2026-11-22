package me.itsjustin.clrd

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Test

/** #150: the Send box posts what POST /api/v1/notifications takes, and waits for a topic and a title or message. */
class SendBodyTest {
    @Test fun theBodyHasTopicTitleMessageAndPriority() {
        val o = sendBody(" notes ", " From the phone ", " https://example.com/read-later ", 4)!!
        assertEquals("notes", o.getString("topic"))
        assertEquals("From the phone", o.getString("title"))
        assertEquals("https://example.com/read-later", o.getString("message"))
        assertEquals(4, o.getInt("priority"))
    }

    @Test fun noTitleSendsNone() {
        assertFalse(sendBody("notes", "  ", "hi", 3)!!.has("title"))
    }

    @Test fun waitsForATopicAndSomethingToSay() {
        assertNull(sendBody(" ", "t", "hi", 3))
        assertNull(sendBody("notes", " ", "  ", 3))
        assertEquals("a title alone is enough, as on the server", "Door open", sendBody("notes", "Door open", " ", 3)!!.getString("title"))
    }
}
