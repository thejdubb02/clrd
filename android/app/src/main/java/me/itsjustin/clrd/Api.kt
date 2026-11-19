package me.itsjustin.clrd

import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.MediaType.Companion.toMediaType
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException

/** HTTP error from the server. [code] is the HTTP status. */
class HttpError(val code: Int, msg: String) : IOException("HTTP $code: $msg") {
    val isAuth get() = code == 401 || code == 403

    /** 4xx other than auth and rate limit means the request itself is wrong (gone, already final): retrying will not help. */
    val isPermanent get() = code in 400..499 && !isAuth && code != 408 && code != 429
}

/** Blocking REST client for docs/SPEC.md section 5. Call off the main thread. */
class Api(private val http: OkHttpClient, baseUrl: String, private val token: String) {
    val base = baseUrl.trimEnd('/')

    fun post(path: String, body: String = "{}"): String =
        call(req(path).post(body.toRequestBody(JSON)))

    fun get(path: String): String = call(req(path).get())

    fun action(p: Pending) = post("/api/v1/notifications/${p.notifId}/${p.action}", p.body)

    /** Current event seq, from /healthz. */
    fun currentSeq(): Long = JSONObject(get("/healthz")).getLong("seq")

    // ponytail: one page of 1000 open rows. Paginate with `before` if anyone has more.
    fun listOpen(): List<Notif> {
        // The spec does not fix the envelope yet: accept a bare array or {"notifications": [...]}.
        val text = get("/api/v1/notifications?state=open&limit=1000").trim()
        val a = if (text.startsWith("[")) JSONArray(text) else JSONObject(text).optJSONArray("notifications") ?: return emptyList()
        return (0 until a.length()).map { Notif.fromJson(a.getJSONObject(it)) }
    }

    fun wsRequest(since: Long): Request = req("/api/v1/ws?since=$since").build()

    private fun req(path: String) = Request.Builder().url(base + path).header("Authorization", "Bearer $token")

    private fun call(b: Request.Builder): String = http.newCall(b.build()).execute().use { r ->
        val text = r.body?.string().orEmpty()
        if (!r.isSuccessful) throw HttpError(r.code, runCatching { JSONObject(text).optString("error") }.getOrNull() ?: text.take(200))
        text
    }

    companion object {
        private val JSON = "application/json".toMediaType()
    }
}
