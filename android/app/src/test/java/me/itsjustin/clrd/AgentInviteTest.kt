package me.itsjustin.clrd

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** #457: the Add agent form sends what POST /api/v1/agents/invite takes, and waits until it is filled. */
class AgentInviteTest {
    @Test fun topicsSplitOnCommasAndSpaces() {
        assertEquals(listOf("build", "deploys"), agentTopics(" build, deploys ,,build "))
        assertEquals(listOf("a", "b"), agentTopics("a b"))
    }

    @Test fun theBodyHasNameTopicsAndMinutes() {
        val o = agentInviteBody(" build-bot ", "build,deploys", 15)!!
        assertEquals("build-bot", o.getString("name"))
        assertEquals("[\"build\",\"deploys\"]", o.getJSONArray("topics").toString())
        assertEquals("15m", o.getString("expires_in"))
    }

    @Test fun noBodyWithoutANameOrATopic() {
        assertNull(agentInviteBody("  ", "build", 15))
        assertNull(agentInviteBody("bot", " , ", 15))
    }
}
