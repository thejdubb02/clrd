package me.itsjustin.clrd

import okhttp3.OkHttpClient
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.util.concurrent.CopyOnWriteArrayList

/** Runs SyncClient against a fake clrd server: replay, reconnect, reset, queued actions, bad token. */
class SyncClientTest {
    private val server = MockWebServer()
    private val http = OkHttpClient()
    private val store = FakeStore()
    private val statuses = CopyOnWriteArrayList<SyncClient.Status>()
    private val wsSince = CopyOnWriteArrayList<String>()
    private val posts = CopyOnWriteArrayList<String>()
    private val serverSockets = CopyOnWriteArrayList<WebSocket>()

    /** What the fake server sends on each WebSocket connect, by connect number. */
    private var onConnect: (Int, WebSocket) -> Unit = { _, _ -> }
    private var wsResponse: () -> MockResponse? = { null }
    private var open = "[]"
    private var client: SyncClient? = null

    @Before fun setUp() {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                val path = request.path.orEmpty()
                return when {
                    path.startsWith("/api/v1/ws") -> {
                        wsSince += request.requestUrl!!.queryParameter("since")!!
                        val n = wsSince.size
                        wsResponse() ?: MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                            override fun onOpen(webSocket: WebSocket, response: Response) { serverSockets += webSocket; onConnect(n, webSocket) }
                        })
                    }
                    path == "/healthz" -> MockResponse().setBody("""{"ok":true,"seq":50}""")
                    path.startsWith("/api/v1/notifications?state=open") -> MockResponse().setBody(open)
                    request.method == "POST" -> {
                        posts += path
                        if (path.contains("gone")) MockResponse().setResponseCode(404).setBody("""{"error":"not found"}""")
                        else MockResponse().setBody("{}")
                    }
                    else -> MockResponse().setResponseCode(500)
                }
            }
        }
        server.start()
    }

    @After fun tearDown() {
        client?.stop()
        serverSockets.forEach { it.close(1001, null) }
        server.shutdown()
    }

    private fun start() {
        val api = Api(http, server.url("/").toString().trimEnd('/'), "tok")
        client = SyncClient(http, api, store, onStatus = { statuses += it }, baseDelayMs = 20).also { it.start() }
    }

    @Test fun replayIsCoalescedAndSeqSaved() {
        onConnect = { n, ws ->
            if (n == 1) {
                ws.send(ev(1, "a", "active"))
                ws.send(ev(2, "a", "dismissed")) // cleared on another phone while we were offline
                ws.send(ev(3, "b", "active"))
                ws.send("""{"type":"ready","seq":3}""")
            }
        }
        start()
        await { SyncClient.Status.CONNECTED in statuses }
        assertEquals(listOf("0"), wsSince)
        assertEquals(1, store.applies.size) // one batch, so the phone never buzzed for "a"
        assertEquals("dismissed", store.rows["a"]!!.state)
        assertEquals("active", store.rows["b"]!!.state)
        assertEquals(3L, store.lastSeq())
    }

    @Test fun liveEventAfterReadyIsAppliedAtOnce() {
        onConnect = { _, ws ->
            ws.send("""{"type":"ready","seq":0}""")
            ws.send(ev(9, "x", "resolved"))
        }
        start()
        await { store.rows["x"]?.state == "resolved" }
        assertEquals(9L, store.lastSeq())
    }

    @Test fun reconnectAsksForEventsSinceLastSeq() {
        onConnect = { n, ws ->
            if (n == 1) {
                ws.send(ev(4, "a", "active"))
                ws.send("""{"type":"ready","seq":4}""")
                ws.close(1001, "server restart")
            }
        }
        start()
        await { wsSince.size >= 2 }
        assertEquals("4", wsSince[1])
    }

    @Test fun resetReplacesEverythingWithOpenList() {
        store.rows["stale"] = notif("stale", "active", 1)
        open = """{"notifications":[${notifJson("fresh", "active", 40)}]}"""
        onConnect = { n, ws -> if (n == 1) ws.send("""{"type":"reset"}""") else ws.send("""{"type":"ready","seq":50}""") }
        start()
        await { wsSince.size >= 2 }
        assertEquals(setOf("fresh"), store.rows.keys)
        assertEquals("50", wsSince[1]) // seq read from /healthz before the list
    }

    @Test fun queuedActionsGoOutBeforeTheSocketAndA404IsDropped() {
        store.queue += Pending(1, "a", "dismiss")
        store.queue += Pending(2, "gone", "ack")
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":0}""") }
        start()
        await { SyncClient.Status.CONNECTED in statuses }
        assertEquals(listOf("/api/v1/notifications/a/dismiss", "/api/v1/notifications/gone/ack"), posts)
        assertTrue(store.queue.isEmpty())
    }

    @Test fun badTokenStopsRetrying() {
        wsResponse = { MockResponse().setResponseCode(401) }
        start()
        await { SyncClient.Status.AUTH_FAILED in statuses }
        Thread.sleep(300) // 15 backoff periods at 20 ms
        assertEquals(1, wsSince.size)
    }

    @Test fun serverDownRetriesWithBackoff() {
        wsResponse = { MockResponse().setResponseCode(503) }
        start()
        await { wsSince.size >= 3 }
        assertTrue(SyncClient.Status.OFFLINE in statuses)
    }

    private fun await(cond: () -> Boolean) {
        val end = System.currentTimeMillis() + 5_000
        while (!cond()) {
            check(System.currentTimeMillis() < end) { "timed out; statuses=$statuses since=$wsSince rows=${store.rows.keys}" }
            Thread.sleep(10)
        }
    }

    private fun notifJson(id: String, state: String, seq: Long) =
        """{"id":"$id","topic":"t","title":"T","message":"m","priority":3,"tags":[],"actions":[],"state":"$state",
           "created_at":"2026-11-19T10:00:00Z","updated_at":"2026-11-19T10:00:00Z","seq":$seq}"""

    private fun ev(seq: Long, id: String, state: String) =
        """{"seq":$seq,"type":"${if (state == "active") "created" else "state"}","notification":${notifJson(id, state, seq)}}"""

    private fun notif(id: String, state: String, seq: Long) = Notif.fromJson(org.json.JSONObject(notifJson(id, state, seq)))

    /** Mirrors RoomStore rules: keep a row only if its seq is not older than what we have. */
    class FakeStore : Store {
        val rows = java.util.concurrent.ConcurrentHashMap<String, Notif>()
        val queue = CopyOnWriteArrayList<Pending>()
        val applies = CopyOnWriteArrayList<Collection<Notif>>()
        @Volatile private var seq = 0L

        override fun lastSeq() = seq
        override fun apply(notifs: Collection<Notif>, seq: Long) {
            applies += notifs
            for (n in notifs) if (n.seq >= (rows[n.id]?.seq ?: 0)) rows[n.id] = n
            this.seq = maxOf(this.seq, seq)
        }
        override fun replaceAll(open: List<Notif>, seq: Long) {
            rows.clear()
            open.forEach { rows[it.id] = it }
            this.seq = seq
        }
        override fun pending() = queue.toList()
        override fun removePending(p: Pending) { queue.remove(p) }
    }
}
