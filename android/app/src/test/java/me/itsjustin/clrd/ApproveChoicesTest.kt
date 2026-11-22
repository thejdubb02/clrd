package me.itsjustin.clrd

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test

/** #382: the phone's "May approve" menu offers what the web does, never a raise of itself or what it hangs under. */
class ApproveChoicesTest {
    private fun dev(id: String, approve: String, via: String = "", me: Boolean = false) =
        JSONObject().put("id", id).put("approve", approve).put("via_id", via).put("this", me)

    private val all = listOf("denied", "ask", "allowed")

    @Test fun anAllowedPhoneMayLowerItselfAndSetOthers() {
        val token = dev("t", "ask")
        val phone = dev("p", "allowed", via = "t", me = true)
        val other = dev("o", "denied")
        val list = listOf(token, phone, other)
        assertEquals(all, approveChoices(list, phone)) // allowed is the top, so every choice is a lowering
        assertEquals("never raise the token it hangs under", listOf("denied", "ask"), approveChoices(list, token))
        assertEquals(all, approveChoices(list, other))
    }

    @Test fun aPhoneNotAllowedGetsNoMenu() {
        val phone = dev("p", "ask", me = true)
        val other = dev("o", "denied")
        assertEquals(emptyList<String>(), approveChoices(listOf(phone, other), other))
        assertEquals(emptyList<String>(), approveChoices(listOf(phone, other), phone))
    }

    @Test fun aTopicLimitedPhoneGetsNoMenuEvenWhenAllowed() { // #433: the server says can_approve false
        val phone = dev("p", "allowed", me = true).put("can_approve", false)
        val other = dev("o", "denied")
        assertEquals(emptyList<String>(), approveChoices(listOf(phone, other), other))
        val full = dev("p", "allowed", me = true).put("can_approve", true)
        assertEquals(all, approveChoices(listOf(full, other), other))
    }

    @Test fun ownIsWalkedByIdAndALoopEnds() {
        val a = dev("a", "denied", via = "b")
        val b = dev("b", "denied", via = "a")
        val phone = dev("p", "allowed", via = "a", me = true)
        val list = listOf(a, b, phone)
        assertEquals(true, isOwn(list, b)) // two hops up
        assertEquals(listOf("denied"), approveChoices(list, b))
        assertEquals(false, isOwn(list, dev("x", "denied")))
    }
}
