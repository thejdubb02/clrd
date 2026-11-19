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
class HttpError(val code: Int, msg: String, val retryAfterMs: Long? = null) : IOException("HTTP $code: $msg") {
    val isAuth get() = code == 401 || code == 403

    /** The server is turning us away for now (429, or 503 with a Retry-After), not down. */
    val isBusy get() = code == 429 || (code == 503 && retryAfterMs != null)

    /** 4xx other than auth and rate limit means the request itself is wrong (gone, already final): retrying will not help. */
    val isPermanent get() = code in 400..499 && !isAuth && code != 408 && code != 429
}

/** `Retry-After` in seconds, at most an hour. The rare HTTP-date form counts as absent. */
fun retryAfterMs(r: Response): Long? = r.header("Retry-After")?.trim()?.toLongOrNull()?.coerceIn(0, 3600)?.times(1000)

/** Blocking REST client for docs/SPEC.md section 5. Call off the main thread. */
class Api(private val http: OkHttpClient, baseUrl: String, private val token: String) {
    val base = baseUrl.trimEnd('/')

    fun post(path: String, body: String = "{}"): String =
        call(req(path).post(body.toRequestBody(JSON)))

    fun get(path: String): String = call(req(path).get())

    fun action(p: Pending) = post("/api/v1/notifications/${p.notifId}/${p.action}", p.body)

    /**
     * Every open row plus the seq to resume the WebSocket from (docs/SPEC.md section 5).
     * The seq comes from the first page: the server reads it before querying, so nothing is missed.
     */
    fun listOpen(): Pair<List<Notif>, Long> {
        val rows = mutableListOf<Notif>()
        var seq = -1L
        var before = ""
        while (true) {
            val o = JSONObject(get("/api/v1/notifications?state=open&limit=$PAGE" + if (before.isEmpty()) "" else "&before=$before"))
            if (seq < 0) seq = o.getLong("seq")
            val a = o.getJSONArray("notifications")
            for (i in 0 until a.length()) rows += Notif.fromJson(a.getJSONObject(i))
            if (a.length() < PAGE) return rows to seq
            before = rows.last().id
        }
    }

    fun wsRequest(since: Long): Request = req("/api/v1/ws?since=$since").build()

    private fun req(path: String) = Request.Builder().url(base + path).header("Authorization", "Bearer $token")

    private fun call(b: Request.Builder): String = http.newCall(b.build()).execute().use { r ->
        val text = r.body?.string().orEmpty()
        if (!r.isSuccessful) throw HttpError(r.code, runCatching { JSONObject(text).optString("error") }.getOrNull() ?: text.take(200), retryAfterMs(r))
        text
    }

    companion object {
        private val JSON = "application/json".toMediaType()
        const val PAGE = 500 // server max
    }
}
