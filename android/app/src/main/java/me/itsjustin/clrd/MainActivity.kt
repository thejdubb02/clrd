package me.itsjustin.clrd

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.media.RingtoneManager
import android.provider.Settings
import android.widget.Toast
import androidx.compose.foundation.Image
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.DateRange
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Share
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.SnackbarResult
import androidx.compose.material3.Surface
import androidx.compose.material3.SwipeToDismissBox
import androidx.compose.material3.SwipeToDismissBoxValue
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.text.DateFormat
import java.util.Date
import kotlin.concurrent.thread

/**
 * Null when the phone already lets clrd run in the background, else the system prompt that allows it.
 * Some phones stop a foreground service anyway when battery optimisation is on, and then clears
 * from other devices stop arriving.
 */
fun batteryPrompt(ctx: Context): Intent? =
    if (ctx.getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(ctx.packageName)) null
    else Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:${ctx.packageName}"))

/** Opens the prompt. Some builds have no such dialog; the list of apps then lets the person do it by hand. */
fun openBattery(ctx: Context, prompt: Intent) {
    try {
        ctx.startActivity(prompt)
    } catch (e: ActivityNotFoundException) {
        runCatching { ctx.startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)) }
    }
}

class MainActivity : ComponentActivity() {
    /** Rechecked on every resume, so a change made in Android's settings shows when the person comes back. */
    val battery = mutableStateOf<Intent?>(null)
    /** Counts resumes, so Settings re-reads what the phone's own settings changed meanwhile (the sound). */
    val resumes = mutableStateOf(0)

    /** The browser sign-in or phone link in progress, if any. */
    val pair = mutableStateOf<PairUi?>(null)

    /** In-app QR scanner (zxing, no Google services). Null contents means the person backed out. */
    private val scanner = registerForActivityResult(ScanContract()) { r ->
        r.contents?.let { route(parseScan(it), getString(R.string.scan_not_clrd)) }
    }

    private fun scan() = scanner.launch(
        ScanOptions().setDesiredBarcodeFormats(ScanOptions.QR_CODE).setPrompt(getString(R.string.scan_prompt))
            .setBeepEnabled(false).setOrientationLocked(false),
    )

