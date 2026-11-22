package me.itsjustin.clrd

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class SendersTest { // #119
    @Test fun kindsReadAsOnTheWeb() {
        assertEquals(R.string.sender_gotify_app, senderKind("gotify_app"))
        assertEquals(R.string.sender_anonymous, senderKind("anonymous"))
        assertEquals(R.string.sender_token, senderKind("something_new"))
    }

    @Test fun topicsByName() {
        assertEquals("alerts (3), ci (1)", senderTopics(JSONObject("""{"topics":{"ci":1,"alerts":3}}""")))
        assertEquals("", senderTopics(JSONObject("{}")))
    }

    @Test fun anEmptyRecentListSaysWhy() { // #530
        assertEquals(R.string.sender_none_stored, senderEmpty(JSONObject("""{"count":40,"recent":[]}""")))
        assertEquals(R.string.sender_none_sent, senderEmpty(JSONObject("""{"count":0}""")))
        assertNull(senderEmpty(JSONObject("""{"count":1,"recent":[{"id":"a"}]}""")))
    }
}
