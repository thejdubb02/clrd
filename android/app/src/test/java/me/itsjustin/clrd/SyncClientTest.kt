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
    private val log = CopyOnWriteArrayList<String>() // POSTs and WebSocket connects, in arrival order
    private val serverSockets = CopyOnWriteArrayList<WebSocket>()

    /** What the fake server sends on each WebSocket connect, by connect number. */
    private var onConnect: (Int, WebSocket) -> Unit = { _, _ -> }
    private var wsResponse: () -> MockResponse? = { null }
    private val lists = CopyOnWriteArrayList<String>()
    /** Open list page by `before` cursor (null = first page). */
    private var open: (String?) -> String = { """{"notifications":[],"seq":0}""" }
    private var client: SyncClient? = null
    /** Holds a "slow" POST open until the test lets it go. */
    private val release = java.util.concurrent.CountDownLatch(1)

    @Before fun setUp() {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                val path = request.path.orEmpty()
                return when {
                    path.startsWith("/api/v1/ws") -> {
                        wsSince += request.requestUrl!!.queryParameter("since")!!
                        log += "ws"
                        val n = wsSince.size
                        wsResponse() ?: MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                            override fun onOpen(webSocket: WebSocket, response: Response) { serverSockets += webSocket; onConnect(n, webSocket) }
                        })
                    }
                    path.startsWith("/api/v1/notifications?state=open") -> {
                        lists += path
                        MockResponse().setBody(open(request.requestUrl!!.queryParameter("before")))
                    }
                    request.method == "POST" -> {
                        posts += path
                        log += path
                        if (path.contains("flaky") && posts.count { it == path } == 1) MockResponse().setResponseCode(503)
                        else if (path.contains("slow")) { release.await(5, java.util.concurrent.TimeUnit.SECONDS); MockResponse().setBody("{}") }
                        else if (path.contains("gone")) MockResponse().setResponseCode(404).setBody("""{"error":"not found"}""")
                        else MockResponse().setBody("{}")
                    }
                    else -> MockResponse().setResponseCode(500)
                }
            }
        }
        server.start()
    }

    @After fun tearDown() {
        release.countDown()
        client?.stop()
        serverSockets.forEach { it.close(1001, null) }
        server.shutdown()
    }

    private fun start() {
        val api = Api(http, server.url("/").toString().trimEnd('/'), "tok")
        client = SyncClient(http, api, store, onStatus = { statuses += it }, baseDelayMs = 20, flushRetryMs = 50).also { it.start() }
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
        // A full first page (500) means the client must ask for the next one with before=<last id>.
        val page1 = (1..Api.PAGE).joinToString(",") { notifJson("p$it", "active", 40) }
        open = { before ->
            if (before == null) """{"notifications":[$page1],"seq":50}"""
            else """{"notifications":[${notifJson("last", "active", 41)}],"seq":51}"""
        }
        onConnect = { n, ws -> if (n == 1) ws.send("""{"type":"reset"}""") else ws.send("""{"type":"ready","seq":50}""") }
        start()
        await { wsSince.size >= 2 }
        assertEquals(Api.PAGE + 1, store.rows.size)
        assertTrue(!store.rows.containsKey("stale") && store.rows.containsKey("last"))
        assertEquals(2, lists.size)
        assertTrue(lists[1], lists[1].endsWith("before=p${Api.PAGE}"))
        assertEquals("50", wsSince[1]) // seq from the first page, read by the server before its query
    }

    @Test fun queuedActionsGoOutBeforeTheSocketAndA404IsDropped() {
        store.queue += Pending(1, "a", "dismiss")
        store.queue += Pending(2, "gone", "ack")
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":0}""") }
        start()
        await { SyncClient.Status.CONNECTED in statuses }
        assertEquals(listOf("/api/v1/notifications/a/dismiss", "/api/v1/notifications/gone/ack", "ws"), log)
        assertTrue(store.queue.isEmpty())
    }

    @Test fun failedSendWhileConnectedRetriesOnItsOwn() {
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":0}""") }
        start()
        await { SyncClient.Status.CONNECTED in statuses }
        store.queue += Pending(1, "flaky", "dismiss")
        client!!.flushSoon() // first POST gets 503 while the socket stays up
        await { store.queue.isEmpty() }
        assertEquals(2, posts.size)
        assertEquals(1, wsSince.size) // no reconnect was needed
    }

    @Test fun stopDuringFirstFlushNeverOpensTheSocket() {
        store.queue += Pending(1, "slow", "dismiss")
        start()
        await { posts.isNotEmpty() } // the send is held open by the server
        client!!.stop()
        release.countDown()
        Thread.sleep(500) // the connect would follow the send within milliseconds
        assertTrue(log.toString(), "ws" !in log)
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
