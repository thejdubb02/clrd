package me.itsjustin.clrd

import android.content.Context
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import org.json.JSONObject

/** One version's section of CHANGELOG.md: its sections in order, each with its one-line items (SPEC 5.2). */
internal data class Release(val version: String, val date: String, val changes: List<Pair<String, List<String>>>)

/** `GET /api/v1/version`, or this app's own copy when [commit] is null (the server was not reached). */
internal data class Versions(val version: String?, val commit: String?, val date: String, val history: List<Release>, val full: String)

internal const val FULL_HISTORY = "$REPO/blob/main/CHANGELOG.md"

/**
 * CHANGELOG.md read the way the server reads it: `## version - date` starts a version, `### Section` a section,
 * `- item` an item, and a wrapped line joins its item with a space. A section repeated under one version merges
 * into the first. Text before the first version is skipped.
 */
internal fun parseChangelog(md: String): List<Release> {
    val out = mutableListOf<Triple<String, String, LinkedHashMap<String, MutableList<String>>>>()
    var items: MutableList<String>? = null
    for (raw in md.lines()) {
        val line = raw.trimEnd()
        when {
            line.startsWith("## ") -> {
                val (v, d) = line.removePrefix("## ").split(" - ", limit = 2).let { it[0].trim() to it.getOrElse(1) { "" }.trim() }
                out += Triple(v, d, LinkedHashMap()); items = null
            }
            out.isEmpty() -> Unit
            line.startsWith("### ") -> items = out.last().third.getOrPut(line.removePrefix("### ").trim()) { mutableListOf() }
            line.startsWith("- ") -> items?.add(line.removePrefix("- ").trim())
            line.isBlank() -> Unit
            else -> items?.takeIf { it.isNotEmpty() }?.let { it[it.lastIndex] = it.last() + " " + line.trim() }
        }
    }
    return out.map { (v, d, s) -> Release(v, d, s.filterValues { it.isNotEmpty() }.map { it.key to it.value }) }.filter { it.changes.isNotEmpty() }
}

internal fun parseVersions(j: JSONObject): Versions? {
    if (j.optString("version").isEmpty()) return null // not a clrd version answer
    val history = j.optJSONArray("history")?.let { h ->
        (0 until h.length()).map { i ->
            val r = h.getJSONObject(i)
            val ch = r.optJSONArray("changes")
            Release(r.optString("version"), r.optString("date"), (0 until (ch?.length() ?: 0)).map { k ->
                val c = ch!!.getJSONObject(k)
                val it = c.optJSONArray("items")
                c.optString("section") to (0 until (it?.length() ?: 0)).map { n -> it!!.getString(n) }
            })
        }
    }.orEmpty()
    return Versions(j.optString("version"), j.optString("commit"), j.optString("date"), history, j.optString("full_history").ifEmpty { FULL_HISTORY })
}

/** The copy of the released history built into the app (res/raw/changelog, cut from CHANGELOG.md at build time). */
internal fun bundledHistory(ctx: Context): List<Release> =
    runCatching { ctx.resources.openRawResource(R.raw.changelog).bufferedReader().use { parseChangelog(it.readText()) } }.getOrDefault(emptyList())

/** Below 0 when [a] is the older version, above when newer; null when either is not plain numbers (`1.2.3`). */
internal fun compareVersions(a: String, b: String): Int? {
    fun parts(v: String): List<Int>? = v.trim().removePrefix("v").substringBefore('-').substringBefore('+').split('.').map { it.toIntOrNull() ?: return null }
    val x = parts(a) ?: return null
    val y = parts(b) ?: return null
    for (i in 0 until maxOf(x.size, y.size)) (x.getOrElse(i) { 0 }).compareTo(y.getOrElse(i) { 0 }).let { if (it != 0) return it }
    return 0
}

/** The line that says the app and server differ, or null when they match or cannot be compared. */
internal fun versionGap(ctx: Context, app: String, server: String?): String? {
    val c = compareVersions(app, server ?: return null) ?: return if (app.trim() != server.trim()) ctx.getString(R.string.about_differ) else null
    return when { c < 0 -> ctx.getString(R.string.about_server_newer); c > 0 -> ctx.getString(R.string.about_app_newer); else -> null }
}

/**
 * The after-update note (SPEC 5.2): true when this app's version is new since the last open and the note is on.
 * The first open ever, or any open with the note off, records the version quietly.
 */
