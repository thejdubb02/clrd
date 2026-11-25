package me.itsjustin.clrd

import android.graphics.Bitmap
import android.net.Uri
import android.util.Base64
import android.util.LruCache
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.withContext
import org.json.JSONObject

/** A link preview (#107, docs/formatting.md): the server fetched the page, so the phone never contacts the site. */
internal data class LinkPreview(val url: String, val title: String, val description: String, val site: String, val image: Bitmap?) {
    companion object {
        // Sized by bytes: a few full-width pictures, not 32 of them (each can be several MB).
        private val cache = object : LruCache<String, LinkPreview>(16 shl 20) { override fun sizeOf(key: String, p: LinkPreview) = (p.image?.byteCount ?: 0) + 1024 }

        /** The first web link in a rendered message, as the web inbox picks it. */
        fun firstLink(text: AnnotatedString) = text.getLinkAnnotations(0, text.length)
            .firstNotNullOfOrNull { (it.item as? LinkAnnotation.Url)?.url?.takeIf { u -> u.startsWith("https://", true) || u.startsWith("http://", true) } }

        /** The server's preview of [url] for [topic], or null when it has none (off, no title, no connection). Call off the main thread. */
        fun load(app: App, url: String, topic: String): LinkPreview? {
            val key = "${app.serverUrl} ${app.token.hashCode()} $topic $url" // another server or account never sees this one's
            cache.get(key)?.let { return it }
            val o = runCatching { JSONObject(app.api().get("/api/v1/preview?url=" + Uri.encode(url) + "&topic=" + Uri.encode(topic))) }.getOrNull() ?: return null
            val title = o.optString("title")
            if (title.isEmpty()) return null
            return LinkPreview(url, title, o.optString("description"), o.optString("site_name"), picture(o.optString("image"))).also { cache.put(key, it) }
        }

        /** Only a data: image, as the server sends it; anything else is never fetched. */
        internal fun picture(data: String): Bitmap? {
            if (!data.startsWith("data:image/", true)) return null
            val comma = data.indexOf(',').takeIf { it > 0 && data.substring(0, it).endsWith(";base64", true) } ?: return null
            return runCatching { Images.decode(Base64.decode(data.substring(comma + 1), Base64.DEFAULT), Images.PICTURE_PX) }.getOrNull()
        }
    }
}

/** The preview card under an open message, while previews are on for its topic. Tap opens the page. */
@Composable
internal fun PreviewCard(n: Notif, text: AnnotatedString) {
    val app = App.of(LocalContext.current)
    val b = LocalBrand.current
    val url = remember(text) { LinkPreview.firstLink(text) } ?: return
    var p by remember(url, n.topic) { mutableStateOf<LinkPreview?>(null) }
    LaunchedEffect(url, n.topic) { p = withContext(app.images.dispatcher) { LinkPreview.load(app, url, n.topic) } }
    val shown = p ?: return
    val uri = LocalUriHandler.current
    Surface(
        onClick = { runCatching { uri.openUri(shown.url) } }, color = b.paper, shape = RoundedCornerShape(10.dp), border = BorderStroke(1.dp, b.line),
        modifier = Modifier.padding(top = 8.dp, end = 8.dp).fillMaxWidth().testTag("preview"),
    ) {
        Column {
            shown.image?.let { Image(remember(it) { it.asImageBitmap() }, null, contentScale = ContentScale.Crop, modifier = Modifier.fillMaxWidth().heightIn(max = 160.dp)) }
            Column(Modifier.padding(10.dp)) {
                if (shown.site.isNotEmpty()) Text(shown.site, color = b.slate, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(shown.title, color = b.ink, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, maxLines = 2, overflow = TextOverflow.Ellipsis)
                if (shown.description.isNotEmpty()) Text(shown.description, color = b.body, fontSize = 13.sp, maxLines = 3, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}
