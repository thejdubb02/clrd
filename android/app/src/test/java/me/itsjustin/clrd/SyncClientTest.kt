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
    private val wsAt = CopyOnWriteArrayList<Long>() // System.nanoTime() of each connect, as the server saw it
    private val posts = CopyOnWriteArrayList<String>()
    private val postAt = CopyOnWriteArrayList<Long>()
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
    @Volatile private var slowAnswered = false
    /** Body of a successful POST, by path. The server answers an action with the changed row. */
    private var answer: (String) -> String = { "{}" }
    /** Body of GET /api/v1/notifications/{id}, or null for 404. */
    private var current: (String) -> String? = { null }
    private val gets = CopyOnWriteArrayList<String>()
    /** How many GETs of a row fail with 503 before they answer. */
    @Volatile private var getFails = 0

    @Before fun setUp() {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                val path = request.path.orEmpty()
                return when {
                    path.startsWith("/api/v1/ws") -> {
                        wsAt += System.nanoTime()
                        wsSince += request.requestUrl!!.queryParameter("since")!!
                        val n = wsSince.size
                        wsResponse() ?: MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                            override fun onOpen(webSocket: WebSocket, response: Response) { serverSockets += webSocket; onConnect(n, webSocket) }
                            // Answer the client's close as a real server does. A socket still opening when
                            // tearDown runs is not in serverSockets, and unanswered it held shutdown for 5 s (#421).
                            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { webSocket.close(code, null) }
                        })
                    }
                    path.startsWith("/api/v1/notifications?state=open") -> {
                        lists += path
                        MockResponse().setBody(open(request.requestUrl!!.queryParameter("before")))
                    }
                    request.method == "GET" && path.startsWith("/api/v1/notifications/") -> {
                        gets += path
                        if (gets.size <= getFails) return MockResponse().setResponseCode(503)
                        current(path.substringAfterLast('/'))?.let { MockResponse().setBody(it) }
                            ?: MockResponse().setResponseCode(404).setBody("""{"error":"not found"}""")
                    }
                    request.method == "POST" -> {
                        postAt += System.nanoTime()
                        posts += path
                        if (path.contains("flaky") && posts.count { it == path } == 1) MockResponse().setResponseCode(503)
                        else if (path.contains("busy") && posts.count { it == path } == 1) MockResponse().setResponseCode(429).setHeader("Retry-After", "1")
                        else if (path.contains("slow")) {
                            release.await(10, java.util.concurrent.TimeUnit.SECONDS)
                            slowAnswered = true
                            MockResponse().setBody("{}")
                        }
                        else if (path.contains("stale")) MockResponse().setResponseCode(409).setBody("""{"error":"changed"}""")
                        else if (path.contains("gone")) MockResponse().setResponseCode(404).setBody("""{"error":"not found"}""")
                        else if (path.contains("denied")) MockResponse().setResponseCode(403).setBody("""{"error":"no","code":"approve_denied"}""")
                        else if (path.contains("badtext")) MockResponse().setResponseCode(400).setBody("""{"error":"text over 1000 characters","code":"bad_request"}""")
                        else if (path.contains("asked")) MockResponse().setResponseCode(202).setBody(notifJson("prompt", "active", 7))
                        else MockResponse().setBody(answer(path))
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

    private val settings = CopyOnWriteArrayList<String>()
    @Volatile private var devices = 0

    private fun start(replayQuietMs: Long = 5_000) {
        val api = Api(http, server.url("/").toString().trimEnd('/'), "tok")
        client = SyncClient(http, api, store, onStatus = { statuses += it }, onSettings = { settings += it.toString() }, onDevices = { devices++ }, baseDelayMs = 20, flushRetryMs = 50, replayQuietMs = replayQuietMs)
            .also { it.start() }
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

    @Test fun readySeqMovesCursorWithNoVisibleEvents() {
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":900}""") }
        start()
        await { store.lastSeq() == 900L }
    }

    @Test fun settingsMessageReachesTheApp() {
        onConnect = { _, ws ->
            ws.send("""{"type":"settings","settings":{"snooze_intervals":["15m","2h"],"snooze_default":"2h"}}""")
            ws.send("""{"type":"ready","seq":0}""")
        }
        start()
        await { SyncClient.Status.CONNECTED in statuses }
        assertEquals(Snooze("2h", listOf("15m", "2h")), Snooze.from(org.json.JSONObject(settings.single())))
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

    @Test fun aRepeatEventAlertsAgain() {
        onConnect = { _, ws ->
            ws.send("""{"type":"ready","seq":0}""")
            ws.send(ev(4, "r", "active"))
            ws.send(ev(5, "r", "active").replace("\"created\"", "\"repeat\""))
        }
        start()
        await { store.rows["r"]?.seq == 5L }
        assertEquals(listOf(false, true), store.again.toList()) // only the repeat rings while it is showing
    }

    @Test fun readyTellsThePhoneWhichMarksAreItsOwn() { // #451: never guessed, and an older server sends none
        onConnect = { n, ws -> ws.send(if (n == 1) """{"type":"ready","seq":1,"mark_id":"u1"}""" else """{"type":"ready","seq":1}""") }
        start()
        await { store.markIds.isNotEmpty() }
        assertEquals(listOf("u1"), store.markIds.toList())
        val n = Notif.fromJson(org.json.JSONObject(notifJson("m", "active", 1)).put("marks", org.json.JSONArray(
            """[{"by":"stew","by_id":"t9","mark":"seen"},{"by":"justin","by_id":"u1","mark":"actioned","note":"done"}]""")))
        assertEquals("done", n.markBy("u1")!!.optString("note"))
        assertEquals(null, n.markBy("u2"))
        assertEquals(null, n.markBy("")) // no id from the server: no own mark, so no Remove
    }

    @Test fun aMarkUpdatesTheCardQuietly() { // card marks: someone else saw or actioned it, nothing rings
        val mark = """"marks":[{"by":"stew","mark":"actioned","note":"filed relay#512"}],"seq":6}"""
        onConnect = { _, ws ->
            ws.send(ev(5, "z", "active").replace("\"created\"", "\"mark\"").replace("\"seq\":5}}", "$mark}")) // only a mark in the replay
            ws.send("""{"type":"ready","seq":6}""")
            ws.send(ev(7, "y", "active"))
            ws.send(ev(8, "y", "active").replace("\"created\"", "\"mark\"").replace("\"seq\":8}}", mark.replace(":6", ":8") + "}"))
        }
        start()
        await { store.lastSeq() == 8L } // set after the row, so the row is there too
        assertEquals(listOf("stew actioned: filed relay#512"), store.rows["z"]!!.markLines)
        assertEquals(listOf("stew actioned: filed relay#512"), store.rows["y"]!!.markLines)
        assertEquals(listOf(true, false, false, true), store.quiet.toList()) // mark z, the empty replay save, y created, mark y
        assertEquals(8L, store.lastSeq())
    }

    @Test fun aReplayedMarkKeepsTheAlertsSilence() { // the mark event has no silenced field, the created one does
        val mark = """"marks":[{"by":"stew","mark":"seen"}],"seq":6}"""
        onConnect = { _, ws ->
            ws.send(ev(5, "x", "active").replace("\"seq\":5}}", "\"silenced\":true,\"seq\":5}}"))
            ws.send(ev(6, "x", "active").replace("\"created\"", "\"mark\"").replace("\"seq\":6}}", "$mark}"))
            ws.send("""{"type":"ready","seq":6}""")
        }
        start()
        await { store.lastSeq() == 6L }
        assertTrue(store.rows["x"]!!.silenced)
        assertEquals(listOf("stew seen"), store.rows["x"]!!.markLines)
    }

    @Test fun aReplayedMarkIsDroppedOnANewEpoch() {
        store.epoch = "old"
        open = { """{"notifications":[${notifJson("fresh", "active", 2)}],"seq":3,"epoch":"new"}""" }
        onConnect = { n, ws ->
            if (n == 1) ws.send(ev(91, "other", "active").replace("\"created\"", "\"mark\""))
            ws.send("""{"type":"ready","seq":${if (n == 1) 91 else 3},"epoch":"new"}""")
        }
        start()
        await { wsSince.size >= 2 }
        assertEquals(setOf("fresh"), store.rows.keys)
        assertTrue(store.applies.none { c -> c.any { it.id == "other" } }) // the other database's mark is never saved, not even briefly
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

    @Test fun aNewEpochIsHandledLikeAReset() {
        store.epoch = "old"
        store.apply(listOf(notif("stale", "active", 90)), 90) // a cursor from the old database
        open = { """{"notifications":[${notifJson("fresh", "active", 2)}],"seq":3,"epoch":"new"}""" }
        // The new database happens to have events past our cursor, so the server sees nothing wrong and replays.
        onConnect = { n, ws ->
            if (n == 1) ws.send(ev(91, "other", "active"))
            ws.send("""{"type":"ready","seq":${if (n == 1) 91 else 3},"epoch":"new"}""")
        }
        start()
        await { wsSince.size >= 2 }
        assertEquals(listOf("90", "3"), wsSince.take(2))
        assertEquals(setOf("fresh"), store.rows.keys) // neither the stale row nor the replayed one
        assertEquals("new", store.epoch)
        assertEquals(1, lists.size)
    }

    @Test fun theFirstEpochSeenIsSavedWithoutAReset() {
        store.apply(listOf(notif("a", "active", 4)), 4)
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":4,"epoch":"e1"}""") }
        start()
        await { store.epoch == "e1" }
        assertTrue(lists.isEmpty() && store.rows.containsKey("a"))
    }

    @Test fun queuedActionsGoOutInOrderOnConnectAndA404IsDropped() {
        store.queue += Pending(1, "a", "dismiss")
        store.queue += Pending(2, "gone", "ack")
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":0}""") }
        start()
        await { store.queue.isEmpty() && SyncClient.Status.CONNECTED in statuses }
        assertEquals(listOf("/api/v1/notifications/a/dismiss", "/api/v1/notifications/gone/ack"), posts)
    }

    @Test fun aRefusedActionTakesTheServersRow() {
        store.rows["stale"] = notif("stale", "acknowledged", 4) // the tap's local guess
        store.queue += Pending(1, "stale", "reply", """{"reply":"yes","seq":4}""")
        current = { if (it == "stale") notifJson("stale", "active", 5) else null } // the question changed since
        getFails = 1 // the first fetch fails: the action stays queued and is tried again
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":0}""") }
        start()
        await { store.queue.isEmpty() && SyncClient.Status.CONNECTED in statuses }
        assertEquals(List(2) { "/api/v1/notifications/stale" }, gets)
        assertEquals("active", store.rows["stale"]!!.state)
    }

    @Test fun aRefusedApprovalSaysWhyAndIsNotRetried() { // #330: a 403 on a reply is about the tap, not the token
        store.rows["denied"] = notif("denied", "acknowledged", 4)
        store.queue += Pending(1, "denied", "reply", """{"reply":"yes","seq":4}""")
        current = { notifJson(it, "active", 4) }
        flushPending(store, Api(http, server.url("/").toString().trimEnd('/'), "tok"))
        assertEquals(emptyList<Pending>(), store.queue)
        assertEquals(listOf("approve_denied"), store.outcomes)
        assertEquals("active", store.rows["denied"]!!.state)
        assertEquals(listOf(true), store.quiet)
    }

    @Test fun aRefusedTypedAnswerSaysWhyQuietly() { // #153: a 400 is about the text, so it says so and never rings
        store.rows["badtext"] = notif("badtext", "acknowledged", 4)
        store.queue += Pending(1, "badtext", "reply", """{"reply":"other","text":"x","seq":4}""")
        current = { notifJson(it, "active", 4) }
        flushPending(store, Api(http, server.url("/").toString().trimEnd('/'), "tok"))
        assertEquals(emptyList<Pending>(), store.queue)
        assertEquals(listOf("bad_request"), store.outcomes)
        assertEquals("active", store.rows["badtext"]!!.state)
        assertEquals(listOf(true), store.quiet)
    }

    @Test fun aRefusedTapRetriedAfterAFailedRefetchSaysWhyOnce() { // #345
        store.rows["denied"] = notif("denied", "acknowledged", 4)
        store.queue += Pending(1, "denied", "reply", """{"reply":"yes","seq":4}""")
        current = { notifJson(it, "active", 4) }
        getFails = 1 // the refetch after the refusal fails once, so the tap stays queued and is sent again
        val api = Api(http, server.url("/").toString().trimEnd('/'), "tok")
        assertTrue(runCatching { flushPending(store, api) }.isFailure)
        assertEquals(1, store.queue.size)
        flushPending(store, api)
        assertEquals(emptyList<Pending>(), store.queue)
        assertEquals(listOf("approve_denied"), store.outcomes)
    }

    @Test fun aDevicesMessageTellsTheApp() { // #345: Settings reloads the device list when one connects or drops
        onConnect = { _, ws ->
            ws.send("""{"type":"devices","devices":[]}""")
            ws.send("""{"type":"ready","seq":0}""")
        }
        start()
        await { devices == 2 } // once for the message, once on ready (changes made while offline)
    }

    @Test fun anAskAnswerShowsThePromptAndKeepsTheQuestionOpen() { // #330: 202 with the confirm prompt
        store.rows["asked"] = notif("asked", "acknowledged", 4) // the tap's local guess
        store.queue += Pending(1, "asked", "reply", """{"reply":"yes","seq":4}""")
        current = { notifJson(it, "active", 4) }
        flushPending(store, Api(http, server.url("/").toString().trimEnd('/'), "tok"))
        assertEquals(listOf("confirm"), store.outcomes)
        assertEquals("active", store.rows["asked"]!!.state)
        assertEquals("active", store.rows["prompt"]!!.state)
        assertEquals(listOf("/api/v1/notifications/asked"), gets)
    }

    @Test fun aHungSendDoesNotHoldBackEvents() {
        store.queue += Pending(1, "slow", "dismiss")
        onConnect = { _, ws ->
            ws.send("""{"type":"ready","seq":0}""")
            ws.send(ev(7, "x", "dismissed")) // cleared on another device
        }
        start()
        await { store.rows["x"] != null }
        assertTrue("the event waited for the send", !slowAnswered)
        release.countDown()
        await { store.queue.isEmpty() }
    }

    @Test fun theAnswerToASendBeatsAnOlderEvent() {
        store.rows["a"] = notif("a", "active", 1)
        answer = { if (it.endsWith("/a/dismiss")) notifJson("a", "dismissed", 5) else "{}" }
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":2}""") }
        start()
        await { SyncClient.Status.CONNECTED in statuses }
        store.queue += Pending(1, "a", "dismiss")
        client!!.flushSoon()
        await { store.queue.isEmpty() }
        serverSockets.last().send(ev(3, "a", "active")) // the server's event from before the dismiss, delivered late
        serverSockets.last().send(ev(4, "y", "active"))
        await { store.rows["y"] != null }
        assertEquals("dismissed", store.rows["a"]!!.state)
        assertEquals(4L, store.lastSeq()) // the answer did not move the cursor
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

    @Test fun stopDuringASendSendsNothingMore() {
        store.queue += Pending(1, "slow", "dismiss")
        store.queue += Pending(2, "b", "snooze")
        start()
        await { posts.isNotEmpty() } // the first send is held open by the server
        client!!.stop() // a restart: the next client sends "b" itself, so this one must not send it again
        release.countDown()
        Thread.sleep(500)
        assertEquals(listOf("/api/v1/notifications/slow/dismiss"), posts)
    }

    @Test fun badTokenStopsRetrying() {
        wsResponse = { MockResponse().setResponseCode(401) }
        start()
        await { SyncClient.Status.AUTH_FAILED in statuses }
        Thread.sleep(300) // 15 backoff periods at 20 ms
        assertEquals(1, wsSince.size)
    }

    @Test fun unlinkedFromAnotherDeviceSignsOutAtOnce() {
        onConnect = { n, ws ->
            if (n == 1) {
                ws.send("""{"type":"ready","seq":0}""")
                ws.close(1013, "token revoked") // what the server sends when this phone is unlinked or its token revoked
            }
        }
        start()
        await { SyncClient.Status.AUTH_FAILED in statuses }
        Thread.sleep(300) // 15 backoff periods at 20 ms
        assertEquals(1, wsSince.size) // no retry with a token the server already refused
    }

    @Test fun serverDownRetriesWithBackoff() {
        wsResponse = { MockResponse().setResponseCode(503) }
        start()
        await { wsSince.size >= 3 }
        assertTrue(SyncClient.Status.OFFLINE in statuses)
    }

    @Test fun busyServerIsLeftAloneForItsRetryAfter() {
        wsResponse = { if (wsSince.size == 1) MockResponse().setResponseCode(429).setHeader("Retry-After", "1") else null }
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":0}""") }
        start()
        await { SyncClient.Status.BUSY in statuses }
        client!!.kick(force = true) // a network switch does not cut the wait short either
        await { SyncClient.Status.CONNECTED in statuses }
        // Measured, not slept: the 20 ms backoff alone would have retried within a few ms.
        assertTrue("retried too soon", wsAt[1] - wsAt[0] >= 1_000_000_000L)
    }

    @Test fun busySendWaitsForRetryAfter() {
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":0}""") }
        start()
        await { SyncClient.Status.CONNECTED in statuses }
        store.queue += Pending(1, "busy", "dismiss")
        client!!.flushSoon()
        await { posts.isNotEmpty() }
        store.queue += Pending(2, "b", "dismiss")
        client!!.flushSoon() // a new tap does not cut the wait short either
        await { store.queue.isEmpty() }
        assertTrue("sent again too soon", postAt[1] - postAt[0] >= 1_000_000_000L) // flushRetryMs is 50
        // The retry finds two dismisses queued and sends them as one clear (#108).
        assertEquals(listOf("/api/v1/notifications/busy/dismiss", "/api/v1/notifications/clear"), posts)
    }

    @Test fun theAnswerToOurOwnTapIsSavedQuietly() {
        answer = { notifJson("a", "active", 5) } // reopened from the inbox
        current = { notifJson(it, "active", 6) } // a row that changed since the tap
        store.queue += Pending(1, "a", "reopen")
        store.queue += Pending(2, "stale", "dismiss")
        flushPending(store, Api(http, server.url("/").toString().trimEnd('/'), "tok"))
        assertEquals(listOf(true, false), store.quiet)
    }

    @Test fun aSwipedStackGoesOutAsOneClear() {
        store.queue += Pending(1, "a", "dismiss")
        store.queue += Pending(2, "b", "dismiss")
        store.queue += Pending(3, "a", "reopen") // after the run: still its own call, after the clear
        flushPending(store, Api(http, server.url("/").toString().trimEnd('/'), "tok"))
        assertEquals(listOf("/api/v1/notifications/clear", "/api/v1/notifications/a/reopen"), posts)
        val clear = server.takeRequest()
        assertEquals("""{"ids":["a","b"]}""", clear.body.readUtf8())
        assertTrue(store.queue.isEmpty())
    }

    @Test fun anAckQueuedFirstDoesNotSplitAClear() { // #425
        store.queue += Pending(1, "a", "ack")
        store.queue += Pending(2, "b", "dismiss")
        store.queue += Pending(3, "c", "dismiss")
        flushPending(store, Api(http, server.url("/").toString().trimEnd('/'), "tok"))
        assertEquals(listOf("/api/v1/notifications/a/ack", "/api/v1/notifications/clear"), posts)
        assertTrue(store.queue.isEmpty())
    }

    @Test fun aClearRunKeepsOneVia() { // #267: each clear call is logged the way it was made
        store.queue += Pending(1, "a", "dismiss", """{"via":"tray_button"}""")
        store.queue += Pending(2, "b", "dismiss", """{"via":"tray_button"}""")
        store.queue += Pending(3, "c", "dismiss", """{"via":"app"}""") // another gesture: its own call
        flushPending(store, Api(http, server.url("/").toString().trimEnd('/'), "tok"))
        assertEquals(listOf("/api/v1/notifications/clear", "/api/v1/notifications/c/dismiss"), posts)
        assertEquals("""{"ids":["a","b"],"via":"tray_button"}""", server.takeRequest().body.readUtf8())
        assertEquals("""{"via":"app"}""", server.takeRequest().body.readUtf8())
    }

    @Test fun aFailedPartialSaveIsRetried() {
        store.failOnce = true
        onConnect = { n, ws -> if (n == 1) ws.send(ev(1, "a", "active")) } // and no `ready` yet
        start(replayQuietMs = 100)
        await { store.rows["a"] != null }
    }

    @Test fun aFullReplayBufferIsSavedQuietly() {
        onConnect = { n, ws ->
            if (n == 1) {
                (1..500L).forEach { ws.send(ev(it, "r$it", "active")) }
                ws.send(ev(501, "last", "active"))
                ws.send("""{"type":"ready","seq":501}""")
            }
        }
        start()
        await { SyncClient.Status.CONNECTED in statuses }
        assertEquals(listOf(true, true), store.quiet) // the rest of a replay that was already saved quietly stays quiet
    }

    @Test fun aSlowReplayIsSavedQuietlyAndOnlyReadyMeansInSync() {
        onConnect = { n, ws -> if (n == 1) ws.send(ev(1, "a", "active")) } // and no `ready` yet
        start(replayQuietMs = 100)
        await { store.rows["a"] != null }
        Thread.sleep(300)
        assertTrue(store.quiet.all { it })
        assertTrue("$statuses", SyncClient.Status.CONNECTED !in statuses)
        serverSockets.last().send("""{"type":"ready","seq":1}""")
        await { SyncClient.Status.CONNECTED in statuses }
    }

    @Test fun aReplayDroppedBeforeASaveDoesNotMoveTheCursor() {
        // Socket 1 replays 11..20, then drops with no `ready`. The close arrives after the events on the
        // same socket, so they are in the buffer when it drops; no timer race (#75). The quiet save is off.
        onConnect = { n, ws ->
            if (n == 1) { (11..20L).forEach { ws.send(ev(it, "r$it", "active")) }; ws.close(1001, "dropped") }
            else ws.send("""{"type":"ready","seq":0}""") // the new socket has nothing to replay
        }
        start(replayQuietMs = 600_000)
        await { SyncClient.Status.CONNECTED in statuses }
        assertEquals("0", wsSince[1])
        assertEquals(0L, store.lastSeq()) // events 11..20 must still be replayed
    }

    @Test fun anUnreadableEventReconnectsFromTheSameCursor() {
        onConnect = { n, ws ->
            if (n == 1) {
                ws.send("""{"type":"ready","seq":1}""")
                ws.send("""{"seq":2,"type":"created","notification":{}}""") // no id
                ws.send(ev(3, "c", "active")) // must not move the cursor past seq 2
            } else ws.send("""{"type":"ready","seq":3}""")
        }
        start()
        await { wsSince.size >= 2 }
        assertEquals("1", wsSince[1])
    }

    @Test fun anUnreadableOpenListIsRetried() {
        var calls = 0
        open = { if (++calls == 1) "not json" else """{"notifications":[],"seq":7}""" }
        onConnect = { n, ws -> if (n <= 2) ws.send("""{"type":"reset"}""") else ws.send("""{"type":"ready","seq":7}""") }
        start()
        await { store.lastSeq() == 7L && SyncClient.Status.CONNECTED in statuses }
        assertEquals(2, lists.size)
    }

    @Test fun aNetworkSwitchReconnectsALiveSocket() {
        onConnect = { _, ws -> ws.send("""{"type":"ready","seq":0}""") }
        start()
        await { SyncClient.Status.CONNECTED in statuses }
        client!!.kick() // the same network came back: keep the socket
        Thread.sleep(200)
        assertEquals(1, wsSince.size)
        client!!.kick(force = true)
        await { wsSince.size >= 2 }
    }

    private fun await(cond: () -> Boolean) {
        val end = System.currentTimeMillis() + 30_000 // a ceiling only: a loaded build box can be slow (#75)
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
        val quiet = CopyOnWriteArrayList<Boolean>()
        @Volatile var failOnce = false
        @Volatile private var seq = 0L

        override fun lastSeq() = seq
        val again = CopyOnWriteArrayList<Boolean>()
        @Synchronized override fun apply(notifs: Collection<Notif>, seq: Long, quiet: Boolean, again: Boolean) {
            if (failOnce) { failOnce = false; throw IllegalStateException("disk full") }
            applies += notifs
            this.quiet += quiet
            this.again += again
            for (n in notifs) if (n.seq >= (rows[n.id]?.seq ?: 0)) rows[n.id] = n
            this.seq = maxOf(this.seq, seq)
        }
        override fun replaceAll(open: List<Notif>, seq: Long, epoch: String) {
            rows.clear()
            open.forEach { rows[it.id] = it }
            this.seq = seq
            if (epoch.isNotEmpty()) this.epoch = epoch
        }
        @Volatile var epoch = ""
        override fun epoch() = epoch
        override fun saveEpoch(epoch: String) { this.epoch = epoch }
        val markIds = CopyOnWriteArrayList<String>()
        override fun saveMarkId(id: String) { markIds += id }
        override fun pending() = queue.toList()
        override fun removePending(p: Pending) { queue.remove(p) }
        val outcomes = CopyOnWriteArrayList<String>()
        override fun replyOutcome(p: Pending, outcome: String) { outcomes += outcome }
    }
}
