package me.itsjustin.clrd

import okhttp3.OkHttpClient
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit
import kotlin.random.Random

/** Where synced state lives. Room in the app, a map in tests. */
interface Store {
    fun lastSeq(): Long

    /**
     * Save these rows and the new last seq in one go, then update the tray for the rows that were kept.
     * A row older than the one already saved is dropped. `seq = 0` leaves the cursor where it is.
     * [quiet] posts to the tray without a sound (part of a replay that is not finished).
     * [again] is a live `repeat` event: an active row alerts again even while it is showing.
     */
    fun apply(notifs: Collection<Notif>, seq: Long, quiet: Boolean = false, again: Boolean = false)

    /**
     * After a `reset`: the open list from the server replaces everything known, and [epoch] is saved.
     * When [epoch] differs from the saved one the list comes from another database, so no old row is kept.
     */
    fun replaceAll(open: List<Notif>, seq: Long, epoch: String = "")

    /** The id of the server database our seq belongs to (docs/SPEC.md section 3); "" when none is saved yet. */
    fun epoch(): String
    fun saveEpoch(epoch: String)

    fun pending(): List<Pending>
    fun removePending(p: Pending)

    /**
     * How a reply tap ended when it did not simply count (#330): `confirm` (sent to an allowed device
     * to confirm), `approve_denied`, `own_request` or `conflict` (the question changed).
     */
    fun replyOutcome(p: Pending, outcome: String) {}
}

/**
 * Holds the WebSocket from docs/SPEC.md section 3.
 *
 * Every connect opens `/api/v1/ws?since=<last seq>` and sends queued user actions alongside, on their
 * own thread, so a send that hangs or keeps failing never holds back events from other devices.
 * Replayed events are coalesced per notification until `ready`, so a phone that was
 * offline does not buzz for things that were already cleared elsewhere. A replay too long to
 * hold (500 rows, or 5 s) is saved in parts without a sound; only `ready` reports "in sync".
 * Socket state is touched only on [exec], one thread, so there are no locks.
 */
