package me.itsjustin.clrd

import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.MediaType.Companion.toMediaType
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException

/** HTTP error from the server. [code] is the HTTP status. */
class HttpError(val code: Int, msg: String, val retryAfterMs: Long? = null, val reason: String = "") : IOException("HTTP $code: $msg") {
    /** A refused approval tap (403 `approve_denied` or `own_request`) is about the tap, not the token. */
    val isAuth get() = code == 401 || (code == 403 && reason != "approve_denied" && reason != "own_request")

    /** The server is turning us away for now (429, or 503 with a Retry-After), not down. */
    val isBusy get() = code == 429 || (code == 503 && retryAfterMs != null)

    /** 4xx other than auth and rate limit means the request itself is wrong (gone, already final): retrying will not help. */
    val isPermanent get() = code in 400..499 && !isAuth && code != 408 && code != 429
}

/** `Retry-After` in seconds, at most an hour. The rare HTTP-date form counts as absent. */
fun retryAfterMs(r: Response): Long? = r.header("Retry-After")?.trim()?.toLongOrNull()?.coerceIn(0, 3600)?.times(1000)

/** The open rows, the seq to resume from, and the server's epoch ("" from a server older than epochs). */
data class OpenList(val rows: List<Notif>, val seq: Long, val epoch: String)

/** Blocking REST client for docs/SPEC.md section 5. Call off the main thread. */
class Api(private val http: OkHttpClient, baseUrl: String, private val token: String) {
    val base = baseUrl.trimEnd('/')

    fun post(path: String, body: String = "{}"): String =
        call(req(path).post(body.toRequestBody(JSON)))

    fun get(path: String): String = call(req(path).get())

    fun put(path: String, body: String): String = call(req(path).put(body.toRequestBody(JSON)))

    fun delete(path: String): String = call(req(path).delete())

    fun action(p: Pending) = post("/api/v1/notifications/${p.notifId}/${p.action}", snoozeWire(p.body))

    /**
     * Every open row plus the seq to resume the WebSocket from (docs/SPEC.md section 5).
     * The seq comes from the first page: the server reads it before querying, so nothing is missed.
     */
    fun listOpen(filter: String = ""): OpenList { // filter: more query, such as one topic with subscribed=all (#624)
        val rows = mutableListOf<Notif>()
        var seq = -1L
        var epoch = ""
        var before = ""
        while (true) {
            val o = JSONObject(get("/api/v1/notifications?state=open&limit=$PAGE$filter" + if (before.isEmpty()) "" else "&before=$before"))
            if (seq < 0) { seq = o.getLong("seq"); epoch = o.optString("epoch") }
            else if (o.optString("epoch") != epoch) throw IOException("server changed between pages") // retried from page one
            val a = o.getJSONArray("notifications")
            for (i in 0 until a.length()) rows += Notif.fromJson(a.getJSONObject(i))
            if (a.length() < PAGE) return OpenList(rows, seq, epoch)
            before = rows.last().id
        }
    }

    /** Name of the browser waiting on pairing [code] (issue #74). 404 when unknown, expired or used. */
    fun pairInfo(code: String): String = JSONObject(get("/api/v1/pair/$code")).getString("name")

    fun pairApprove(code: String) { post("/api/v1/pair/$code/approve") }

    fun pairDeny(code: String) { post("/api/v1/pair/$code/deny") }

    fun wsRequest(since: Long): Request = req("/api/v1/ws?since=$since").build()

    /** subscribed=mine: follow this token's topic choices even when it is a typed-in one with no user (#764). A path naming its own subscribed= keeps it. */
    private fun req(path: String) = Request.Builder()
        .url(base + path + if ("subscribed=" in path) "" else (if ('?' in path) "&" else "?") + "subscribed=mine")
        .header("Authorization", "Bearer $token")

    private fun call(b: Request.Builder): String = http.newCall(b.build()).execute().use { r ->
        val text = r.body?.string().orEmpty()
        if (!r.isSuccessful) {
            val o = runCatching { JSONObject(text) }.getOrNull()
            throw HttpError(r.code, o?.optString("error") ?: text.take(200), retryAfterMs(r), o?.optString("code").orEmpty())
        }
        text
    }

    companion object {
        private val JSON = "application/json".toMediaType()
        const val PAGE = 500 // server max
    }
}

/** The answer to one link claim poll (issue #74, Flow A). */
sealed interface Claim {
    data object Waiting : Claim
    data object Gone : Claim
    data class Approved(val token: String) : Claim
}

/**
 * Claims link [code] on [server] for this phone. No Authorization header: [server] came from a scan,
 * and this phone's token (if it has one) belongs to the server it is set up with.
 */
fun linkClaim(http: OkHttpClient, server: String, code: String, secret: String, name: String, install: String = "", detail: String = ""): Claim {
    val body = JSONObject().put("code", code).put("secret", secret).put("name", name).put("install", install).put("detail", detail).toString()
    val req = Request.Builder().url(server.trimEnd('/') + "/api/v1/link/claim").post(body.toRequestBody("application/json".toMediaType())).build()
    return http.newCall(req).execute().use { r ->
        val text = r.body?.string().orEmpty()
        when (r.code) {
            202 -> Claim.Waiting
            410 -> Claim.Gone
            200 -> JSONObject(text).optString("token").takeIf { it.isNotBlank() }?.let { Claim.Approved(it) }
                ?: throw HttpError(200, "no token in the answer")
            else -> throw HttpError(r.code, runCatching { JSONObject(text).optString("error") }.getOrNull() ?: text.take(200), retryAfterMs(r))
        }
    }
}
