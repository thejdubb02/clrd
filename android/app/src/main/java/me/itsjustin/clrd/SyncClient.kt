package me.itsjustin.clrd

import okhttp3.OkHttpClient
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
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

    /** Save these rows and the new last seq in one go, then update the tray. */
    fun apply(notifs: Collection<Notif>, seq: Long)

    /** After a `reset`: the open list from the server replaces everything known. */
    fun replaceAll(open: List<Notif>, seq: Long)

    fun pending(): List<Pending>
    fun removePending(p: Pending)
}

/**
 * Holds the WebSocket from docs/SPEC.md section 3.
 *
 * Every connect first sends queued user actions, then opens `/api/v1/ws?since=<last seq>`.
 * Replayed events are coalesced per notification until `ready`, so a phone that was
 * offline does not buzz for things that were already cleared elsewhere.
 * All state is touched only on [exec], one thread, so there are no locks.
 */
class SyncClient(
    private val http: OkHttpClient,
    private val api: Api,
    private val store: Store,
    private val onStatus: (Status) -> Unit = {},
    private val baseDelayMs: Long = 1_000,
    private val flushRetryMs: Long = 30_000,
    private val exec: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor(),
) {
    enum class Status { CONNECTING, CONNECTED, OFFLINE, AUTH_FAILED }

    private var ws: WebSocket? = null
    private var gen = 0 // bumps on every new socket; callbacks from an old one are ignored
    private var attempt = 0
    private var stopped = false
    private var retryTask: ScheduledFuture<*>? = null
    private var flushRetry: ScheduledFuture<*>? = null

    private var replaying = false
    private val buffer = LinkedHashMap<String, Notif>()
    private var bufferSeq = 0L

    fun start() { exec.execute { connect() } }

    fun stop() {
        exec.execute { stopped = true; gen++; ws?.close(1000, null); ws = null }
        exec.shutdown()
    }

    /** [stop], then wait until nothing more can be written to the store. Not for the main thread. */
    fun stopAndWait() {
        stop()
        exec.awaitTermination(15, TimeUnit.SECONDS)
    }

    /** The network came back: skip the rest of the backoff wait. */
    fun kick() {
        exec.execute {
            if (!stopped && ws == null) { retryTask?.cancel(false); attempt = 0; connect() }
        }
    }

    /** A user action was queued: try to send it now. A failure retries every 30 s, and on every connect. */
    fun flushSoon() {
        if (exec.isShutdown) return
        exec.execute {
            flushRetry?.cancel(false)
            try {
                flush()
            } catch (e: IOException) {
                // A rejected token will not fix itself; the next connect reports it as AUTH_FAILED.
                if (!stopped && !(e is HttpError && e.isAuth)) flushRetry = exec.schedule({ flushSoon() }, flushRetryMs, TimeUnit.MILLISECONDS)
            }
        }
    }

    private fun connect() {
        if (stopped) return
        onStatus(Status.CONNECTING)
        try {
            flush()
        } catch (e: IOException) {
            return fail(e)
        }
        val myGen = ++gen
        replaying = true
        buffer.clear()
        ws = http.newWebSocket(api.wsRequest(store.lastSeq()), object : WebSocketListener() {
            override fun onMessage(webSocket: WebSocket, text: String) = onThread(myGen) { handle(text) }
            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { webSocket.close(1000, null) }
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = onThread(myGen) { fail(IOException("closed $code")) }
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = onThread(myGen) {
                fail(response?.let { HttpError(it.code, "ws upgrade") } ?: IOException(t))
            }
        })
        // A server that never says `ready` must not leave events stuck in the buffer.
        exec.schedule({ if (myGen == gen && replaying) endReplay() }, 5, TimeUnit.SECONDS)
    }

    private fun onThread(myGen: Int, f: () -> Unit) {
        if (!exec.isShutdown) runCatching { exec.execute { if (myGen == gen && !stopped) f() } } // may lose a race with stop()
    }

    private fun handle(text: String) {
        val o = runCatching { JSONObject(text) }.getOrNull() ?: return
        when (o.optString("type")) {
            "created", "updated", "state" -> {
                val n = Notif.fromJson(o.getJSONObject("notification"))
                val seq = o.optLong("seq", n.seq)
                if (replaying) {
                    buffer.remove(n.id) // re-insert so order follows the latest event
                    buffer[n.id] = n
                    bufferSeq = maxOf(bufferSeq, seq)
                    if (buffer.size >= 500) applyBuffer()
                } else {
                    store.apply(listOf(n), seq)
                }
            }
            "ready" -> endReplay()
            "reset" -> reset()
            // Unknown types (pings, future additions) are ignored on purpose.
        }
    }

    private fun endReplay() {
        applyBuffer()
        replaying = false
        attempt = 0
        onStatus(Status.CONNECTED)
    }

    private fun applyBuffer() {
        if (buffer.isEmpty()) return
        store.apply(buffer.values.toList(), bufferSeq)
        buffer.clear()
    }

    /**
     * The server no longer has events back to our seq: take its open list as the truth.
     * The spec lets the socket stay open after `reset`, but a fresh connect from the list's seq
     * is simpler and also right for servers that close it.
     */
    private fun reset() {
        gen++
        ws?.close(1000, null)
        ws = null
        try {
            val (open, seq) = api.listOpen()
            store.replaceAll(open, seq)
        } catch (e: IOException) {
            return fail(e)
        }
        connect()
    }

    private fun flush() = flushPending(store, api)

    private fun fail(e: IOException) {
        ws = null
        gen++
        if (stopped) return
        if (e is HttpError && e.isAuth) {
            onStatus(Status.AUTH_FAILED) // a bad token will not fix itself; Settings restarts us
            return
        }
        onStatus(Status.OFFLINE)
        val delay = minOf(60_000L, baseDelayMs shl minOf(attempt, 6))
        attempt++
        retryTask = exec.schedule({ connect() }, delay / 2 + Random.nextLong(delay / 2 + 1), TimeUnit.MILLISECONDS)
    }
}

/**
 * Send queued user actions in order. A 4xx that retrying cannot fix (404, 409) drops the action;
 * anything else throws and leaves it queued.
 */
fun flushPending(store: Store, api: Api) {
    for (p in store.pending()) {
        try {
            api.action(p)
        } catch (e: HttpError) {
            if (!e.isPermanent) throw e
        }
        store.removePending(p)
    }
}