class SyncClient(
    private val http: OkHttpClient,
    private val api: Api,
    private val store: Store,
    private val onStatus: (Status) -> Unit = {},
    private val onSettings: (JSONObject) -> Unit = {},
    private val onDevices: () -> Unit = {},
    private val baseDelayMs: Long = 1_000,
    private val flushRetryMs: Long = 30_000,
    private val replayQuietMs: Long = 5_000,
    private val exec: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor(),
) {
    enum class Status { CONNECTING, CONNECTED, OFFLINE, BUSY, AUTH_FAILED }

    private var ws: WebSocket? = null
    private var gen = 0 // bumps on every new socket; callbacks from an old one are ignored
    private var attempt = 0
    @Volatile private var stopped = false
    private var retryTask: ScheduledFuture<*>? = null
    private val sender = Executors.newSingleThreadExecutor() // sends queued actions, in order
    private var flushRetry: ScheduledFuture<*>? = null // touched only on [sender]
    private var flushNotBefore = 0L // System.nanoTime(); a busy server's Retry-After for sends, touched only on [sender]
    private var connectNotBefore = 0L // the same for the socket, touched only on [exec]

    private var replaying = false
    private val buffer = LinkedHashMap<String, Notif>()
    private var bufferSeq = 0L
    private var replayQuiet = false // part of this replay was already saved quietly, so the rest is too

    /** Runs on the sync thread unless stopped. Once stop() has run, calls are dropped, never thrown. */
    private fun post(f: () -> Unit) { runCatching { exec.execute { if (!stopped) f() } } }

    private fun later(ms: Long, f: () -> Unit) = runCatching { exec.schedule({ if (!stopped) f() }, ms, TimeUnit.MILLISECONDS) }.getOrNull()

    fun start() = post { connect() }

    /** Safe to call more than once, from any thread. */
    fun stop() {
        stopped = true // set here, not on the thread, so nothing queued after this call can send
        runCatching { exec.execute { gen++; ws?.close(1000, null); ws = null } }
        exec.shutdown()
        sender.shutdown()
    }

    /**
     * The network came back: skip the rest of the backoff wait. [force] also drops a live socket,
     * for a switch (Wi-Fi to cellular) that leaves the old one half open.
     */
    fun kick(force: Boolean = false) {
        post {
            if (ws != null && !force) return@post
            if (ws == null && System.nanoTime() < connectNotBefore) return@post // the server asked us to wait
            ws?.cancel()
            ws = null
            retryTask?.cancel(false)
            attempt = 0
            connect()
        }
    }

    /** A user action was queued: try to send it now. A failure retries every 30 s, and on every connect. */
    fun flushSoon() {
        runCatching {
            sender.execute {
                if (stopped || System.nanoTime() < flushNotBefore) return@execute // a retry is already set for then
                flushRetry?.cancel(false)
                try {
                    flushPending(store, api) { !stopped }
                } catch (e: IOException) {
                    // A rejected token will not fix itself; the next connect reports it as AUTH_FAILED.
                    val wait = maxOf(flushRetryMs, (e as? HttpError)?.retryAfterMs ?: 0)
                    flushNotBefore = System.nanoTime() + ((e as? HttpError)?.retryAfterMs ?: 0) * 1_000_000
                    if (!stopped && !(e is HttpError && e.isAuth)) flushRetry = later(wait) { flushSoon() }
                }
            }
        }
    }

    private fun connect() {
        if (stopped) return
        onStatus(Status.CONNECTING)
        flushSoon()
        val myGen = ++gen
        replaying = true
        buffer.clear()
        replayQuiet = false
        bufferSeq = 0 // or a silent new socket would save the dropped replay's cursor with none of its rows
        ws = http.newWebSocket(api.wsRequest(store.lastSeq()), object : WebSocketListener() {
            override fun onMessage(webSocket: WebSocket, text: String) = onThread(myGen) {
                // A frame we cannot read must not be skipped: the cursor would move past it. Reconnect and replay.
                try { handle(text) } catch (e: Exception) { fail(e as? IOException ?: IOException("bad frame", e)) }
            }
            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { webSocket.close(1000, null) }
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = onThread(myGen) {
                // Unlinked or revoked: the server says so in the close reason, so report it now instead of after a retry.
                fail(if (reason == "token revoked") HttpError(401, reason) else IOException("closed $code"))
            }
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = onThread(myGen) {
                fail(response?.let { HttpError(it.code, "ws upgrade", retryAfterMs(it)) } ?: IOException(t))
            }
        })
        saveReplaySoon(myGen)
    }

    /** A slow replay, or a server that never says `ready`, must not leave events stuck in the buffer. */
    private fun saveReplaySoon(myGen: Int) {
        later(replayQuietMs) { if (myGen == gen && replaying) { saveReplaySoon(myGen); applyBuffer(quiet = true) } } // re-armed first, so a failed save is retried
    }

    private fun onThread(myGen: Int, f: () -> Unit) {
        post { if (myGen == gen && !stopped) f() }
    }

    private fun handle(text: String) {
        val o = JSONObject(text)
        when (o.optString("type")) {
            "created", "updated", "state", "repeat" -> {
                val n = Notif.fromJson(o.getJSONObject("notification"))
                val seq = o.optLong("seq", n.seq)
                if (replaying) {
                    buffer.remove(n.id) // re-insert so order follows the latest event
                    buffer[n.id] = n
                    bufferSeq = maxOf(bufferSeq, seq)
                    if (buffer.size >= 500) applyBuffer(quiet = true) // a later event in the replay may still clear these
                } else {
                    store.apply(listOf(n), seq, again = o.optString("type") == "repeat") // a missed repeat in a replay does not ring
                }
            }
            "ready" -> {
                // Checked before anything replayed is trusted: another epoch means the seq numbers
                // belong to another database, which is handled exactly like `reset`.
                val epoch = o.optString("epoch")
                val saved = store.epoch()
                if (epoch.isNotEmpty() && saved.isNotEmpty() && epoch != saved) return reset()
                if (epoch.isNotEmpty() && saved.isEmpty()) store.saveEpoch(epoch)
                // Events this token cannot see still move the cursor, or a filtered token
                // falls behind retention and resets on every reconnect.
                bufferSeq = maxOf(bufferSeq, o.optLong("seq"))
                endReplay()
                onDevices() // changes while offline send no devices message; the server expects a reload on ready
            }
            "reset" -> reset()
            "settings" -> o.optJSONObject("settings")?.let(onSettings) // sent on every connect and on every change
            "devices" -> onDevices() // a linked device connected, dropped or had its approve setting changed (#345)
            // Unknown types (pings, future additions) are ignored on purpose.
        }
    }

    private fun endReplay() {
        applyBuffer(quiet = replayQuiet)
        replaying = false
        attempt = 0
        onStatus(Status.CONNECTED)
    }

    private fun applyBuffer(quiet: Boolean = false) {
        if (buffer.isEmpty() && bufferSeq <= store.lastSeq()) return
        store.apply(buffer.values.toList(), bufferSeq, quiet)
        if (quiet && buffer.isNotEmpty()) replayQuiet = true
        buffer.clear()
    }

    /**
     * The server no longer has events back to our seq, or it runs on another database (a new
     * epoch): take its open list as the truth.
     * The spec lets the socket stay open after `reset`, but a fresh connect from the list's seq
     * is simpler and also right for servers that close it.
     */
    private fun reset() {
        gen++
        ws?.close(1000, null)
        ws = null
        try {
            val (open, seq, epoch) = api.listOpen()
            store.replaceAll(open, seq, epoch)
        } catch (e: IOException) {
            return fail(e)
        } // a malformed list throws past here to onMessage, which reconnects
        connect()
    }

    private fun fail(e: IOException) {
        ws?.cancel() // still open after a bad frame
        ws = null
        gen++
        if (stopped) return
        if (e is HttpError && e.isAuth) {
            onStatus(Status.AUTH_FAILED) // a bad token will not fix itself; Settings restarts us
            return
        }
        val busy = e is HttpError && e.isBusy
        onStatus(if (busy) Status.BUSY else Status.OFFLINE)
        val delay = minOf(60_000L, baseDelayMs shl minOf(attempt, 6))
        attempt++
        val wait = delay / 2 + Random.nextLong(delay / 2 + 1)
        connectNotBefore = System.nanoTime() + ((e as? HttpError)?.retryAfterMs ?: 0) * 1_000_000
        retryTask = later(maxOf(wait, (e as? HttpError)?.retryAfterMs ?: 0)) { connect() } // never sooner than the server asked
    }
}