    override fun onResume() {
        super.onResume()
        battery.value = batteryPrompt(this)
        resumes.value++
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        if (Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        val app = App.of(this)
        if (app.configured) SyncService.start(this)
        // Not again after rotation or from recents: the code was used already and would read as expired.
        if (savedInstanceState == null && (intent.flags and Intent.FLAG_ACTIVITY_LAUNCHED_FROM_HISTORY) == 0) handlePair(intent)
        setContent {
            ClrdTheme {
                Home(app, battery.value, resumes.value, ::scan)
                PairDialog(pair.value, { pair.value = it }, ::approvePair, ::claimLink)
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handlePair(intent)
    }

    private fun handlePair(intent: Intent?) {
        if (intent?.action != Intent.ACTION_VIEW) return
        route(parseScan(intent.dataString ?: return), getString(R.string.pair_bad_link))
    }

    /** One way in for scanned codes and opened links, so both get the same checks. */
    private fun route(scan: Scan, bad: String) {
        when (scan) {
            is Scan.Pair -> pairWith(scan.link)
            // Nothing is sent until the person has seen the server address and said Yes.
            is Scan.Link -> App.of(this).let { app ->
                pair.value = PairUi.Confirm(scan.link, app.serverUrl.takeIf { app.configured })
            }
            Scan.Bad -> pair.value = PairUi.Note(bad)
        }
    }

    /**
     * Claims the link on the scanned server, polling until the browser approves, denies or the code expires.
     * On its own thread: two minutes on [App.io] would hold up Settings.
     */
    private fun claimLink(link: LinkCode) {
        val app = App.of(this)
        val secret = newLinkSecret() // one per attempt, sent on every poll
        val waiting = PairUi.Waiting(link, linkCheck(secret))
        pair.value = waiting
        val name = deviceName(this)
        thread(isDaemon = true) {
            val deadline = System.nanoTime() + LINK_TTL_NS
            var outcome: Claim = Claim.Gone
            var error: Exception? = null
            while (pair.value === waiting && System.nanoTime() < deadline) {
                try {
                    outcome = linkClaim(app.http, link.server, link.code, secret, name)
                    error = null
                    if (outcome != Claim.Waiting) break
                } catch (e: Exception) {
                    error = e
                    if (!claimRetries(e)) break // otherwise keep trying until the code expires
                }
                Thread.sleep(LINK_POLL_MS)
            }
            if (pair.value !== waiting) return@thread // closed by the person: the code just expires
            val got = outcome
            if (got is Claim.Approved) {
                app.io.execute {
                    app.saveServer(link.server, got.token)
                    SyncService.start(this, restart = true)
                    pair.value = PairUi.Note(getString(R.string.link_done))
                }
            } else {
                // A failed last attempt says why; otherwise it was refused, denied or ran out of time.
                pair.value = PairUi.Note(error?.let { pairError(it) } ?: getString(R.string.link_expired))
            }
        }
    }

    private fun pairWith(link: PairLink) {
        val app = App.of(this)
        // One snapshot: the gate and every request use the same server and token, never the link's server.
        val url = app.serverUrl
        val token = app.token
        when (pairGate(link, url, token)) {
            PairGate.NOT_SET_UP -> pair.value = PairUi.Note(getString(R.string.pair_not_set_up))
            PairGate.OTHER_SERVER -> pair.value = PairUi.Note(getString(R.string.pair_other_server, serverAddress(link.server)))
            PairGate.OK -> {
                val api = Api(app.http, url, token)
                pair.value = null
                app.io.execute {
                    pair.value = try {
                        PairUi.Ask(api, link.code, api.pairInfo(link.code), url)
                    } catch (e: Exception) {
                        PairUi.Note(pairError(e))
                    }
                }
            }
        }
    }

    private fun approvePair(ask: PairUi.Ask) {
        pair.value = null // closed first, so a second tap cannot send it again
        App.of(this).io.execute {
            pair.value = PairUi.Note(
                try {
                    ask.api.pairApprove(ask.code)
                    getString(R.string.pair_done)
                } catch (e: Exception) {
                    pairError(e)
                },
            )
        }
    }

    private fun pairError(e: Exception) = when {
        e is HttpError && e.code == 404 -> getString(R.string.pair_expired)
        e is HttpError && e.isAuth -> getString(R.string.test_token_rejected)
        e is HttpError -> getString(R.string.test_http_error, e.code)
        else -> getString(R.string.test_unreachable, e.message)
    }

    private companion object {
        const val LINK_TTL_NS = 125_000_000_000L // 2 minutes, plus a poll or two after an approval near expiry (#184)
        const val LINK_POLL_MS = 1_500L
    }
}

/** What the sign-in dialog shows: a question to approve, or a one-line result. */
sealed interface PairUi {
    class Ask(val api: Api, val code: String, val name: String, val server: String) : PairUi
    class Note(val text: String) : PairUi
    /** Link this phone to [link]'s server? [replaces] is the server it is set up with now, if any. */
    class Confirm(val link: LinkCode, val replaces: String?) : PairUi
    class Waiting(val link: LinkCode, val check: String) : PairUi
}

@Composable
private fun PairDialog(ui: PairUi?, set: (PairUi?) -> Unit, approve: (PairUi.Ask) -> Unit, claim: (LinkCode) -> Unit) {
    val app = App.of(LocalContext.current)
    when (ui) {
        null -> Unit
        is PairUi.Note -> AlertDialog(
            onDismissRequest = { set(null) },
            text = { Text(ui.text) },
            confirmButton = { TextButton(onClick = { set(null) }) { Text(stringResource(R.string.ok)) } },
        )
        // Back or a tap outside closes it without approving; the code then just expires.
        is PairUi.Ask -> AlertDialog(
            onDismissRequest = { set(null) },
            title = { Text(stringResource(R.string.pair_title)) },
            text = { Text(stringResource(R.string.pair_text, ui.name, ui.server)) },
            confirmButton = { TextButton(onClick = { approve(ui) }) { Text(stringResource(R.string.pair_approve)) } },
            dismissButton = {
                TextButton(onClick = {
                    set(null)
                    app.io.execute { runCatching { ui.api.pairDeny(ui.code) } }
                }) { Text(stringResource(R.string.pair_deny)) }
            },
        )
        is PairUi.Confirm -> AlertDialog(
            onDismissRequest = { set(null) },
            title = { Text(stringResource(R.string.link_title, ui.link.server)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(stringResource(R.string.link_text))
                    ui.replaces?.let { Text(stringResource(R.string.link_replaces, it)) }
                    if (ui.link.server.startsWith("http://")) Text(stringResource(R.string.plain_http_warning), color = LocalBrand.current.signal)
                }
            },
            confirmButton = { TextButton(onClick = { claim(ui.link) }) { Text(stringResource(R.string.link_yes)) } },
            dismissButton = { TextButton(onClick = { set(null) }) { Text(stringResource(R.string.link_no)) } },
        )
        // Closing it stops the polling; the code then expires on the server.
        is PairUi.Waiting -> AlertDialog(
            onDismissRequest = { set(null) },
            title = { Text(stringResource(R.string.link_waiting_title)) },
            text = { Text(stringResource(R.string.link_waiting_text, ui.link.server, ui.check)) },
            confirmButton = { TextButton(onClick = { set(null) }) { Text(stringResource(R.string.link_cancel)) } },
        )
    }
}

private enum class Tab(val label: Int, val icon: ImageVector) {
    Inbox(R.string.tab_inbox, Icons.Filled.Notifications),
    Snoozed(R.string.tab_snoozed, Icons.Filled.DateRange),
    Cleared(R.string.tab_cleared, Icons.Filled.CheckCircle),
    Settings(R.string.tab_settings, Icons.Filled.Settings),
}

@Composable
private fun Home(app: App, battery: Intent?, resumes: Int, scan: () -> Unit) {
    val b = LocalBrand.current
    var tab by rememberSaveable { mutableStateOf(if (app.configured) Tab.Inbox else Tab.Settings) }
    var topic by rememberSaveable { mutableStateOf<String?>(null) }
    var unfolded by rememberSaveable { mutableStateOf(setOf<String>()) } // stacks opened in the list
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    val dao = app.db.dao()
    val ctx = LocalContext.current
    val status by app.status.collectAsState()
    LaunchedEffect(status) { if (status == SyncClient.Status.AUTH_FAILED) tab = Tab.Settings } // signed out: show the sign-in screen

    // Clear with an Undo toast, as on the web inbox.
    val clear: (Notif) -> Unit = { n ->
        app.act(n.id, "dismiss")
        scope.launch {
            if (snackbar.showSnackbar(ctx.getString(R.string.cleared_everywhere), ctx.getString(R.string.undo), duration = androidx.compose.material3.SnackbarDuration.Short) == SnackbarResult.ActionPerformed) {
                app.undoClear(listOf(n))
            }
        }
    }
    // A stack's Clear clears every row in it, one bulk clear as on the web (#360).
    val clearAll: (List<Notif>) -> Unit = { rows ->
        val ids = rows.map { it.id }
        app.act(ids, "dismiss", App.VIA_APP_BULK)
        scope.launch {
            if (snackbar.showSnackbar(ctx.getString(R.string.cleared_everywhere), ctx.getString(R.string.undo), duration = androidx.compose.material3.SnackbarDuration.Short) == SnackbarResult.ActionPerformed) {
                app.undoClear(rows)
            }
        }
    }

    Scaffold(
        containerColor = b.paper,
        snackbarHost = { SnackbarHost(snackbar) },
        bottomBar = {
            NavigationBar(containerColor = b.paper, tonalElevation = 0.dp) {
                Tab.entries.forEach { t ->
                    NavigationBarItem(
                        selected = tab == t, onClick = { tab = t },
                        icon = { Icon(t.icon, null) }, label = { Text(stringResource(t.label)) },
                        colors = NavigationBarItemDefaults.colors(
                            selectedIconColor = b.ink, selectedTextColor = b.ink, indicatorColor = b.line,
                            unselectedIconColor = b.slate, unselectedTextColor = b.slate,
                        ),
                    )
                }
            }
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            Header(app)
            when (tab) {
                Tab.Settings -> SettingsScreen(app, battery, resumes, scan)
                else -> {
                    val flow = when (tab) { Tab.Inbox -> dao.inbox(); Tab.Snoozed -> dao.snoozed(); else -> dao.cleared() }
                    val rows by remember(tab) { flow }.collectAsState(emptyList())
                    val counts = topicCounts(rows)
                    val topics = counts.keys.sorted()
                    val picked = topic?.takeIf { it in topics }
                    if (topics.size > 1) Pills(topics, counts, rows.size, picked) { topic = it }
                    if (tab == Tab.Cleared) ServerCleared(app, rows.size, rows.firstOrNull()?.id)
                    val shown = rows.filter { picked == null || it.topic == picked }
                    // Stack by topic also folds the list here, as on the web; not when one topic is picked (#231).
                    val entries = if (app.stack && picked == null) stacked(shown, unfolded) else shown.map(::One)
                    if (shown.isEmpty()) Empty(tab) else LazyColumn(
                        contentPadding = PaddingValues(16.dp, 8.dp, 16.dp, 16.dp),
                        verticalArrangement = Arrangement.spacedBy(10.dp),
                    ) {
                        items(entries, key = { it.key }) { e ->
                            if (e is Stack) {
                                val open = e.topic in unfolded
                                StackCard(e, open, if (tab == Tab.Cleared) null else ({ clearAll(e.rows) })) { unfolded = if (open) unfolded - e.topic else unfolded + e.topic }
                                return@items
                            }
                            val n = (e as One).n
                            if (tab == Tab.Cleared) Card(n) { TextButton(onClick = { app.act(n.id, "reopen") }) { Text(stringResource(R.string.restore), color = b.ink) } }
                            else Swipeable(n, onClear = { clear(n) }) {
                                Card(n) {
                                    if (n.state == Notif.ACTIVE) TextButton(onClick = { app.act(n.id, "ack") }) { Text(stringResource(R.string.ack), color = b.ink) }
                                    if (tab == Tab.Inbox) SnoozeButton(app, n.id)
                                    TextButton(onClick = { clear(n) }) { Text(stringResource(R.string.clear), color = b.clear, fontWeight = FontWeight.SemiBold) }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

/** The phone keeps only recent cleared history; this says the server's exact count when it holds more (#261). */
@Composable
private fun ServerCleared(app: App, here: Int, newest: String?) {
    val b = LocalBrand.current
    var total by remember { mutableStateOf(-1) }
    LaunchedEffect(here, newest) { // a new clear at the 200-row cap keeps the size but changes the top row
        app.io.execute { app.pruneCleared() } // rows age out while the tab is open on an idle connection
        if (!app.configured) return@LaunchedEffect
        val api = app.api()
        app.io.execute { runCatching { org.json.JSONObject(api.get("/api/v1/counts")).getInt("cleared") }.getOrNull()?.let { total = it } }
    }
    if (total > here) Text(
        pluralStringResource(R.plurals.cleared_on_server, total, total, App.KEEP_DAYS),
        color = b.slate, fontSize = 13.sp, modifier = Modifier.padding(horizontal = 20.dp),
    )
}

@Composable
private fun Header(app: App) {
    val b = LocalBrand.current
    val status by app.status.collectAsState()
    val (dot, label) = when {
        !app.configured -> b.slate to R.string.status_not_set_up
        status == SyncClient.Status.CONNECTED -> b.clear to R.string.status_in_sync
        status == SyncClient.Status.CONNECTING -> b.snooze to R.string.status_connecting
        status == SyncClient.Status.AUTH_FAILED -> b.signal to R.string.status_token_rejected
        status == SyncClient.Status.BUSY -> b.snooze to R.string.status_busy
        else -> b.slate to R.string.status_offline
    }
    Row(Modifier.fillMaxWidth().padding(20.dp, 12.dp, 20.dp, 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(stringResource(R.string.app_name), style = Wordmark, color = b.ink)
        Spacer(Modifier.weight(1f))
        Box(Modifier.size(8.dp).clip(CircleShape).background(dot))
        Spacer(Modifier.width(6.dp))
        Text(stringResource(label), style = Mono, color = b.slate)
    }
}

@Composable
private fun Pills(topics: List<String>, counts: Map<String, Int>, total: Int, selected: String?, onPick: (String?) -> Unit) {
    val b = LocalBrand.current
    // Solid background and room below, so cards scrolling up are cut off cleanly under the chips (#145).
    LazyRow(
        Modifier.fillMaxWidth().background(b.paper).padding(bottom = 8.dp),
        contentPadding = PaddingValues(horizontal = 16.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        items(listOf<String?>(null) + topics) { t ->
            val on = t == selected
            Text(
                stringResource(R.string.topic_count, t ?: stringResource(R.string.all_topics), if (t == null) total else counts[t] ?: 0),
                color = if (on) b.paper else b.ink,
                fontSize = 14.sp,
                modifier = Modifier
                    .clip(RoundedCornerShape(50))
                    .background(if (on) b.ink else Color.Transparent)
                    .border(1.dp, if (on) b.ink else b.line, RoundedCornerShape(50))
                    .clickable { onPick(t) }
                    .padding(horizontal = 14.dp, vertical = 7.dp),
            )
        }
    }
}

@OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)
@Composable
private fun Swipeable(n: Notif, onClear: () -> Unit, content: @Composable () -> Unit) {
    val b = LocalBrand.current
    val state = rememberSwipeToDismissBoxState(confirmValueChange = {
        if (it != SwipeToDismissBoxValue.Settled) onClear()
        it != SwipeToDismissBoxValue.Settled
    })
    SwipeToDismissBox(
        state = state,
        backgroundContent = {
            // Green means it went away.
            Row(
                Modifier.fillMaxSize().clip(RoundedCornerShape(16.dp)).background(b.clear).padding(horizontal = 24.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = if (state.dismissDirection == SwipeToDismissBoxValue.EndToStart) Arrangement.End else Arrangement.Start,
            ) {
                Icon(Icons.Filled.Check, null, tint = b.paper)
                Spacer(Modifier.width(8.dp))
                Text(stringResource(R.string.clear), color = b.paper, fontWeight = FontWeight.SemiBold)
            }
        },
        content = { content() },
    )
}

@Composable
private fun Card(n: Notif, actions: @Composable () -> Unit) {
    val b = LocalBrand.current
    // Tap to read the whole message; tap again to fold it.
    var open by rememberSaveable(n.id) { mutableStateOf(false) }
    Surface(
        onClick = { open = !open },
        color = b.surface, shape = RoundedCornerShape(16.dp), border = BorderStroke(1.dp, b.line),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(Modifier.padding(start = 16.dp, top = 14.dp, end = 8.dp, bottom = 4.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(end = 8.dp)) {
                // Orange only when something needs you.
                val dot = when { n.isFinal -> b.clear; n.priority >= 4 -> b.signal; n.priority == 3 -> b.slate; else -> b.line }
                val icon = rememberPicture(n.icon, Images.ICON_PX)
                if (icon != null) Image(icon, null, Modifier.size(24.dp).clip(RoundedCornerShape(6.dp)).background(b.iconTile).padding(if (b.iconTile == Color.Transparent) 0.dp else 1.dp))
                else Box(Modifier.size(8.dp).clip(CircleShape).background(dot))
                Spacer(Modifier.width(10.dp))
                Text(
                    n.title.ifEmpty { n.topic }, color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold,
                    maxLines = if (open) Int.MAX_VALUE else 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f),
                )
                if (n.state != Notif.ACTIVE) StateChip(n.state)
            }
            if (n.message.isNotEmpty()) {
                Spacer(Modifier.height(4.dp))
                val text = remember(n.message, n.format, b) { Md.render(n.message, n.isMarkdown, link = b.clear, muted = b.slate) }
                val body = @Composable { Text(text, color = b.body, fontSize = 15.sp, maxLines = if (open) Int.MAX_VALUE else 6, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(end = 8.dp)) }
                // Open text can be selected and copied; links open in the browser.
                if (open) SelectionContainer { body() } else body()
            }
            n.file?.let { f -> Attached(f) }
            val ctx = LocalContext.current
            // The sender's answers (Allow, Deny) show only once the whole text does: an answer approves all of it (#366).
            val answers = if (n.isFinal) emptyList() else TrayButton.replies(n)
            if (answers.isNotEmpty() && !open) Text(stringResource(R.string.tap_to_answer), color = b.slate, fontSize = 13.sp, modifier = Modifier.padding(top = 6.dp))
            if (answers.isNotEmpty() && open) Row(Modifier.horizontalScroll(rememberScrollState())) { // a long label never hides the next one
                answers.forEach { a ->
                    TextButton(onClick = { App.of(ctx).act(n.id, "reply", org.json.JSONObject().put("reply", a.reply).put("seq", n.seq).toString()) }) { Text(a.label, color = b.ink) }
                }
            }
            Spacer(Modifier.height(8.dp))
            Text(meta(ctx, n), style = Mono, color = b.slate, maxLines = 2, overflow = TextOverflow.Ellipsis) // 2: a full date is long
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                IconButton(onClick = { share(ctx, n) }) { Icon(Icons.Filled.Share, stringResource(R.string.share), tint = b.slate) }
                IconButton(onClick = { copy(ctx, n) }) { Icon(painterResource(R.drawable.ic_copy), stringResource(R.string.copy), tint = b.slate, modifier = Modifier.size(20.dp)) }
                Spacer(Modifier.weight(1f))
                actions()
            }
        }
    }
}

/** A picture attachment shows inline; any other file, or a picture that would not load, is a download button. */
@Composable
private fun Attached(f: Attachment) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val images = App.of(ctx).images
    val pic = if (f.isImage) rememberPicture(f.url, Images.PICTURE_PX) else null
    Spacer(Modifier.height(8.dp))
    if (pic != null) Image(
        pic, f.name, contentScale = ContentScale.Fit,
        modifier = Modifier.padding(end = 8.dp).heightIn(max = 240.dp).clip(RoundedCornerShape(10.dp)).clickable { images.download(ctx, f) },
    )
    else TextButton(onClick = { images.download(ctx, f) }, contentPadding = PaddingValues(0.dp)) {
        Text(stringResource(R.string.download, f.name), color = b.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/** The picture at [url], or null while it loads, when there is none, or when it fails. */
@Composable
private fun rememberPicture(url: String, px: Int): ImageBitmap? {
    if (url.isEmpty()) return null
    val images = App.of(LocalContext.current).images
    var bmp by remember(url) { mutableStateOf(images.cached(url, px)) }
    LaunchedEffect(url) {
        if (bmp == null) bmp = withContext(images.dispatcher) { images.load(url, px) }
    }
    return remember(bmp) { bmp?.asImageBitmap() }
}

private fun share(ctx: Context, n: Notif) {
    val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, n.shareText())
    if (n.title.isNotEmpty()) send.putExtra(Intent.EXTRA_SUBJECT, n.title)
    runCatching { ctx.startActivity(Intent.createChooser(send, null)) }
}

private fun copy(ctx: Context, n: Notif) {
    ctx.getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText(n.title, n.copyText()))
    if (Build.VERSION.SDK_INT < 33) Toast.makeText(ctx, R.string.copied, Toast.LENGTH_SHORT).show() // Android 13 shows its own
}

@Composable
private fun StateChip(state: String) {
    val b = LocalBrand.current
    val (bg, fg) = chip(state)
    Text(
        stateLabel(state)?.let { stringResource(it) } ?: state, style = Mono, color = fg,
        modifier = Modifier.clip(RoundedCornerShape(50)).background(bg)
            .border(1.dp, if (bg == Color.Transparent) b.line else bg, RoundedCornerShape(50))
            .padding(horizontal = 8.dp, vertical = 2.dp),
    )
}

/** The chip text for a state the server sends; an unknown one shows as sent. */
private fun stateLabel(state: String): Int? = when (state) {
    Notif.ACKNOWLEDGED -> R.string.state_acknowledged
    Notif.SNOOZED -> R.string.state_snoozed
    Notif.DISMISSED -> R.string.state_dismissed
    Notif.RESOLVED -> R.string.state_resolved
    else -> null
}

private fun meta(ctx: Context, n: Notif): String {
    val parts = mutableListOf(n.topic, ago(ctx, n.createdAt))
    val by = n.stateBy ?: ctx.getString(R.string.unknown_device)
    when (n.state) {
        Notif.ACKNOWLEDGED -> parts += ctx.getString(R.string.acked_on, by)
        Notif.SNOOZED -> n.snoozeUntil?.let { parts += ctx.getString(R.string.snoozed_until, DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(it))) }
        Notif.DISMISSED -> parts += ctx.getString(R.string.cleared_on, by)
        Notif.RESOLVED -> parts += ctx.getString(R.string.state_resolved)
    }
    return parts.joinToString("  ·  ")
}

/** Short relative time for the last day, then the full date and time ("3d" alone hides when it happened). */
internal fun ago(ctx: Context, t: Long, now: Long = System.currentTimeMillis()): String {
    val m = (now - t) / 60_000
    return when {
        m < 1 -> ctx.getString(R.string.ago_now)
        m < 60 -> ctx.getString(R.string.ago_minutes, m.toInt())
        m < 1440 -> ctx.getString(R.string.ago_hours, (m / 60).toInt())
        else -> DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(t))
    }
}

/** How many rows each topic has, for the topic chips. */
internal fun topicCounts(rows: List<Notif>): Map<String, Int> = rows.groupingBy { it.topic }.eachCount()

/** One row of the list: a notification, or a stack of one topic's (#231). */
internal sealed interface Entry { val key: String }
internal data class One(val n: Notif) : Entry { override val key get() = n.id }
internal data class Stack(val topic: String, val rows: List<Notif>) : Entry { override val key get() = "stack\u0000$topic" }

/** Two or more of one topic fold into a stack at its top one's place, in the tab's own order; an open stack shows them under it. */
internal fun stacked(rows: List<Notif>, open: Set<String>): List<Entry> = rows.groupBy { it.topic }.values.flatMap { g ->
    if (g.size < 2) listOf(One(g[0]))
    else listOf(Stack(g[0].topic, g)) + if (g[0].topic in open) g.map(::One) else emptyList()
}

@Composable
private fun StackCard(s: Stack, open: Boolean, onClear: (() -> Unit)?, onToggle: () -> Unit) {
    val b = LocalBrand.current
    val top = s.rows.maxOf { it.priority }
    Surface(
        onClick = onToggle,
        color = b.surface, shape = RoundedCornerShape(16.dp), border = BorderStroke(1.dp, b.line),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Row(Modifier.padding(start = 16.dp, top = 12.dp, end = 8.dp, bottom = 12.dp), verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(8.dp).clip(CircleShape).background(when { top >= 4 -> b.signal; top == 3 -> b.slate; else -> b.line }))
            Spacer(Modifier.width(10.dp))
            Column(Modifier.weight(1f)) {
                Text(s.topic, color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(s.rows[0].title.ifEmpty { s.rows[0].message }, color = b.body, fontSize = 15.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(pluralStringResource(R.plurals.tray_stack, s.rows.size, s.rows.size), style = Mono, color = b.slate)
            }
            if (onClear != null) TextButton(onClick = onClear) { Text(stringResource(R.string.clear), color = b.ink) }
            TextButton(onClick = onToggle) { Text(stringResource(if (open) R.string.stack_hide else R.string.stack_show), color = b.ink) }
        }
    }
}

@Composable
private fun Empty(tab: Tab) {
    val b = LocalBrand.current
    Column(Modifier.fillMaxSize(), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.Center) {
        Icon(Icons.Filled.CheckCircle, null, tint = b.clear, modifier = Modifier.size(48.dp))
        Spacer(Modifier.height(12.dp))
        Text(
            stringResource(when (tab) { Tab.Inbox -> R.string.empty_inbox; Tab.Snoozed -> R.string.empty_snoozed; else -> R.string.empty_cleared }),
            color = b.slate, fontSize = 15.sp,
        )
    }
}

/** Tap snoozes for the default time; a long press offers the other saved times, like the web (#193). */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun SnoozeButton(app: App, id: String) {
    val b = LocalBrand.current
    val s by app.snooze.collectAsState()
    var menu by remember { mutableStateOf(false) }
    Box {
        Text(
            stringResource(R.string.snooze_for, snoozeLabel(s.default)), color = b.ink, style = MaterialTheme.typography.labelLarge,
            modifier = Modifier.clip(RoundedCornerShape(20.dp))
                .combinedClickable(role = Role.Button, onLongClick = { menu = true }) { app.act(id, "snooze", Snooze.body(s.default)) }
                .padding(horizontal = 12.dp, vertical = 10.dp),
        )
        DropdownMenu(menu, { menu = false }) {
            s.times.forEach { t ->
                DropdownMenuItem(text = { Text(snoozeLabel(t)) }, onClick = { menu = false; app.act(id, "snooze", Snooze.body(t)) })
            }
        }
    }
}

/** The shared snooze times and default, saved on the server for every device (#193). */
private fun objects(o: org.json.JSONObject, key: String): List<org.json.JSONObject> =
    o.optJSONArray(key)?.let { a -> (0 until a.length()).map { a.getJSONObject(it) } }.orEmpty()

private fun millisOf(s: String) = runCatching { java.time.OffsetDateTime.parse(s).toInstant().toEpochMilli() }.getOrNull()

private val approveOrder = listOf("denied", "ask", "allowed")

private fun approveName(v: String) = when (v) {
    "allowed" -> R.string.approve_name_allowed
    "ask" -> R.string.approve_name_ask
    "denied" -> R.string.approve_name_denied
    else -> null
}

/** Is d this phone or a device or token it hangs under, walked by id as the web does (#382). */
internal fun isOwn(list: List<org.json.JSONObject>, d: org.json.JSONObject): Boolean {
    var id = list.firstOrNull { it.optBoolean("this") }?.optString("id").orEmpty()
    repeat(list.size) { // at most one hop per device, so a loop in bad data ends
        if (id.isEmpty()) return false
        if (id == d.optString("id")) return true
        id = list.firstOrNull { it.optString("id") == id }?.optString("via_id") ?: return false
    }
    return false
}

/**
 * The "May approve" menu for d, weakest first, as the web Settings page (#382). Empty unless this phone
 * is allowed; for this phone or what it hangs under it stops at the current setting, since the server
 * never lets a device raise those. The server checks it all again.
 */
internal fun approveChoices(list: List<org.json.JSONObject>, d: org.json.JSONObject): List<String> {
    if (list.firstOrNull { it.optBoolean("this") }?.optString("approve") != "allowed") return emptyList()
    val i = approveOrder.indexOf(d.optString("approve"))
    return if (i >= 0 && isOwn(list, d)) approveOrder.take(i + 1) else approveOrder
}

/** One device's second line: what it is, what it hangs under, when it was last seen. */
private fun deviceLine(ctx: Context, d: org.json.JSONObject): String = listOfNotNull(
    when (d.optString("kind")) {
        "phone" -> ctx.getString(R.string.kind_phone)
        "browser" -> ctx.getString(R.string.kind_browser)
        "agent" -> ctx.getString(R.string.kind_agent)
        else -> ctx.getString(R.string.kind_token)
    },
    d.optString("via").takeIf { it.isNotEmpty() }?.let { ctx.getString(R.string.device_via, it) },
    millisOf(d.optString("last_seen_at"))?.let { ctx.getString(R.string.device_seen, ago(ctx, it)) } ?: ctx.getString(R.string.device_never_seen),
).joinToString(", ")

/**
 * Linked devices with Unlink, their activity log, and this phone's own approve setting
 * (docs/SPEC.md section 5, the same list the web Settings page shows). Hidden until the server answers.
 */
@Composable
private fun DeviceSettings(app: App, resumes: Int) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val server by app.saved.collectAsState()
    var devices by remember(server) { mutableStateOf<List<org.json.JSONObject>?>(null) }
    var log by remember(server) { mutableStateOf<List<org.json.JSONObject>>(emptyList()) }
    var reload by remember { mutableStateOf(0) }
    var unlink by remember { mutableStateOf<org.json.JSONObject?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var showLog by rememberSaveable { mutableStateOf(false) }
    var menu by remember { mutableStateOf<String?>(null) }
    var lower by remember { mutableStateOf<Pair<org.json.JSONObject, String>?>(null) }
    // The server decides (docs/SPEC.md "Who may approve"): a refusal shows as the error line.
    var busy by remember { mutableStateOf(false) } // a change is on its way; the menu waits for the fresh list
    val setApprove: (org.json.JSONObject, String) -> Unit = { d, v ->
        busy = true
        val api = app.api()
        app.io.execute {
            error = runCatching { api.put("/api/v1/devices/${android.net.Uri.encode(d.optString("id"))}/approve", org.json.JSONObject().put("approve", v).toString()) }
                .exceptionOrNull()?.let { ctx.getString(R.string.device_approve_failed, it.message) }
            reload++
        }
    }
    val changed by app.devicesChanged.collectAsState() // another device changed this phone's approve setting, or one came or went
    LaunchedEffect(server, resumes, reload, changed) {
        val api = app.api()
        app.io.execute {
            runCatching { objects(org.json.JSONObject(api.get("/api/v1/devices?all=1")), "devices") }.getOrNull()?.let { devices = it }
            busy = false
            runCatching { objects(org.json.JSONObject(api.get("/api/v1/devices/log")), "events") }.getOrNull()?.let { log = it }
        }
    }
    val list = devices ?: return
    val approve = when (list.firstOrNull { it.optBoolean("this") }?.optString("approve")) {
        "allowed" -> R.string.approve_allowed
        "ask" -> R.string.approve_ask
        "denied" -> R.string.approve_denied
        else -> null
    }
    if (approve != null) {
        Spacer(Modifier.height(8.dp))
        Text(stringResource(R.string.approve_title), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
        Text(stringResource(approve), color = b.slate, fontSize = 13.sp)
    }
    Spacer(Modifier.height(8.dp))
    Text(stringResource(R.string.devices_title), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
    list.forEach { d ->
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.weight(1f)) {
                val name = d.optString("name")
                Text(if (d.optBoolean("this")) stringResource(R.string.device_this, name) else name, color = b.ink)
                Text(deviceLine(ctx, d), color = b.slate, fontSize = 13.sp)
                val now = approveName(d.optString("approve"))?.let { ctx.getString(R.string.device_approve, ctx.getString(it)) }
                val choices = approveChoices(list, d)
                if (choices.size > 1) Box {
                    Text(now.orEmpty(), color = b.ink, fontSize = 13.sp, modifier = Modifier.clip(RoundedCornerShape(8.dp)).clickable(enabled = !busy, role = Role.Button) { menu = d.optString("id") }.padding(vertical = 6.dp))
                    DropdownMenu(menu == d.optString("id"), { menu = null }) {
                        choices.forEach { v ->
                            DropdownMenuItem(text = { Text(stringResource(approveName(v)!!)) }, onClick = {
                                menu = null
                                if (v != d.optString("approve")) {
                                    // Lowering this phone or what it hangs under cannot be undone here, so it asks first, as the web does.
                                    if (isOwn(list, d)) lower = d to v else setApprove(d, v)
                                }
                            })
                        }
                    }
                } else if (now != null) Text(now, color = b.slate, fontSize = 13.sp)
            }
            // A typed-in token cannot be unlinked (revoke it on the web); unlinking this phone signs it out here too.
            if (d.optBoolean("can_unlink")) TextButton(onClick = { unlink = d }) { Text(stringResource(R.string.device_unlink), color = b.signal) }
        }
    }
    val hold = androidx.lifecycle.ViewModelProvider(ctx as ComponentActivity)[AgentHold::class.java]
    TextButton(onClick = { hold.open.value = true }) { Text(stringResource(R.string.agent_add), color = b.ink) }
    if (hold.open.value) AgentInvite(app, hold) { hold.open.value = false; hold.made.value = null; reload++ }
    error?.let { Text(it, color = b.signal, fontSize = 13.sp) }
    if (log.isNotEmpty()) TextButton(onClick = { showLog = !showLog }) {
        Text(stringResource(if (showLog) R.string.device_log_hide else R.string.device_log_show), color = b.ink)
    }
    if (showLog) log.forEach { e ->
        val at = millisOf(e.optString("at"))?.let { ago(ctx, it) }.orEmpty()
        val by = e.optString("by").takeIf { it.isNotEmpty() }?.let { " " + ctx.getString(R.string.device_log_by, it) }.orEmpty()
        val device = e.optString("device")
        val action = e.optString("action")
        Text(ctx.getString(R.string.device_log_line, at, device, action) + by, style = Mono, color = b.slate, fontSize = 12.sp)
    }
    lower?.let { (d, v) ->
        AlertDialog(
            onDismissRequest = { lower = null },
            title = { Text(stringResource(R.string.device_lower_title, d.optString("name"), stringResource(approveName(v)!!))) },
            text = { Text(stringResource(R.string.device_lower_text)) },
            confirmButton = { TextButton(onClick = { lower = null; setApprove(d, v) }) { Text(stringResource(R.string.device_lower), color = b.signal) } },
            dismissButton = { TextButton(onClick = { lower = null }) { Text(stringResource(R.string.link_cancel)) } },
        )
    }
    unlink?.let { d ->
        AlertDialog(
            onDismissRequest = { unlink = null },
            title = { Text(stringResource(R.string.device_unlink_title, d.optString("name"))) },
            text = { Text(stringResource(R.string.device_unlink_text)) },
            confirmButton = {
                TextButton(onClick = {
                    unlink = null
                    val api = app.api()
                    app.io.execute {
                        error = runCatching { api.delete("/api/v1/devices/${android.net.Uri.encode(d.optString("id"))}") }
                            .exceptionOrNull()?.let { ctx.getString(R.string.device_unlink_failed, it.message) }
                        reload++
                    }
                }) { Text(stringResource(R.string.device_unlink), color = b.signal) }
            },
            dismissButton = { TextButton(onClick = { unlink = null }) { Text(stringResource(R.string.link_cancel)) } },
        )
    }
}

/** The topics typed in the Add agent form, split on commas and spaces. */
internal fun agentTopics(s: String): List<String> = s.split(',', ' ').map { it.trim() }.filter { it.isNotEmpty() }.distinct()

/** The body of POST /api/v1/agents/invite (docs/integrations/agent-invite.md), or null while the form is not filled. */
internal fun agentInviteBody(name: String, topics: String, minutes: Int): org.json.JSONObject? {
    val t = agentTopics(topics)
    if (name.isBlank() || t.isEmpty()) return null
    return org.json.JSONObject().put("name", name.trim()).put("topics", org.json.JSONArray(t)).put("expires_in", "${minutes}m")
}

/**
 * Add agent (#457): a name, its topics and how long the invite lasts, then the invite to copy or share.
 * The invite is never logged or saved: it lives in [AgentHold], so a rotation keeps it and leaving the screen drops it.
 */
class AgentHold : androidx.lifecycle.ViewModel() {
    val open = mutableStateOf(false)
    val made = mutableStateOf<org.json.JSONObject?>(null)
}

@Composable
private fun AgentInvite(app: App, hold: AgentHold, close: () -> Unit) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    var name by remember { mutableStateOf("") }
    var topics by remember { mutableStateOf("") }
    var minutes by remember { mutableStateOf(15) }
    var sending by remember { mutableStateOf(false) }
    var err by remember { mutableStateOf<String?>(null) }
    var made by hold.made
    val body = agentInviteBody(name, topics, minutes)
    val done = made
    AlertDialog(
        // Once asked for, only Done closes it, so a stray tap cannot lose an invite the server already made.
        onDismissRequest = { if (done == null && !sending) close() },
        title = { Text(stringResource(R.string.agent_add)) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (done == null) {
                    OutlinedTextField(name, { name = it }, label = { Text(stringResource(R.string.agent_name)) }, singleLine = true, modifier = Modifier.fillMaxWidth())
                    OutlinedTextField(topics, { topics = it }, label = { Text(stringResource(R.string.agent_topics)) }, supportingText = { Text(stringResource(R.string.agent_topics_hint)) }, singleLine = true, modifier = Modifier.fillMaxWidth())
                    Text(stringResource(R.string.agent_lasts), color = b.slate, fontSize = 13.sp)
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        listOf(5 to R.string.agent_5m, 15 to R.string.agent_15m, 60 to R.string.agent_1h).forEach { (m, label) ->
                            FilterChip(selected = minutes == m, onClick = { minutes = m }, label = { Text(stringResource(label)) })
                        }
                    }
                    Text(stringResource(R.string.agent_denied), color = b.slate, fontSize = 13.sp)
                } else {
                    val url = done.optString("url")
                    Text(url, style = Mono, color = b.ink, fontSize = 13.sp)
                    val until = millisOf(done.optString("expires_at"))?.let { DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(it)) }.orEmpty()
                    Text(stringResource(R.string.agent_once, until), color = b.slate, fontSize = 13.sp)
                    Text(stringResource(R.string.agent_denied), color = b.slate, fontSize = 13.sp)
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        TextButton(onClick = {
                            val clip = ClipData.newPlainText(done.optString("name"), url)
                            clip.description.extras = android.os.PersistableBundle().apply { putBoolean("android.content.extra.IS_SENSITIVE", true) } // kept out of the clipboard preview
                            ctx.getSystemService(ClipboardManager::class.java).setPrimaryClip(clip)
                        }) { Text(stringResource(R.string.agent_copy), color = b.ink) }
                        TextButton(onClick = {
                            runCatching { ctx.startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, url), null)) }
                        }) { Text(stringResource(R.string.agent_share), color = b.ink) }
                    }
                }
                err?.let { Text(it, color = b.signal, fontSize = 13.sp) }
            }
        },
        confirmButton = {
            if (done != null) TextButton(onClick = close) { Text(stringResource(R.string.agent_done)) }
            else TextButton(enabled = body != null && !sending, onClick = {
                sending = true
                err = null
                val api = app.api()
                app.io.execute {
                    runCatching { org.json.JSONObject(api.post("/api/v1/agents/invite", body.toString())) }
                        .onSuccess { made = it }
                        .onFailure { err = ctx.getString(R.string.agent_failed, it.message) }
                    sending = false
                }
            }) { Text(stringResource(R.string.agent_make)) }
        },
        dismissButton = { if (done == null) TextButton(enabled = !sending, onClick = close) { Text(stringResource(R.string.link_cancel)) } },
    )
}

@Composable
private fun SnoozeSettings(app: App) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val s by app.snooze.collectAsState()
    val server by app.saved.collectAsState() // a switch to another server drops unsaved edits
    var times by remember(server) { mutableStateOf(s.times.joinToString(", ")) }
    var def by remember(server) { mutableStateOf(s.default) }
    var result by remember { mutableStateOf<String?>(null) }
    // Unsaved edits win over a settings message from another device; after a save the server's answer shows.
    var edited by remember(server) { mutableStateOf(false) }
    LaunchedEffect(s, edited) {
        if (!edited) {
            times = s.times.joinToString(", ")
            def = s.default
        }
    }
    val typed = Snooze.parse(times)
    Spacer(Modifier.height(8.dp))
    Text(stringResource(R.string.snooze_settings), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
    Text(stringResource(R.string.snooze_explain), color = b.slate, fontSize = 13.sp)
    OutlinedTextField(times, { times = it; edited = true }, label = { Text(stringResource(R.string.snooze_times)) }, singleLine = true, modifier = Modifier.fillMaxWidth())
    Text(stringResource(R.string.snooze_default), color = b.body, fontSize = 14.sp)
    Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        typed.forEach { t -> FilterChip(selected = t == def, onClick = { def = t; edited = true }, label = { Text(snoozeLabel(t)) }) }
    }
    Button(
        onClick = {
            // Without a default from the list the server keeps the old one, or picks the first.
            val body = org.json.JSONObject().put("snooze_intervals", org.json.JSONArray(typed)).apply { if (def in typed) put("snooze_default", def) }.toString()
            val api = app.api() // the server shown now, even if a switch is queued ahead of this save
            val sent = times to def
            app.io.execute {
                result = try {
                    // The server sends the new times to every device, this one too, as a settings message.
                    api.put("/api/v1/settings", body)
                    if (times to def == sent) edited = false // edits typed during the save stay unsaved
                    ctx.getString(R.string.snooze_saved)
                } catch (e: Exception) {
                    ctx.getString(R.string.snooze_not_saved, e.message)
                }
            }
        },
        colors = ButtonDefaults.buttonColors(containerColor = b.ink, contentColor = b.paper),
    ) { Text(stringResource(R.string.snooze_save)) }
    result?.let { Text(it, color = b.body, fontSize = 14.sp) }
}

/** The notification sound on this phone (#282): pick one, hear it first, and see when Android's own settings win. */
@Composable
private fun SoundSettings(app: App, resumes: Int) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val sound by app.soundNow.collectAsState() // follows a change from the server or an agent (#289)
    var saves by remember { mutableStateOf(0) } // bumped once a save has made the channels, so the check reads them
    var why by remember { mutableStateOf<String?>(null) } // why the choice stays on this phone only
    val overridden = remember(sound, resumes, saves) { app.tray.soundOverridden(sound) }
    val dndLost = remember(sound, resumes, saves) { app.tray.dndLost(sound) }
    var playing by remember { mutableStateOf<android.media.Ringtone?>(null) }
    androidx.compose.runtime.DisposableEffect(Unit) { onDispose { playing?.stop() } }

    Spacer(Modifier.height(8.dp))
    Text(stringResource(R.string.sound), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
    Text(stringResource(R.string.sound_explain), color = b.slate, fontSize = 13.sp)
    Sounds.NAMES.forEach { name ->
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            RadioButton(selected = name == sound, onClick = {
                app.soundNow.value = name
                app.io.execute { why = app.pickSound(name); saves++ }
            })
            Text(stringResource(soundLabel(name)), color = b.ink, modifier = Modifier.weight(1f))
            Sounds.uri(ctx, name)?.let { uri ->
                TextButton(onClick = {
                    playing?.stop()
                    playing = RingtoneManager.getRingtone(ctx, uri)?.apply { play() }
                }) { Text(stringResource(R.string.sound_preview), color = b.ink) }
            }
        }
    }
    why?.let { Text(it, color = b.slate, fontSize = 13.sp) }
    if (overridden) {
        Text(stringResource(R.string.sound_overridden), color = b.signal, fontSize = 13.sp)
        TextButton(onClick = {
            runCatching { ctx.startActivity(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, ctx.packageName)) }
        }) { Text(stringResource(R.string.sound_open_settings), color = b.ink) }
    }
    // Only the person can let a channel through Do Not Disturb; an app cannot switch it on by itself (#259).
    if (dndLost) Text(stringResource(R.string.dnd_lost), color = b.signal, fontSize = 13.sp)
    Text(stringResource(R.string.dnd_explain), color = b.slate, fontSize = 13.sp)
    TextButton(onClick = {
        runCatching {
            ctx.startActivity(
                Intent(Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
                    .putExtra(Settings.EXTRA_APP_PACKAGE, ctx.packageName)
                    .putExtra(Settings.EXTRA_CHANNEL_ID, Sounds.channel(5, sound)),
            )
        }
    }) { Text(stringResource(R.string.dnd_open), color = b.ink) }
}

private fun soundLabel(name: String) = when (name) {
    Sounds.DEFAULT -> R.string.sound_default
    Sounds.NONE -> R.string.sound_none
    "chime" -> R.string.sound_chime
    "ding" -> R.string.sound_ding
    "pop" -> R.string.sound_pop
    "bell" -> R.string.sound_bell
    "alert" -> R.string.sound_alert
    else -> R.string.sound_soft
}

@Composable
private fun SettingsScreen(app: App, battery: Intent?, resumes: Int, scan: () -> Unit) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val saved by app.saved.collectAsState() // a linked phone's new server and token show here at once
    var url by rememberSaveable(saved) { mutableStateOf(app.serverUrl.ifEmpty { "https://" }) }
    var token by rememberSaveable(saved) { mutableStateOf(app.token) }
    var result by remember { mutableStateOf<String?>(null) }
    var muted by remember { mutableStateOf(app.muted) }
    var stack by remember { mutableStateOf(app.stack) }
    val topics by app.db.dao().topics().collectAsState(emptyList())
    val status by app.status.collectAsState()

