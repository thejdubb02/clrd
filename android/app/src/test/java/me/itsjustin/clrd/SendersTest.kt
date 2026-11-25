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
        val many = (1..20).map { "t$it" }
        assertEquals(many.sorted().joinToString(", ") { "$it (1)" }, senderTopics(JSONObject().put("topics", JSONObject(many.reversed().associateWith { 1 }))))
        assertEquals("", senderTopics(JSONObject("{}")))
    }

    @Test fun routesRoundTripThroughTheBox() {
        val r = JSONObject("""{"routes":{"zz":["a","b"],"*":[]}}""")
        assertEquals("*: \nzz: a, b\n", routeLines(r))
        val back = parseRoutes(routeLines(r))!!
        assertEquals("[\"a\",\"b\"]", back.getJSONArray("zz").toString())
        assertEquals(0, back.getJSONArray("*").length())
        assertEquals("", routeLines(JSONObject("{}")))
        assertEquals(0, parseRoutes("  \n")!!.length())
    }

    @Test fun badRoutesAreRefused() {
        assertNull(parseRoutes("no colon here"))
        assertNull(parseRoutes(": a"))
        assertNull(parseRoutes("ci: a\nci: b"))
        assertEquals("[\"x\"]", parseRoutes(" ci : x, ,\n")!!.getJSONArray("ci").toString())
    }

    @Test fun anEmptyRecentListSaysWhy() { // #530
        assertEquals(R.string.sender_none_stored, senderEmpty(JSONObject("""{"count":40,"recent":[]}""")))
        assertEquals(R.string.sender_none_sent, senderEmpty(JSONObject("""{"count":0}""")))
        assertNull(senderEmpty(JSONObject("""{"count":1,"recent":[{"id":"a"}]}""")))
    }
}
