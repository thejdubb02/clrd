package me.itsjustin.clrd

import android.app.DownloadManager
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.widget.Toast
import kotlinx.coroutines.asCoroutineDispatcher
import android.graphics.BitmapFactory
import android.util.LruCache
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.OkHttpClient
import okhttp3.Request
import okio.Buffer
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/**
 * Icons and picture attachments for the inbox and the tray. Loaded off the main thread with a size and time limit;
 * any failure means no picture, and the notification looks as it did before icons.
 */
class Images(private val app: App) {
    private val http by lazy { limits(app.http) }
    private val plainHttp by lazy { limits(app.plainHttp) } // a server past the first never gets account 0's certificate (#78)
    private fun limits(h: OkHttpClient) = h.newBuilder().callTimeout(15, TimeUnit.SECONDS).readTimeout(10, TimeUnit.SECONDS).build()
    /** "/api/v1/files/x" is another file on each server, so another account's pictures are cached apart (#78). */
    private fun key(px: Int, url: String, a: Account) = if (a.id == 0) "$px $url" else "${a.id} $px $url"
    /** Its own threads, so a slow image host never holds up sync or a tap. */
    val pool = Executors.newFixedThreadPool(2)
    private val cache = object : LruCache<String, Bitmap>(24 shl 20) {
        override fun sizeOf(key: String, value: Bitmap) = value.byteCount
    }
    private val failedAt = ConcurrentHashMap<String, Long>()
    private val loading = ConcurrentHashMap.newKeySet<String>()

    val dispatcher = pool.asCoroutineDispatcher()

    /**
     * Saves a file to Downloads. One on our server goes through DownloadManager with the token; an outside link
     * opens in the browser and never sees the token.
     */
    fun download(ctx: Context, f: Attachment) {
        val (base, token) = synchronized(app.configLock) { app.serverUrl to app.token }
        val req = request(f.url, base, token) ?: return
        if (!f.isOurs) {
            runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(f.url)).addCategory(Intent.CATEGORY_BROWSABLE)) }
            return
        }
        val name = f.name.replace(Regex("[/\\\\]"), "_").takeIf { it != "." && it != ".." } ?: "attachment"
        val r = DownloadManager.Request(Uri.parse(req.url.toString()))
            .addRequestHeader("Authorization", req.header("Authorization"))
            .setTitle(name)
            .setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
        // Shared Downloads needs a storage permission before Android 10; the app's own folder does not.
        if (Build.VERSION.SDK_INT >= 29) r.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, name)
        else r.setDestinationInExternalFilesDir(ctx, Environment.DIRECTORY_DOWNLOADS, name)
        runCatching { ctx.getSystemService(DownloadManager::class.java).enqueue(r) }
            .onSuccess { Toast.makeText(ctx, ctx.getString(R.string.downloading, name), Toast.LENGTH_SHORT).show() }
    }

    fun cached(url: String, px: Int, a: Account = app.account): Bitmap? = cache.get(key(px, url, a))

    /** Blocking. A failed URL is not tried again for 10 minutes. */
    fun load(url: String, px: Int, a: Account = app.account): Bitmap? {
        val key = key(px, url, a)
        cache.get(key)?.let { return it }
        if (System.currentTimeMillis() - (failedAt[key] ?: 0) < RETRY_MS) return null
        val (base, token) = synchronized(a.configLock) { a.serverUrl to a.token }
        val bmp = request(url, base, token)?.let { fetch(if (a.id == 0) http else plainHttp, it, MAX_BYTES) }?.let { decode(it, px) }
        if (bmp == null) failedAt[key] = System.currentTimeMillis() else cache.put(key, bmp)
        return bmp
    }

    /** Starts [load] in the background unless it is cached, running or failed lately. [done] runs once a new picture arrives. */
    fun prefetch(url: String, px: Int, a: Account = app.account, done: () -> Unit) {
        val key = key(px, url, a)
        if (cache.get(key) != null || System.currentTimeMillis() - (failedAt[key] ?: 0) < RETRY_MS || !loading.add(key)) return
        pool.execute {
            try {
                if (load(url, px, a) != null) done()
            } finally {
                loading.remove(key)
            }
        }
    }

    companion object {
        const val ICON_PX = 192 // a tray large icon at the densest screens
        const val PICTURE_PX = 1080
        const val MAX_BYTES = 5L shl 20
        private const val RETRY_MS = 10 * 60_000L

        /** "/api/v1/files/x" is on our server; "//host/x" and "/\\host/x" only look like it. */
        fun isServerPath(url: String) = url.startsWith("/") && !url.startsWith("//") && !url.startsWith("/\\")

        /**
         * The request for an image URL, or null if it may not be fetched. A path on our server ("/api/v1/files/...")
         * carries the token; an outside http(s) URL never does. "//host/x" is an outside URL in disguise and is refused.
         * OkHttp drops the Authorization header when a redirect leaves the server.
         */
        fun request(url: String, base: String, token: String): Request? {
            val ours = isServerPath(url)
            val outside = url.startsWith("https://", true) || url.startsWith("http://", true)
            val u = when {
                ours && base.isNotEmpty() -> (base.trimEnd('/') + url).toHttpUrlOrNull()
                outside -> url.toHttpUrlOrNull()
                else -> null
            } ?: return null
            return Request.Builder().url(u).apply { if (ours) header("Authorization", "Bearer $token") }.build()
        }

        /** The body, or null on any error or once more than [cap] bytes arrive (whatever Content-Length claims). */
        fun fetch(http: OkHttpClient, req: Request, cap: Long): ByteArray? = runCatching {
            http.newCall(req).execute().use { r ->
                val body = r.body
                if (!r.isSuccessful || body == null || body.contentLength() > cap) return null
                val buf = Buffer()
                val src = body.source()
                while (buf.size <= cap && src.read(buf, 8192) != -1L) Unit
                if (buf.size > cap) null else buf.readByteArray()
            }
        }.getOrNull()

        /** Decodes at most about [px] on the longer side, so a small file with huge dimensions cannot run out of memory. */
        fun decode(bytes: ByteArray, px: Int): Bitmap? {
            val o = BitmapFactory.Options().apply { inJustDecodeBounds = true }
            BitmapFactory.decodeByteArray(bytes, 0, bytes.size, o)
            if (o.outWidth <= 0 || o.outHeight <= 0) return null
            var sample = 1
            while (maxOf(o.outWidth, o.outHeight) / (sample * 2) >= px) sample *= 2
            val bmp = BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = sample }) ?: return null
            val long = maxOf(bmp.width, bmp.height)
            if (long <= px) return bmp
            return Bitmap.createScaledBitmap(bmp, maxOf(1, bmp.width * px / long), maxOf(1, bmp.height * px / long), true)
        }
    }
}