    val scroll = rememberScrollState()
    // Scroll up so the signed-out line is seen even if Settings was scrolled down.
    LaunchedEffect(status) { if (status == SyncClient.Status.AUTH_FAILED) scroll.animateScrollTo(0) }
    Column(Modifier.verticalScroll(scroll).padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        // Unlinked from another device, revoked, or mistyped: this is where the phone signs in again (#264).
        if (app.configured && status == SyncClient.Status.AUTH_FAILED) Text(stringResource(R.string.signed_out), color = b.signal, fontSize = 14.sp)
        Text(stringResource(R.string.server), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
        OutlinedTextField(
            url, { url = pastedUrl(it) }, label = { Text(stringResource(R.string.server_url)) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
        )
        if (url.trim().startsWith("http://", ignoreCase = true)) Text(stringResource(R.string.plain_http_warning), color = b.signal, fontSize = 13.sp)
        OutlinedTextField(
            token, { token = it }, label = { Text(stringResource(R.string.token)) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
            visualTransformation = PasswordVisualTransformation(),
        )
        Button(
            onClick = {
                result = ctx.getString(R.string.testing)
                val newUrl = serverAddress(url)
                url = newUrl
                val newToken = token.trim()
                app.io.execute {
                    app.saveServer(newUrl, newToken)
                    // /api/v1/topics needs the read scope, so this checks the URL and the token together.
                    result = try {
                        app.api().get("/api/v1/topics")
                        SyncService.start(ctx, restart = true)
                        ctx.getString(R.string.test_ok)
                    } catch (e: HttpError) {
                        if (e.isAuth) ctx.getString(R.string.test_token_rejected) else ctx.getString(R.string.test_http_error, e.code)
                    } catch (e: Exception) {
                        ctx.getString(R.string.test_unreachable, e.message)
                    }
                }
            },
            colors = ButtonDefaults.buttonColors(containerColor = b.ink, contentColor = b.paper),
        ) { Text(stringResource(R.string.save_and_test)) }
        // Signed in on a browser already? Its Settings page shows a code that sets this phone up.
        TextButton(onClick = scan) { Text(stringResource(R.string.scan_qr), color = b.ink) }
        result?.let { Text(it, color = b.body, fontSize = 14.sp) }
        battery?.takeIf { app.configured }?.let { prompt ->
            Text(stringResource(R.string.battery_explain), color = b.slate, fontSize = 13.sp)
            TextButton(onClick = { openBattery(ctx, prompt) }) { Text(stringResource(R.string.battery_button), color = b.ink) }
        }

        if (topics.isNotEmpty()) {
            Spacer(Modifier.height(8.dp))
            Text(stringResource(R.string.topics), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
            Text(stringResource(R.string.topics_explain), color = b.slate, fontSize = 13.sp)
            topics.forEach { t ->
                Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                    Text(t, color = b.ink, modifier = Modifier.weight(1f))
                    Text(stringResource(if (t in muted) R.string.topic_muted else R.string.topic_on), style = Mono, color = b.slate)
                    Spacer(Modifier.width(8.dp))
                    Switch(checked = t !in muted, onCheckedChange = { on ->
                        muted = if (on) muted - t else muted + t
                        app.muted = muted
                        app.io.execute { RoomStore(app).reconcile() }
                    })
                }
            }
        }
        Spacer(Modifier.height(8.dp))
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.weight(1f)) {
                Text(stringResource(R.string.stack), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
                Text(stringResource(R.string.stack_explain), color = b.slate, fontSize = 13.sp)
            }
            Switch(checked = stack, onCheckedChange = { on ->
                stack = on
                app.stack = on
                app.io.execute { RoomStore(app).reconcile() } // reposts every row in or out of its stack
            })
        }
        SoundSettings(app, resumes)
        if (app.configured) SnoozeSettings(app)
        if (app.configured) DeviceSettings(app, resumes)
        Spacer(Modifier.height(8.dp))
        Text(stringResource(R.string.about, BuildConfig.VERSION_NAME), style = Mono, color = b.slate)
    }
}
