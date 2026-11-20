package me.itsjustin.clrd

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PairTest {
    private val code = "Ab3dEf6hIj9kLm2n"
    private fun link(server: String) = "clrd://pair?server=${java.net.URLEncoder.encode(server, "UTF-8")}&code=$code"

    @Test fun readsTheLinkTheBrowserPageMakes() {
        assertEquals(PairLink("https://clrd.example.org", code), parsePairLink(link("https://clrd.example.org")))
        assertEquals(PairLink("https://home.example.org/clrd", code), parsePairLink("CLRD://Pair?code=$code&server=https%3A%2F%2Fhome.example.org%2Fclrd"))
    }

    @Test fun rejectsAnythingElse() {
        assertNull(parsePairLink("https://pair?server=https%3A%2F%2Fa.example&code=$code"))
        assertNull(parsePairLink("clrd://setup?server=https%3A%2F%2Fa.example&code=$code"))
        assertNull(parsePairLink("clrd://pair?code=$code"))
        assertNull(parsePairLink("clrd://pair?server=https%3A%2F%2Fa.example"))
        assertNull(parsePairLink("clrd://pair?server=javascript%3Aalert(1)&code=$code"))
        assertNull(parsePairLink("clrd://pair?server=https%3A%2F%2Fa.example&code=$code&server=https%3A%2F%2Fevil.example"))
        assertNull(parsePairLink("clrd://pair?server=%zz&code=$code"))
        assertNull(parsePairLink("not a uri at all"))
    }

    @Test fun codeIs16To64Base62() {
        val ok = "a".repeat(16)
        assertEquals(ok, parsePairLink(link("https://a.example").replace(code, ok))?.code)
        assertEquals("a".repeat(64), parsePairLink(link("https://a.example").replace(code, "a".repeat(64)))?.code)
        assertNull(parsePairLink(link("https://a.example").replace(code, "a".repeat(15))))
        assertNull(parsePairLink(link("https://a.example").replace(code, "a".repeat(65))))
        assertNull(parsePairLink(link("https://a.example").replace(code, "$code%0A")))
        assertNull(parsePairLink(link("https://a.example").replace(code, "$code%2F..%2Fx")))
        assertNull(parsePairLink(link("https://a.example").replace(code, "abcdefghijklmno-")))
    }

    @Test fun onlyTheConfiguredServerPasses() {
        val mine = "https://clrd.example.org"
        fun gate(server: String, url: String = mine, token: String = "tk") = pairGate(parsePairLink(link(server))!!, url, token)
        assertEquals(PairGate.OK, gate("https://clrd.example.org"))
        assertEquals(PairGate.OK, gate("HTTPS://Clrd.Example.org/"))
        assertEquals(PairGate.OK, gate("https://home.example.org/clrd", url = "https://home.example.org/clrd"))
        assertEquals(PairGate.OTHER_SERVER, gate("https://evil.example"))
        assertEquals(PairGate.OTHER_SERVER, gate("https://clrd.example.org@evil.example"))
        assertEquals(PairGate.OTHER_SERVER, gate("https://clrd.example.org.evil.example"))
        assertEquals(PairGate.OTHER_SERVER, gate("http://clrd.example.org"))
        assertEquals(PairGate.OTHER_SERVER, gate("https://clrd.example.org:8443"))
        assertEquals(PairGate.OTHER_SERVER, gate("https://home.example.org/other", url = "https://home.example.org/clrd"))
        assertEquals(PairGate.NOT_SET_UP, gate("https://clrd.example.org", url = ""))
        assertEquals(PairGate.NOT_SET_UP, gate("https://clrd.example.org", token = ""))
    }
}
