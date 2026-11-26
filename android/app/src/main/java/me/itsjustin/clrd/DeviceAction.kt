package me.itsjustin.clrd

import android.content.Context
import android.content.Intent
import android.os.Handler
import android.os.Looper
import android.widget.Toast
import okhttp3.Call
import okhttp3.Callback
import okhttp3.Headers.Companion.toHeaders
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.TimeUnit

/**
 * ntfy's http and broadcast buttons (#401, #449). The server keeps them off a topic until an admin turns
 * device_actions on, so the phone runs what it gets. The tap carries only the row id, its seq and the button's
 * index: the request itself is read back from Room, so headers and body never sit in a PendingIntent.
 */
object DeviceAction {
    /** ntfy's default, so a Tasker profile made for ntfy picks clrd's broadcasts up unchanged. */
    const val DEFAULT_INTENT = "io.heckel.ntfy.USER_ACTION"
    private val METHODS = setOf("GET", "POST", "PUT", "PATCH", "DELETE", "HEAD")

    fun isDevice(o: JSONObject) = o.optJSONObject("http") != null || o.optJSONObject("broadcast") != null

    /** The request an http button sends, or null if it is not one we run (the server checked it too). */
    fun request(o: JSONObject): Request? {
        val h = o.optJSONObject("http") ?: return null
        val url = h.optString("url").takeIf { it.startsWith("https://", true) || it.startsWith("http://", true) }?.toHttpUrlOrNull() ?: return null
        val method = h.optString("method").uppercase().ifEmpty { "POST" }.takeIf { it in METHODS } ?: return null
        val headers = h.optJSONObject("headers")?.let { hs -> hs.keys().asSequence().associateWith { hs.optString(it) } }.orEmpty()
        val body = when (method) {
            "GET", "HEAD" -> null
            else -> h.optString("body").toRequestBody() // empty is fine: OkHttp needs a body for POST/PUT/PATCH
        }
        return runCatching { Request.Builder().url(url).headers(headers.toHeaders()).method(method, body).build() }.getOrNull()
    }

    /** The broadcast a broadcast button sends; string extras only, as ntfy. */
    fun intent(o: JSONObject): Intent? {
        val b = o.optJSONObject("broadcast") ?: return null
        val i = Intent(b.optString("intent").ifEmpty { DEFAULT_INTENT })
        b.optJSONObject("extras")?.let { e -> e.keys().forEach { i.putExtra(it, e.optString(it)) } }
        return i
    }

    /**
     * Runs button [index] of row [id] as shown at [seq]. Clears the row everywhere when the button asks and it worked;
     * a failure says so in a toast and leaves the row. [done] runs once, on any path.
     */
    fun run(ctx: Context, acct: Account, id: String, index: Int, seq: Long, server: String?, done: () -> Unit) {
        val app = App.of(ctx)
        // acts, not io: io may be blocked on the network, and this must fit the receiver's 10 s budget.
        app.acts.execute {
            val o = runCatching {
                acct.db.dao().get(id)?.takeIf { it.seq == seq && !acct.removed && (server == null || server == acct.serverUrl) }
                    ?.let { TrayButton.objs(it).getOrNull(index) }?.takeIf(::isDevice)
            }.getOrNull()
            if (o == null) { toast(app, app.getString(R.string.device_changed)); done(); return@execute }
            val label = o.optString("label").ifBlank { app.getString(R.string.tray_open) }
            fun finish(ok: Boolean) {
                if (!ok) toast(app, app.getString(R.string.device_failed, label))
                // Only the version tapped: a row edited while the request ran is left for the person to see.
                val clear = ok && o.optBoolean("clear") && runCatching { acct.db.dao().get(id)?.seq == seq }.getOrDefault(false)
                if (clear) acct.actAll(listOf(id), "dismiss", "{}", server, App.VIA_TRAY_BUTTON) { done() } else done()
            }
            intent(o)?.let { i ->
                finish(runCatching { app.sendBroadcast(i) }.isSuccess)
                return@execute
            }
            val req = request(o) ?: return@execute finish(false)
            // A bare client: the clrd token is never sent to a sender's address, and no redirect carries the
            // sender's headers to another host. 8 s leaves room in the receiver's budget to save a clear.
            acct.http.newBuilder().pingInterval(0, TimeUnit.SECONDS).followRedirects(false).followSslRedirects(false)
                .callTimeout(8, TimeUnit.SECONDS).build()
                .newCall(req).enqueue(object : Callback {
                    override fun onFailure(call: Call, e: IOException) = finish(false)
                    override fun onResponse(call: Call, response: Response) = response.use { finish(it.isSuccessful) }
                })
        }
    }

    private fun toast(ctx: Context, text: String) =
        Handler(Looper.getMainLooper()).post { Toast.makeText(ctx, text, Toast.LENGTH_LONG).show() }
}
