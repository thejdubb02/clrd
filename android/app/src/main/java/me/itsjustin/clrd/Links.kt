package me.itsjustin.clrd

import android.content.ClipData
import android.content.ClipboardManager
import androidx.compose.foundation.Image
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.text.InlineTextContent
import androidx.compose.foundation.text.selection.DisableSelection
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.material.icons.materialIcon
import androidx.compose.material.icons.materialPath
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.Placeholder
import androidx.compose.ui.text.PlaceholderVerticalAlign
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import android.net.Uri

private const val FAVICON_PX = 48

/**
 * A card's message (#107). With [Md.render]'s icons on, each link has its site's icon before it, fetched through our
 * server as on the web, or a plain link icon when the site has none. A long press on a link shows the whole address
 * before anything opens; a tap still opens it, and a press that moves is left to the list's scroll and swipe.
 */
@Composable
internal fun MessageText(text: AnnotatedString, maxLines: Int, modifier: Modifier = Modifier) {
    val b = LocalBrand.current
    val icons = remember(text) {
        text.getLinkAnnotations(0, text.length).mapNotNull { (it.item as? LinkAnnotation.Url)?.url?.let(Md::host) }.distinct()
            .associate { host -> Md.ICON + host to InlineTextContent(Placeholder(1.3.em, 1.em, PlaceholderVerticalAlign.TextCenter)) { Favicon(host) } }
    }
    var layout by remember { mutableStateOf<TextLayoutResult?>(null) }
    var shown by remember { mutableStateOf<String?>(null) }
    val uri = LocalUriHandler.current
    var onLink by remember { mutableStateOf(false) }
    val body = @Composable {
        Text(text, color = b.body, fontSize = 15.sp, maxLines = maxLines, overflow = TextOverflow.Ellipsis, inlineContent = icons, onTextLayout = { layout = it })
    }
    // On the Box, not the Text: the Text is swapped below while a finger is on a link, and this must outlive that.
    Box(modifier.pointerInput(text) {
        awaitEachGesture {
            // Initial pass: this sees the press before the card and the link do. A press that moves past the slop goes
            // back to the list's scroll and swipe, so nothing is consumed until the press is known to be ours.
            val down = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Initial)
            val url = layout?.let { linkAt(text, it, down.position) } ?: return@awaitEachGesture
            onLink = true // an open card's text selection would start on the same long press: off until the lift
            try {
                val tapped = withTimeoutOrNull(viewConfiguration.longPressTimeoutMillis) {
                    var c = down
                    do c = awaitPointerEvent(PointerEventPass.Initial).changes.firstOrNull { it.id == down.id } ?: return@withTimeoutOrNull false
                    while (c.pressed && (c.position - down.position).getDistance() <= viewConfiguration.touchSlop)
                    if (c.pressed) false else { c.consume(); true }
                }
                when (tapped) {
                    true -> runCatching { uri.openUri(url) } // ours, as the link's own click leaves with the swapped Text
                    false -> {}
                    null -> {
                        shown = url
                        do { // the lift after a long press opens nothing
                            val e = awaitPointerEvent(PointerEventPass.Initial)
                            e.changes.forEach { it.consume() }
                        } while (e.changes.any { it.pressed })
                    }
                }
            } finally {
                onLink = false
            }
        }
    }) { if (onLink) DisableSelection { body() } else body() }
    shown?.let { url ->
        val ctx = LocalContext.current
        AlertDialog(
            onDismissRequest = { shown = null },
            text = { SelectionContainer { Text(url, color = b.body) } },
            confirmButton = { TextButton({ shown = null; runCatching { uri.openUri(url) } }) { Text(stringResource(R.string.open_link)) } },
            dismissButton = {
                TextButton({
                    shown = null
                    ctx.getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText(url, url))
                }) { Text(stringResource(R.string.copy)) }
            },
        )
    }
}

/** The address of the link drawn at [p], or null when [p] is beside the text rather than on a link. */
internal fun linkAt(text: AnnotatedString, layout: TextLayoutResult, p: Offset): String? {
    val near = layout.getOffsetForPosition(p) // the nearest gap between characters, so the character may be either side
    val i = listOf(near - 1, near).firstOrNull { it in text.indices && layout.getBoundingBox(it).contains(p) } ?: return null
    return text.getLinkAnnotations(i, i + 1).firstNotNullOfOrNull { (it.item as? LinkAnnotation.Url)?.url }
}

@Composable
private fun Favicon(host: String) {
    // The server answers a site without an icon with a drawing Android cannot decode, so that also shows the link icon.
    val pic = rememberPicture("/api/v1/favicon?host=" + Uri.encode(host), FAVICON_PX)
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.CenterStart) {
        val m = Modifier.fillMaxHeight().aspectRatio(1f)
        if (pic != null) Image(pic, null, m) else Icon(LinkIcon, null, m, tint = LocalBrand.current.slate)
    }
}

// Material's link icon, drawn in code: a drawable file would pull in Android's vector file reader (about 30 KB).
private val LinkIcon = materialIcon("link") {
    materialPath {
        moveTo(3.9f, 12f)
        curveToRelative(0f, -1.71f, 1.39f, -3.1f, 3.1f, -3.1f)
        horizontalLineToRelative(4f)
        verticalLineTo(7f)
        horizontalLineTo(7f)
        curveToRelative(-2.76f, 0f, -5f, 2.24f, -5f, 5f)
        reflectiveCurveToRelative(2.24f, 5f, 5f, 5f)
        horizontalLineToRelative(4f)
        verticalLineToRelative(-1.9f)
        horizontalLineTo(7f)
        curveToRelative(-1.71f, 0f, -3.1f, -1.39f, -3.1f, -3.1f)
        close()
        moveTo(8f, 13f)
        horizontalLineToRelative(8f)
        verticalLineToRelative(-2f)
        horizontalLineTo(8f)
        close()
        moveTo(17f, 7f)
        horizontalLineToRelative(-4f)
        verticalLineToRelative(1.9f)
        horizontalLineToRelative(4f)
        curveToRelative(1.71f, 0f, 3.1f, 1.39f, 3.1f, 3.1f)
        reflectiveCurveToRelative(-1.39f, 3.1f, -3.1f, 3.1f)
        horizontalLineToRelative(-4f)
        verticalLineTo(17f)
        horizontalLineToRelative(4f)
        curveToRelative(2.76f, 0f, 5f, -2.24f, 5f, -5f)
        reflectiveCurveToRelative(-2.24f, -5f, -5f, -5f)
        close()
    }
}