internal fun updateNoteDue(app: App, version: String = BuildConfig.VERSION_NAME): Boolean {
    val seen = app.prefs.getString("seen_version", null)
    if (seen == null || !app.whatsNew) { if (seen != version) app.seenVersion(version); return false }
    return seen != version
}

@Composable
internal fun History(v: Versions) {
    val b = LocalBrand.current
    val uri = LocalUriHandler.current
    Column(verticalArrangement = Arrangement.spacedBy(10.dp), modifier = Modifier.testTag("whats-new")) {
        if (v.commit == null) Text(stringResource(R.string.whats_new_offline), color = b.slate, fontSize = 13.sp)
        v.history.forEach { r ->
            Text(
                if (r.version == "Unreleased") stringResource(R.string.whats_new_unreleased) else listOf(r.version, r.date).filter { it.isNotEmpty() }.joinToString("  "),
                color = b.ink, fontWeight = FontWeight.SemiBold, fontSize = 15.sp,
            )
            r.changes.forEach { (section, items) ->
                Text(section, style = Mono, color = b.slate)
                items.forEach { Text(Md.render("- $it", link = b.clear, muted = b.slate), color = b.body, fontSize = 14.sp) }
            }
        }
        TextButton(onClick = { runCatching { uri.openUri(v.full) } }) { Text(stringResource(R.string.whats_new_all), color = b.ink) }
    }
}

/** The server's version and history for [a], or this app's copy when it cannot be read. Blocks; call off the main thread. */
internal fun loadVersions(ctx: Context, a: Account): Versions =
    runCatching { parseVersions(JSONObject(a.api().get("/api/v1/version"))) }.getOrNull()
        ?: Versions(null, null, "", bundledHistory(ctx), FULL_HISTORY)

/** Settings, About: this app's and the shown server's version side by side, What's new, and the note's switch. */
@Composable
internal fun AboutBlock(app: App, a: Account, title: @Composable () -> Unit) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    var v by remember(a) { mutableStateOf<Versions?>(null) }
    var open by rememberSaveable { mutableStateOf(false) }
    var on by remember { mutableStateOf(app.whatsNew) }
    LaunchedEffect(a) { app.io.execute { v = loadVersions(ctx, a) } }
    title()
    Text(stringResource(R.string.about_app, BuildConfig.VERSION_NAME), color = b.body, fontSize = 14.sp)
    val got = v
    val server = got?.version
    val line = when {
        got == null -> stringResource(R.string.about_server_loading)
        server == null -> stringResource(R.string.about_server_unreached)
        else -> (listOf(stringResource(R.string.about_server, server)) + listOfNotNull(got.commit?.take(7), got.date.take(10)).filter { it.isNotEmpty() }).joinToString(" · ")
    }
    Text(line, color = b.body, fontSize = 14.sp)
    versionGap(ctx, BuildConfig.VERSION_NAME, server)?.let { Text(it, color = b.slate, fontSize = 13.sp) }
    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
        Text(stringResource(R.string.whats_new_note), color = b.body, fontSize = 14.sp, modifier = Modifier.weight(1f))
        Switch(checked = on, onCheckedChange = { on = it; app.whatsNew = it })
    }
    TextButton(onClick = { open = !open }) { Text(stringResource(R.string.whats_new), color = b.ink) }
    if (open) v?.let { History(it) }
}

/** The small note at the top of the inbox after the app updates, until dismissed or opened (SPEC 5.2). */
@Composable
internal fun UpdateNote(app: App) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    var due by remember { mutableStateOf(updateNoteDue(app)) }
    var shown by remember { mutableStateOf<Versions?>(null) }
    if (!due) return
    val done = { due = false; app.seenVersion(BuildConfig.VERSION_NAME) }
    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp).testTag("update-note")) {
        Text(stringResource(R.string.updated_to, BuildConfig.VERSION_NAME), color = b.ink, fontSize = 14.sp, modifier = Modifier.weight(1f))
        TextButton(onClick = { val a = app.shown.value; app.io.execute { shown = loadVersions(ctx, a) } }) { Text(stringResource(R.string.whats_new), color = b.ink) }
        TextButton(onClick = done) { Text(stringResource(R.string.dismiss), color = b.slate) }
    }
    shown?.let { v ->
        AlertDialog(
            onDismissRequest = { shown = null; done() },
            text = { Column(Modifier.heightIn(max = 480.dp).verticalScroll(rememberScrollState())) { History(v) } },
            confirmButton = { TextButton(onClick = { shown = null; done() }) { Text(stringResource(R.string.ok)) } },
        )
    }
}
