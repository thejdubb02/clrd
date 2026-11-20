package me.itsjustin.clrd

import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class LinkTest {
    private val code = "Ab3dEf6hIj9kLm2nOp5qRs"
    private fun enc(s: String) = java.net.URLEncoder.encode(s, "UTF-8")

    @Test fun httpsLinkKeepsTheBasePath() {
        assertEquals(Scan.Link(LinkCode("https://clrd.example.org", code)), parseScan("https://clrd.example.org/link?c=$code"))
        assertEquals(Scan.Link(LinkCode("https://host.example/clrd", code)), parseScan("https://host.example/clrd/link?c=$code"))
        assertEquals(Scan.Link(LinkCode("http://10.0.0.5:8080", code)), parseScan("  HTTP://10.0.0.5:8080/link/?c=$code\n"))
        assertEquals(Scan.Link(LinkCode("https://clrd.example.org", code)), parseScan("https://Clrd.Example.org/link?c=$code#frag"))
    }

    @Test fun clrdLinkFromTheBrowserPage() {
        assertEquals(Scan.Link(LinkCode("https://host.example/clrd", code)), parseScan("clrd://link?server=${enc("https://Host.Example/clrd/")}&code=$code"))
        assertEquals(Scan.Bad, parseScan("clrd://link?server=${enc("javascript:alert(1)")}&code=$code"))
        assertEquals(Scan.Bad, parseScan("clrd://link?server=${enc("ftp://host.example")}&code=$code"))
        assertEquals(Scan.Bad, parseScan("clrd://link?code=$code"))
    }

    @Test fun pairLinksGoToFlowB() {
        assertEquals(Scan.Pair(PairLink("https://host.example/clrd", code)), parseScan("https://host.example/clrd/pair?c=$code"))
        assertEquals(Scan.Pair(PairLink("https://a.example", code)), parseScan("clrd://pair?server=${enc("https://a.example")}&code=$code"))
        // The scanned /pair server is only compared, so a different one is refused by the existing gate.
        val p = (parseScan("https://evil.example/pair?c=$code") as Scan.Pair).link
        assertEquals(PairGate.OTHER_SERVER, pairGate(p, "https://clrd.example.org", "tk"))
    }

    @Test fun shownAddressIsRebuiltNotCopied() {
        // User info would make the dialog read as the good host while requests go to the evil one.
        assertEquals(Scan.Link(LinkCode("https://evil.example", code)), parseScan("https://good.example@evil.example/link?c=$code"))
    }

    @Test fun rejectsAnythingElse() {
        assertEquals(Scan.Bad, parseScan("hello"))
        assertEquals(Scan.Bad, parseScan(""))
        assertEquals(Scan.Bad, parseScan("WIFI:S:home;T:WPA;P:secret;;"))
        assertEquals(Scan.Bad, parseScan("ftp://host.example/link?c=$code"))
        assertEquals(Scan.Bad, parseScan("javascript://host.example/link?c=$code"))
        assertEquals(Scan.Bad, parseScan("clrd://setup?server=${enc("https://a.example")}&code=$code"))
        assertEquals(Scan.Bad, parseScan("https://host.example/?c=$code"))
        assertEquals(Scan.Bad, parseScan("https://host.example/linked?c=$code"))
        assertEquals(Scan.Bad, parseScan("https://host.example/link/extra?c=$code"))
        assertEquals(Scan.Bad, parseScan("https://host.example/link"))
        assertEquals(Scan.Bad, parseScan("https://host.example/link?c=$code&c=${code}x"))
        assertEquals(Scan.Bad, parseScan("https:///link?c=$code"))
    }

    @Test fun codeIs16To64Base62() {
        fun scan(c: String) = parseScan("https://host.example/link?c=$c")
        assertEquals(Scan.Link(LinkCode("https://host.example", "a".repeat(16))), scan("a".repeat(16)))
        assertEquals(Scan.Link(LinkCode("https://host.example", "a".repeat(64))), scan("a".repeat(64)))
        assertEquals(Scan.Bad, scan("a".repeat(15)))
        assertEquals(Scan.Bad, scan("a".repeat(65)))
        assertEquals(Scan.Bad, scan("abcdefghijklmno-"))
        assertEquals(Scan.Bad, scan("$code%0A"))
        assertEquals(Scan.Bad, scan("$code%2F..%2Fx"))
    }

    @Test fun secretIsLongAndFresh() {
        val a = newLinkSecret()
        assertTrue(a.length >= 32)
        assertTrue(a != newLinkSecret())
    }

    @Test fun pollingSurvivesHiccupsButNotRefusals() {
        assertTrue(claimRetries(java.io.IOException("offline")))
        assertTrue(claimRetries(HttpError(502, "bad gateway")))
        assertTrue(claimRetries(HttpError(503, "busy", 5000)))
        assertTrue(claimRetries(HttpError(429, "slow down", 1000)))
        assertTrue(claimRetries(HttpError(408, "timeout")))
        assertTrue(!claimRetries(HttpError(404, "no such route")))
        assertTrue(!claimRetries(HttpError(400, "bad code")))
        assertTrue(!claimRetries(HttpError(200, "no token in the answer")))
    }

    @Test fun claimSendsNoTokenAndReadsEachAnswer() {
        val server = MockWebServer()
        server.enqueue(MockResponse().setResponseCode(202).setBody("""{"state":"waiting"}"""))
        server.enqueue(MockResponse().setResponseCode(200).setBody("""{"state":"approved","token":"clrd_test","name":"Pixel"}"""))
        server.enqueue(MockResponse().setResponseCode(410).setBody("""{"code":"gone"}"""))
        server.enqueue(MockResponse().setResponseCode(200).setBody("""{"state":"approved"}"""))
        server.start()
        try {
            val http = OkHttpClient()
            val base = server.url("/clrd/").toString()
            assertEquals(Claim.Waiting, linkClaim(http, base, code, "s".repeat(43), "Pixel"))
            assertEquals(Claim.Approved("clrd_test"), linkClaim(http, base, code, "s".repeat(43), "Pixel"))
            assertEquals(Claim.Gone, linkClaim(http, base, code, "s".repeat(43), "Pixel"))
            val err = runCatching { linkClaim(http, base, code, "s".repeat(43), "Pixel") }.exceptionOrNull()
            assertTrue(err is HttpError) // a 200 without a token never reaches the save path
            val r = server.takeRequest()
            assertEquals("/clrd/api/v1/link/claim", r.path)
            assertEquals("POST", r.method)
            assertNull(r.getHeader("Authorization"))
            val body = JSONObject(r.body.readUtf8())
            assertEquals(code, body.getString("code"))
            assertEquals("s".repeat(43), body.getString("secret"))
            assertEquals("Pixel", body.getString("name"))
        } finally {
            server.shutdown()
        }
    }

    @Test
    fun matchNumberAgreesWithTheServer() {
        assertEquals("2319", linkCheck("abc")) // same vector as the server's TestLinkCheck
        assertEquals(4, linkCheck(newLinkSecret()).length)
    }
}