private val flushLock = Any()

// Taps whose outcome message has shown, so a retry after a failed refetch does not show it again (#345).
// ponytail: in memory, so a restart between the two tries shows it once more; store it on Pending if that matters.
private val told = HashSet<Long>()

/** How a queued action was made ("" for one queued before it was recorded). */
private fun viaOf(p: Pending) = runCatching { JSONObject(p.body).optString("via") }.getOrDefault("")

/**
 * Send queued user actions in order. A 4xx that retrying cannot fix (404, 409) drops the action
 * and saves the server's current row; anything else, the fetch of that row included, throws and
 * leaves it queued.
 *
 * The server answers with the row after the change. It is saved before the action leaves the
 * queue, so an older event replayed after the send cannot bring a dismissed row back.
 * One sender at a time, process wide: a stopped client's sender stops at [live] instead of
 * sending again what its replacement already sent.
 */
fun flushPending(store: Store, api: Api, live: () -> Boolean = { true }) = synchronized(flushLock) {
    // Several dismisses at the head of the queue (a stack swiped away, #108) go out as one clear call.
    // Only the head run, so a later reopen of the same row still lands after its dismiss.
    // Only one via per run, so the server logs each clear the way it was made (#267).
    val head = store.pending()
    val via = head.firstOrNull()?.let(::viaOf)
    val run = head.takeWhile { it.action == "dismiss" && viaOf(it) == via }
    if (run.size > 1 && live()) {
        val sent = try {
            val body = JSONObject().put("ids", JSONArray(run.map { it.notifId }))
            if (!via.isNullOrEmpty()) body.put("via", via)
            api.post("/api/v1/notifications/clear", body.toString())
            true
        } catch (e: HttpError) {
            if (!e.isPermanent) throw e
            false // refused as a batch: the loop below sends them one by one
        }
        if (sent) run.forEach(store::removePending) // the server's events bring the rows themselves
    }
    for (p in store.pending()) {
        if (!live()) break
        var mine = true // the row after our own action: the person just tapped it, so no sound
        val answer = try {
            api.action(p).let { body ->
                // An ask device gets back the confirm prompt (202), not the question: the question stays unanswered.
                val prompt = p.action == "reply" && runCatching { JSONObject(body).optString("id") }.getOrNull().orEmpty().let { it.isNotEmpty() && it != p.notifId }
                if (!prompt) body else {
                    // Prompt and message first: a failed fetch below resends the tap, and the server answers it with the same prompt.
                    store.apply(listOf(Notif.fromJson(JSONObject(body))), 0, quiet = true)
                    if (told.add(p.key)) store.replyOutcome(p, "confirm")
                    try { api.get("/api/v1/notifications/${p.notifId}") } catch (g: HttpError) { if (!g.isPermanent) throw g; null }
                }
            }
        } catch (e: HttpError) {
            if (!e.isPermanent) throw e
            // A refused tap is still the person's own: the row goes back quietly, with a message saying why.
            if (p.action == "reply" && (e.code == 403 || e.code == 409)) { if (told.add(p.key)) store.replyOutcome(p, if (e.code == 409) "conflict" else e.reason) }
            else mine = false
            // Refused for good (409: it changed since the tap; 404: gone). The row shows our local guess,
            // so take the server's current row instead. A fetch that may work later throws and keeps the
            // action queued: the event that changed the row may already be behind the cursor.
            try { api.get("/api/v1/notifications/${p.notifId}") } catch (g: HttpError) { if (!g.isPermanent) throw g; null }
        }
        answer?.let { runCatching { Notif.fromJson(JSONObject(it)) }.getOrNull() }?.let { store.apply(listOf(it), 0, quiet = mine) }
        store.removePending(p)
        told.remove(p.key)
    }
}
