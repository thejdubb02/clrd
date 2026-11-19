package me.itsjustin.clrd

import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okio.Buffer
import org.json.JSONObject
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Share and Copy text, the icon and attachment fields, and who gets the token when an image is fetched. */
class ShareAndImagesTest {
    private fun n(title: String, message: String, click: String?) =
        Notif("a", "t", title, message, 3, "", click, "[]", Notif.ACTIVE, null, null, 0, 0, 1)

    @Test fun copyIsTitleBlankLineMessageThenClick() {
        assertEquals("Disk full\n\n/ is at 98%\nhttps://x.example/d", n("Disk full", "/ is at 98%", "https://x.example/d").copyText())
        assertEquals("Disk full\n\n/ is at 98%", n("Disk full", "/ is at 98%", null).copyText())
        assertEquals("/ is at 98%", n("", "/ is at 98%", "").copyText())
        assertEquals("Disk full\nhttps://x.example/d", n("Disk full", "", "https://x.example/d").copyText())
    }

    @Test fun shareTextIsMessageThenClick() {
        assertEquals("/ is at 98%\nhttps://x.example/d", n("Disk full", "/ is at 98%", "https://x.example/d").shareText())
        assertEquals("/ is at 98%", n("Disk full", "/ is at 98%", null).shareText())
        assertEquals("https://x.example/d", n("Disk full", "", "https://x.example/d").shareText())
    }

    @Test fun iconAndAttachmentFromJson() {
        val old = Notif.fromJson(JSONObject("""{"id":"a","attachment":null}""")) // logged before the server had them
        assertEquals("", old.icon)
        assertNull(old.file)
        val nulls = Notif.fromJson(JSONObject("""{"id":"a","icon":null}"""))
        assertEquals("", nulls.icon)
        val full = Notif.fromJson(JSONObject("""{"id":"a","icon":"https://x.example/i.png","attachment":{"name":"r.png","url":"/api/v1/files/9f","type":"image/png","size":12}}"""))
        assertEquals("https://x.example/i.png", full.icon)
        assertEquals(Attachment("r.png", "/api/v1/files/9f", "image/png", 12), full.file)
        assertTrue(full.file!!.isOurs && full.file!!.isImage)
    }

    @Test fun attachmentKinds() {
        val ext = Attachment.parse("""{"name":"","url":"https://x.example/r.pdf","type":"","size":0}""")!!
        assertFalse(ext.isOurs)
        assertFalse(ext.isImage)
        assertEquals("attachment", ext.name)
        assertFalse(Attachment.parse("""{"url":"/api/v1/files/1","type":"image/svg+xml"}""")!!.isImage) // shown inline only for the 4 safe types
        assertNull(Attachment.parse("""{"url":"javascript:alert(1)"}"""))
        assertNull(Attachment.parse("""{"url":"//evil.example/x"}""")?.takeIf { it.isOurs })
        assertNull(Attachment.parse(""))
    }

    @Test fun theTokenGoesOnlyToOurServer() {
        val ours = Images.request("/api/v1/files/9f", "https://clrd.example/", "tok")!!
        assertEquals("https://clrd.example/api/v1/files/9f", ours.url.toString())
        assertEquals("Bearer tok", ours.header("Authorization"))
        assertNull(Images.request("https://x.example/i.png", "https://clrd.example", "tok")!!.header("Authorization"))
        assertNull(Images.request("//evil.example/i.png", "https://clrd.example", "tok"))
        assertNull(Images.request("/\\evil.example/i.png", "https://clrd.example", "tok"))
        assertNull(Images.request("file:///etc/passwd", "https://clrd.example", "tok"))
        assertNull(Images.request("/api/v1/files/9f", "", "tok")) // not set up yet
    }

    @Test fun aRedirectOffOurServerDropsTheToken() {
        MockWebServer().use { other ->
            MockWebServer().use { ours ->
                other.enqueue(MockResponse().setBody("img"))
                ours.enqueue(MockResponse().setResponseCode(302).setHeader("Location", other.url("/i.png")))
                val req = Images.request("/api/v1/files/9f", ours.url("/").toString(), "tok")!!
                assertArrayEquals("img".toByteArray(), Images.fetch(OkHttpClient(), req, 100))
                assertEquals("Bearer tok", ours.takeRequest().getHeader("Authorization"))
                assertNull(other.takeRequest().getHeader("Authorization"))
            }
        }
    }

    @Test fun fetchStopsAtTheCap() {
        MockWebServer().use { s ->
            s.enqueue(MockResponse().setBody("x".repeat(101))) // Content-Length says too big
            // chunked, so no Content-Length: the cap must hold while reading
            s.enqueue(MockResponse().setChunkedBody(Buffer().writeUtf8("x".repeat(101)), 10))
            s.enqueue(MockResponse().setChunkedBody(Buffer().writeUtf8("x".repeat(100)), 10))
            s.enqueue(MockResponse().setResponseCode(404).setBody("gone"))
            val http = OkHttpClient()
            val req = Images.request(s.url("/i.png").toString(), "", "")!!
            assertNull(Images.fetch(http, req, 100))
            assertNull(Images.fetch(http, req, 100))
            assertEquals(100, Images.fetch(http, req, 100)!!.size)
            assertNull(Images.fetch(http, req, 100))
        }
    }
}
