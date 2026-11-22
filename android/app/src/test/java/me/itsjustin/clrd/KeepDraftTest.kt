package me.itsjustin.clrd

import org.junit.Assert.assertEquals
import org.junit.Test

/** A reply box that goes away unasked keeps its text (#539); Send, Cancel and a blank box keep none. */
class KeepDraftTest {
    @Test fun onlyAnUnaskedCloseKeepsText() {
        val drafts = mutableMapOf<String, String>()
        keepDraft(drafts, "a", "half typed", ended = false) // the question was edited and the box vanished
        keepDraft(drafts, "b", "sent", ended = true)
        keepDraft(drafts, "c", "  ", ended = false)
        assertEquals(mapOf("a" to "half typed"), drafts)
    }
}
