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
import android.os.SystemClock
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
import androidx.compose.animation.core.exponentialDecay
import androidx.compose.animation.core.tween
import androidx.compose.foundation.gestures.AnchoredDraggableState
import androidx.compose.foundation.gestures.DraggableAnchors
import androidx.compose.foundation.gestures.Orientation
import androidx.compose.foundation.gestures.anchoredDraggable
import androidx.compose.foundation.gestures.animateTo
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.DateRange
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.ThumbUp
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.FloatingActionButton
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
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.text.DateFormat
import java.util.Date
import kotlin.concurrent.thread
import kotlin.math.roundToInt
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

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
    /** Counts tray opens, so the inbox tab comes to the front for each (#78). */
    val opens = mutableStateOf(0)

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
        App.of(this).front = true
    }

    override fun onPause() {
        App.of(this).front = false // a change made while away never moves a card (#598)
        super.onPause()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        if (Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        val app = App.of(this)
        if (app.anyConfigured) SyncService.start(this)
        // Not again after rotation or from recents: the code was used already and would read as expired.
        if (savedInstanceState == null && (intent.flags and Intent.FLAG_ACTIVITY_LAUNCHED_FROM_HISTORY) == 0) { handlePair(intent); handleOpen(intent) }
        setContent {
            val look by app.appearance.collectAsState()
            ClrdTheme(look) {
                Home(app, battery.value, resumes.value, opens.value, ::scan)
                PairDialog(pair.value, { pair.value = it }, ::approvePair, ::claimLink)
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handlePair(intent)
        handleOpen(intent)
    }

    /** A tray entry opens the app on its server, account 0 included, at the inbox (#78). */
    private fun handleOpen(intent: Intent) = openedAccount(intent)?.let { App.of(this).show(it); opens.value++ }

    private fun handlePair(intent: Intent?) {
        if (intent?.action != Intent.ACTION_VIEW) return
        route(parseScan(intent.dataString ?: return), getString(R.string.pair_bad_link))
    }

    /** One way in for scanned codes and opened links, so both get the same checks. */
    private fun route(scan: Scan, bad: String) {
        when (scan) {
            is Scan.Pair -> pairWith(scan.link)
            // Nothing is sent until the person has seen the server address and said Yes.
            // An address this phone has goes to that server; a new one is added beside the others (#78).
            is Scan.Link -> App.of(this).let { app ->
                val same = app.accountAt(scan.link.server)
                pair.value = PairUi.Confirm(scan.link, same?.takeIf { it.configured }?.serverUrl, adds = same == null && app.anyConfigured)
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
        val install = installId(app, link.server)
        val detail = deviceDetail()
        thread(isDaemon = true) {
            val deadline = System.nanoTime() + LINK_TTL_NS
            var outcome: Claim = Claim.Gone
            var error: Exception? = null
            while (pair.value === waiting && System.nanoTime() < deadline) {
                try {
                    outcome = linkClaim(app.http, link.server, link.code, secret, name, install, detail)
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
                    app.saveTo(link.server, got.token)
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
        // The server this phone has at the link's address, else the one shown (#78), whose gate then says why not.
        val acct = app.accountAt(link.server)?.takeIf { it.configured } ?: app.shown.value
        // One snapshot: the gate and every request use the same server and token, never the link's server.
        val (url, token) = acct.creds()
        when (pairGate(link, url, token)) {
            PairGate.NOT_SET_UP -> pair.value = PairUi.Note(getString(R.string.pair_not_set_up))
            PairGate.OTHER_SERVER -> pair.value = PairUi.Note(getString(R.string.pair_other_server, serverAddress(link.server)))
            PairGate.OK -> {
                val api = Api(acct.http, url, token)
                app.show(acct.id)
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
    class Confirm(val link: LinkCode, val replaces: String?, val adds: Boolean = false) : PairUi
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
                    if (ui.adds) Text(stringResource(R.string.link_adds))
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
internal fun Home(app: App, battery: Intent?, resumes: Int, opens: Int, scan: () -> Unit) {
    var tab by rememberSaveable { mutableStateOf(if (app.configured) Tab.Inbox else Tab.Settings) }
    // Everything below reads the shown server (#78), so a switch starts it afresh; only the tab carries over.
    LaunchedEffect(opens) { if (opens > 0 && app.configured) tab = Tab.Inbox } // a tray tap lands on the inbox
    val shown by app.shown.collectAsState()
    androidx.compose.runtime.key(shown.id) { ServerHome(app, battery, resumes, scan, tab) { tab = it } }
}

@Composable
private fun ServerHome(app: App, battery: Intent?, resumes: Int, scan: () -> Unit, tab: Tab, pick: (Tab) -> Unit) {
    val b = LocalBrand.current
    var topic by rememberSaveable { mutableStateOf<String?>(null) }
    var unfolded by rememberSaveable { mutableStateOf(setOf<String>()) } // stacks opened in the list
    var sending by rememberSaveable { mutableStateOf(false) } // the Send box (#150)
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    val dao = app.db.dao()
    val a = remember { app.shown.value } // the server this screen was drawn for; a job queued before a switch still writes to it (#78)
    val ctx = LocalContext.current
    val status by app.status.collectAsState()
    val snooze by app.snooze.collectAsState() // the times a swiped card's Snooze offers
    val swipes by app.swipes.collectAsState() // the buttons each swipe shows (#161)
    val clearLabel = stringResource(R.string.clear)
    val ackLabel = stringResource(R.string.ack)
    val snoozeLabelText = stringResource(R.string.tray_snooze)
    LaunchedEffect(status) { if (status == SyncClient.Status.AUTH_FAILED) pick(Tab.Settings) } // signed out: show the sign-in screen

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

    // Unsubscribe from a card or stack menu (#617). The server's reset then takes the topic's cards and tray entries away.
    val subs by app.subs.collectAsState()
    var asking by remember { mutableStateOf<String?>(null) } // the approval topic waits for a yes
    val unsubscribeNow: (String) -> Unit = { t ->
        val api = app.api()
        app.io.execute {
            val err = a.subscribe(t, false, api)
            scope.launch {
                if (err != null) snackbar.showSnackbar(ctx.getString(R.string.subs_failed, err))
                else if (snackbar.showSnackbar(ctx.getString(R.string.unsubscribed, t), ctx.getString(R.string.undo), duration = androidx.compose.material3.SnackbarDuration.Short) == SnackbarResult.ActionPerformed) {
                    app.io.execute { a.subscribe(t, true, api)?.let { e -> scope.launch { snackbar.showSnackbar(ctx.getString(R.string.subs_failed, e)) } } }
                }
            }
        }
    }
    ConfirmUnsubscribe(asking, { asking = null }, unsubscribeNow)
    // Only a token that follows subscriptions may change them; any other sees every topic anyway.
    // The server says whether the topic carries approvals (#623).
    val unsubscribe: (String) -> Unit = { t ->
        val api = app.api()
        app.io.execute {
            val warn = asksFirst(t, runCatching { a.loadSubs(api) }.getOrNull())
            scope.launch { if (warn) asking = t else unsubscribeNow(t) }
        }
    }
    val topicMenu: @Composable (String) -> Unit = { t -> if (subs?.filtered == true) TopicMenu(t, unsubscribe) }
    val topicItems: @Composable (String) -> List<Pair<String, () -> Unit>> = { t ->
        if (subs?.filtered == true) listOf(stringResource(R.string.unsubscribe_from, t) to { unsubscribe(t) }) else emptyList()
    }

    Scaffold(
        containerColor = b.paper,
        snackbarHost = { SnackbarHost(snackbar) },
        floatingActionButton = {
            if (tab == Tab.Inbox && app.configured) FloatingActionButton(onClick = { app.sender.open(); sending = true }, containerColor = b.ink, contentColor = b.paper) {
                Icon(Icons.AutoMirrored.Filled.Send, stringResource(R.string.send_title))
            }
        },
        bottomBar = {
            NavigationBar(containerColor = b.paper, tonalElevation = 0.dp) {
                Tab.entries.forEach { t ->
                    NavigationBarItem(
                        selected = tab == t, onClick = { pick(t) },
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
            // A send that lands after a rotation still closes the box (#523); a box opened again since stays open (#532).
            LaunchedEffect(app.sender.sent) {
                app.sender.confirm { if (app.sender.done) sending = false; snackbar.showSnackbar(ctx.getString(R.string.send_done)) }
            }
            if (sending) SendBox(app, topic.orEmpty(), close = { sending = false })
            if (tab == Tab.Inbox) UpdateNote(app)
            when (tab) {
                Tab.Settings -> SettingsScreen(app, battery, resumes, scan)
                else -> {
                    val flow = when (tab) { Tab.Inbox -> dao.inbox(); Tab.Snoozed -> dao.snoozed(); else -> dao.cleared() }
                    // tagged with its tab: collectAsState keeps the old tab's list until the new one loads (#598)
                    val loaded by remember(tab) { flow.map { tab to it } }.collectAsState(null)
                    val rows = loaded?.takeIf { it.first == tab }?.second.orEmpty()
                    val counts = topicCounts(rows)
                    val topics = counts.keys.sorted()
                    val picked = topic?.takeIf { it in topics }
                    if (topics.size > 1) Pills(topics, counts, rows.size, picked) { topic = it }
                    if (tab == Tab.Cleared) ServerCleared(app, rows.size, rows.firstOrNull()?.id)
                    val shown = rows.filter { picked == null || it.topic == picked }
                    // Stack by topic also folds the list here, as on the web; not when one topic is picked (#231).
                    val entries = if (app.stack && picked == null) stacked(shown, unfolded) else shown.map(::One)
                    // Cards that leave or come while you look move as on the web (#598).
                    val ms = remember(tab) { MotionState() }
                    val toward = remember { HashMap<String, Pair<Int, Long>>() } // the way a card was swiped, +1 for inline end
                    var swept by remember { mutableIntStateOf(0) }
                    swept // read, so a card that is done leaving redraws the list
                    val still = remember(resumes) { stillMotion(ctx) }
                    val now = SystemClock.uptimeMillis()
                    val list = remember(tab) { LazyListState() }
                    val top by remember(list) { derivedStateOf { list.firstVisibleItemIndex == 0 && list.firstVisibleItemScrollOffset == 0 } }
                    val wiggled = ms.wiggle
                    if (loaded?.first == tab) ms.step(entries, now, app.moved::get) { id -> toward[id]?.takeIf { now - it.second <= FRESH_MS }?.first ?: 1 }
                    // at the top, a new card shows there; the list would otherwise hold the old first card in place
                    if (top && ms.wiggle != wiggled) list.requestScrollToItem(0)
                    if (shown.isEmpty() && ms.ghosts.isEmpty()) Empty(tab) else LazyColumn(
                        state = list,
                        // Room under the last card for the Send button on the inbox.
                        // Each card carries the 10 dp below it, so a leaving card closes its gap as it shrinks (#726).
                        contentPadding = PaddingValues(16.dp, 8.dp, 16.dp, if (tab == Tab.Inbox) 78.dp else 6.dp),
                    ) {
                        items(ms.shown(entries), key = { if (it is Ghost) "gone\u0000" + it.entry.key else (it as Entry).key }) { e ->
                          Box(if (e is Ghost) Modifier else Modifier.padding(bottom = CARD_GAP)) {
                            if (e is Ghost) {
                                Gone(e, still, { ms.ghosts.remove(e); swept++ }) { why ->
                                    when (val g = e.entry) { is Stack -> StackCard(g, false, null, {}, gone = why) {}; is One -> Card(g.n, end = { why() }) }
                                }
                                return@Box
                            }
                            val came = ms.arrived[(e as Entry).key]?.takeIf { SystemClock.uptimeMillis() - it < 500 } != null // off screen, it simply shows when scrolled to
                            Arrive(came && !still, { e.key == ms.wiggle }) {
                            if (e is Stack) {
                                val open = e.topic in unfolded
                                StackCard(e, open, if (tab == Tab.Inbox) ({ clearAll(e.rows) }) else null, { topicMenu(e.topic) }) { unfolded = if (open) unfolded - e.topic else unfolded + e.topic }
                                return@Arrive
                            }
                            val n = (e as One).n
                            if (tab == Tab.Cleared) Card(n, topicItems(n.topic), listOf(CardAction(stringResource(R.string.restore), { app.act(n.id, "reopen") })))
                            else {
                                // The buttons the shared setting names, minus any this card cannot take (#161).
                                val reveal = { names: List<String>, dir: Int ->
                                    val swiped = { // it slides away the way it went
                                        val t = SystemClock.uptimeMillis()
                                        toward.values.removeIf { t - it.second > FRESH_MS }
                                        toward[n.id] = dir to t
                                    }
                                    val snoozeTimes = snooze.times.map { t -> snoozeLabel(t) to { swiped(); app.act(n.id, "snooze", Snooze.body(t)) } }
                                    Swipes.shown(names, n.state == Notif.ACTIVE, tab == Tab.Inbox).map { name ->
                                        when (name) {
                                            "clear" -> RevealAction(clearLabel, Icons.Filled.Check, b.clear, "reveal_clear") { swiped(); clear(n) }
                                            "snooze" -> RevealAction(snoozeLabelText, Icons.Filled.DateRange, b.snooze, "reveal_snooze", snoozeTimes) {
                                                swiped(); app.act(n.id, "snooze", Snooze.body(snooze.default))
                                            }
                                            else -> RevealAction(ackLabel, Icons.Filled.ThumbUp, b.ink, "reveal_ack") { app.act(n.id, "ack") }
                                        }
                                    }
                                }
                                RevealRow(reveal(swipes.start, -1), reveal(swipes.end, 1), app.fastSwipe) {
                                    val snoozeFor = stringResource(R.string.snooze_for, snoozeLabel(snooze.default))
                                    Card(n, topicItems(n.topic), listOfNotNull(
                                        if (n.state == Notif.ACTIVE) CardAction(ackLabel, { app.act(n.id, "ack") }) else null,
                                        if (tab == Tab.Inbox) CardAction(snoozeFor, { app.act(n.id, "snooze", Snooze.body(snooze.default)) },
                                            moved = snooze.times.map { t -> stringResource(R.string.snooze_for, snoozeLabel(t)) to { app.act(n.id, "snooze", Snooze.body(t)) } }) { SnoozeButton(app, n.id) } else null,
                                        CardAction(clearLabel, { clear(n) }, strong = true),
                                    ))
                                }
                            }
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
        val a = app.shown.value; app.io.execute { a.pruneCleared() } // rows age out while the tab is open on an idle connection
        if (!app.configured) return@LaunchedEffect
        val api = app.api()
        app.io.execute { runCatching { org.json.JSONObject(api.get("/api/v1/counts")).getInt("cleared") }.getOrNull()?.let { total = it } }
    }
    if (total > here) Text(
        if (app.dropCleared) pluralStringResource(R.plurals.cleared_on_server_dropped, total, total)
        else pluralStringResource(R.plurals.cleared_on_server, total, total, App.KEEP_DAYS),
        color = b.slate, fontSize = 13.sp, modifier = Modifier.padding(horizontal = 20.dp),
    )
}

@Composable
private fun Header(app: App) {
    val b = LocalBrand.current
    val saved by app.saved.collectAsState()
    val servers = remember(saved) { app.listed }
    val shown by app.shown.collectAsState()
    var menu by remember { mutableStateOf(false) }
    Row(Modifier.fillMaxWidth().padding(20.dp, 12.dp, 20.dp, 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(stringResource(R.string.app_name), style = Wordmark, color = b.ink)
        // More than one server (#78): the one shown, and a tap picks another. One server shows nothing new.
        if (servers.size > 1) Box(Modifier.padding(start = 12.dp, end = 8.dp).weight(1f)) {
            Text(stringResource(R.string.server_switch, shown.host), style = Mono, color = b.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.clip(RoundedCornerShape(8.dp)).clickable(role = Role.Button) { menu = true }.padding(6.dp).testTag("server-switch"))
            DropdownMenu(menu, { menu = false }) {
                servers.forEach { a -> DropdownMenuItem(text = { Text(a.host) }, onClick = { menu = false; app.show(a.id) }) }
            }
        } else Spacer(Modifier.weight(1f))
        SyncState(shown)
    }
}

/** A server's dot and status line, as the header and the Servers list show it. */
@Composable
private fun SyncState(a: Account) {
    val b = LocalBrand.current
    val status by a.status.collectAsState()
    val (dot, label) = when {
        !a.configured -> b.slate to R.string.status_not_set_up
        status == SyncClient.Status.CONNECTED -> b.clear to R.string.status_in_sync
        status == SyncClient.Status.CONNECTING -> b.snooze to R.string.status_connecting
        status == SyncClient.Status.AUTH_FAILED -> b.signal to R.string.status_token_rejected
        status == SyncClient.Status.BUSY -> b.snooze to R.string.status_busy
        else -> b.slate to R.string.status_offline
    }
    Row(verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(8.dp).clip(CircleShape).background(dot))
        Spacer(Modifier.width(6.dp))
        Text(stringResource(label), style = Mono, color = b.slate)
    }
}

/** Settings, Servers (#78), with more than one: each opens that server here, and Remove asks first. */
@Composable
private fun ServerList(app: App, saved: Int) {
    val b = LocalBrand.current
    val servers = remember(saved) { app.listed }
    val shown by app.shown.collectAsState()
    var removing by remember { mutableStateOf<Account?>(null) }
    if (servers.size > 1) servers.forEach { a ->
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.weight(1f).clickable(role = Role.Button) { app.show(a.id) }.padding(vertical = 6.dp)) {
                Text(a.host, color = b.ink, fontWeight = if (a.id == shown.id) FontWeight.SemiBold else null)
                SyncState(a)
            }
            TextButton(onClick = { removing = a }) { Text(stringResource(R.string.server_remove), color = b.signal) }
        }
    }
    removing?.let { a ->
        AlertDialog(
            onDismissRequest = { removing = null },
            title = { Text(stringResource(R.string.server_remove_title, a.host)) },
            text = { Text(stringResource(R.string.server_remove_text)) },
            confirmButton = { TextButton(onClick = { removing = null; app.removeAccount(a.id) }) { Text(stringResource(R.string.server_remove), color = b.signal) } },
            dismissButton = { TextButton(onClick = { removing = null }) { Text(stringResource(R.string.link_cancel)) } },
        )
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

/** One button a swipe reveals. [menu] is what a long press offers, such as the other snooze times. */
class RevealAction(
    val label: String, val icon: ImageVector, val color: Color, val tag: String,
    val menu: List<Pair<String, () -> Unit>> = emptyList(), val run: () -> Unit,
)

private enum class Reveal { Closed, Start, End, FullStart, FullEnd }

/**
 * A swipe slides the card aside and shows round buttons (#161). Nothing changes until one is tapped, and
 * swiping back closes the row. [toStart] shows on a swipe toward the start (left in English), [toEnd] on a
 * swipe toward the end; both mirror in right-to-left languages, and each side's first button sits nearest the
 * screen edge. A side with no buttons does not open. With [fast] on, a swipe all the way across
 * runs that side's first button once the card settles there. A fling only opens the row: while [fast] is
 * off there is no stop past the buttons.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun RevealRow(toStart: List<RevealAction>, toEnd: List<RevealAction>, fast: Boolean, content: @Composable () -> Unit) {
    val density = LocalDensity.current
    val slot = with(density) { 76.dp.toPx() }
    val state = remember {
        AnchoredDraggableState(
            Reveal.Closed, positionalThreshold = { it / 2 }, velocityThreshold = { with(density) { 400.dp.toPx() } },
            snapAnimationSpec = tween(), decayAnimationSpec = exponentialDecay(),
        )
    }
    var width by remember { mutableIntStateOf(0) }
    LaunchedEffect(width, fast, toStart.size, toEnd.size) {
        if (width == 0) return@LaunchedEffect
        state.updateAnchors(DraggableAnchors {
            Reveal.Closed at 0f
            if (toStart.isNotEmpty()) { Reveal.Start at -slot * toStart.size; if (fast) Reveal.FullStart at -width.toFloat() }
            if (toEnd.isNotEmpty()) { Reveal.End at slot * toEnd.size; if (fast) Reveal.FullEnd at width.toFloat() }
        })
    }
    val scope = rememberCoroutineScope()
    val close: () -> Unit = { scope.launch { state.animateTo(Reveal.Closed) } }
    val sides by rememberUpdatedState(toStart to toEnd)
    // settledValue, not currentValue: currentValue flips halfway through a drag, before the finger lifts.
    LaunchedEffect(state.settledValue) {
        when (state.settledValue) {
            Reveal.FullStart -> sides.first.firstOrNull()?.run?.invoke()
            Reveal.FullEnd -> sides.second.firstOrNull()?.run?.invoke()
            else -> return@LaunchedEffect
        }
        state.animateTo(Reveal.Closed)
    }
    val x = state.offset.takeUnless { it.isNaN() } ?: 0f
    Box(Modifier.fillMaxWidth().onSizeChanged { width = it.width }) {
        // Composed only while the card is moved that way, so a closed row has no hidden buttons to hit.
        if (x != 0f) Row(
            Modifier.matchParentSize(),
            horizontalArrangement = if (x < 0) Arrangement.End else Arrangement.Start,
            verticalAlignment = Alignment.CenterVertically,
        ) { (if (x < 0) toStart.reversed() else toEnd).forEach { RevealButton(it) { it.run(); close() } } } // the first sits nearest the edge
        // offset mirrors in right-to-left, as anchoredDraggable's drag direction does. No grab while settling,
        // so a scroll that starts on a card still moving back is still a scroll.
        Box(Modifier.offset { IntOffset(x.roundToInt(), 0) }.anchoredDraggable(state, Orientation.Horizontal, startDragImmediately = false)) { content() }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun RevealButton(a: RevealAction, tap: () -> Unit) {
    val b = LocalBrand.current
    var menu by remember { mutableStateOf(false) }
    Box {
        Column(
            Modifier.width(76.dp).clip(RoundedCornerShape(12.dp)).testTag(a.tag)
                .combinedClickable(role = Role.Button, onLongClick = if (a.menu.isEmpty()) null else ({ menu = true }), onClick = tap)
                .padding(vertical = 6.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Box(Modifier.size(44.dp).clip(CircleShape).background(a.color), contentAlignment = Alignment.Center) { Icon(a.icon, null, tint = b.paper) }
            Spacer(Modifier.height(4.dp))
            Text(a.label, color = b.ink, fontSize = 13.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        DropdownMenu(menu, { menu = false }) {
            a.menu.forEach { (label, pick) -> DropdownMenuItem(text = { Text(label) }, onClick = { menu = false; pick() }) }
        }
    }
}

/** A button on a card's bottom row; [button] draws it when a plain text button will not do, [moved] is what it becomes in More. */
internal class CardAction(
    val label: String, val run: () -> Unit, val strong: Boolean = false,
    val moved: List<Pair<String, () -> Unit>> = listOf(label to run), val button: (@Composable () -> Unit)? = null,
)

/**
 * A card. Share, Copy, [menu] and any [actions] the row has no room for sit in its More menu: the first actions
 * go there first, so the last one (Clear) always shows whole, at any width or font size (#734).
 */
@Composable
internal fun Card(n: Notif, menu: List<Pair<String, () -> Unit>> = emptyList(), actions: List<CardAction> = emptyList(), end: @Composable () -> Unit = {}) {
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
                val app = App.of(LocalContext.current)
                val favicons by app.favicons.collectAsState()
                val formatting by app.formatting.collectAsState()
                val autolink by app.autolink.collectAsState()
                val text = remember(n.message, n.format, b, favicons, formatting, autolink) {
                    Md.render(n.message, n.isMarkdown && formatting, link = b.clear, muted = b.slate, icons = favicons, autolink = autolink)
                }
                val body = @Composable { MessageText(text, maxLines = if (open) Int.MAX_VALUE else 6, modifier = Modifier.padding(end = 8.dp)) }
                // Open text can be selected and copied; links open in the browser.
                if (open) SelectionContainer { body() } else body()
                val on by app.linkPreviews.collectAsState()
                val topics by app.topicPreviews.collectAsState()
                if (open && (topics[n.topic] ?: on)) PreviewCard(n, text) // fetched only once someone opens the card
            }
            n.file?.let { f -> Attached(f) }
            val ctx = LocalContext.current
            // The sender's answers (Allow, Deny) show only once the whole text does: an answer approves all of it (#366).
            val answers = if (n.isFinal) emptyList() else TrayButton.replies(n)
            if (answers.isNotEmpty() && !open) Text(stringResource(R.string.tap_to_answer), color = b.slate, fontSize = 13.sp, modifier = Modifier.padding(top = 6.dp))
            if (answers.isNotEmpty() && open) Row(Modifier.horizontalScroll(rememberScrollState())) { // a long label never hides the next one
                var typing by remember { mutableStateOf(false) }
                answers.forEach { a ->
                    TextButton(onClick = { if (a.text) typing = true else App.of(ctx).act(n.id, "reply", org.json.JSONObject().put("reply", a.reply).put("seq", n.seq).toString()) }) { Text(a.label, color = b.ink) }
                }
                answers.firstOrNull { it.text }?.let { a -> if (typing) ReplyBox(n, a) { typing = false } }
            }
            // What was typed, as plain text: never Markdown (#153).
            if (n.replyText.isNotEmpty() && open) Text(stringResource(R.string.answered_text, n.replyText), color = b.body, fontSize = 14.sp, modifier = Modifier.padding(top = 6.dp, end = 8.dp))
            Spacer(Modifier.height(8.dp))
            Text(meta(ctx, n), style = Mono, color = b.slate, maxLines = 2, overflow = TextOverflow.Ellipsis) // 2: a full date is long
            BoxWithConstraints(Modifier.fillMaxWidth()) {
                val measure = rememberTextMeasurer()
                val style = MaterialTheme.typography.labelLarge.copy(fontWeight = FontWeight.SemiBold) // the widest a label draws
                val density = LocalDensity.current
                val px = { d: androidx.compose.ui.unit.Dp -> with(density) { d.roundToPx() } }
                val pad = 2 * px(12.dp) + 1; val least = px(ButtonDefaults.MinWidth) // each side rounds on its own, so 1 px spare
                val keep = fitsFromEnd(constraints.maxWidth - px(96.dp), actions.map { maxOf(measure.measure(it.label, style).size.width + pad, least) }) // 96: More and Mark
                val items = listOf(stringResource(R.string.share) to { share(ctx, n) }, stringResource(R.string.copy) to { copy(ctx, n) }) +
                    actions.dropLast(keep).flatMap { it.moved } + menu
                Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                    MoreMenu(items)
                    MarkButton(App.of(ctx), n)
                    Spacer(Modifier.weight(1f))
                    actions.takeLast(keep).forEach { a ->
                        a.button?.invoke() ?: TextButton(onClick = a.run) {
                            Text(a.label, color = if (a.strong) b.clear else b.ink, fontWeight = if (a.strong) FontWeight.SemiBold else null, maxLines = 1)
                        }
                    }
                    end()
                }
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
internal fun rememberPicture(url: String, px: Int): ImageBitmap? {
    if (url.isEmpty()) return null
    val app = App.of(LocalContext.current)
    val images = app.images
    val a by app.shown.collectAsState()
    var bmp by remember(url) { mutableStateOf(images.cached(url, px, a)) }
    LaunchedEffect(url) {
        if (bmp == null) bmp = withContext(images.dispatcher) { images.load(url, px, a) }
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
    parts += n.markLines
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
private fun StackCard(
    s: Stack, open: Boolean, onClear: (() -> Unit)?, menu: @Composable () -> Unit,
    gone: (@Composable () -> Unit)? = null, // a leaving stack shows why in place of its buttons (#598)
    onToggle: () -> Unit,
) {
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
            if (gone != null) { gone(); return@Row }
            menu()
            if (onClear != null) TextButton(onClick = onClear) { Text(stringResource(R.string.clear), color = b.ink) }
            TextButton(onClick = onToggle) { Text(stringResource(if (open) R.string.stack_hide else R.string.stack_show), color = b.ink) }
        }
    }
}

@Composable
private fun MoreMenu(items: List<Pair<String, () -> Unit>>) {
    val b = LocalBrand.current
    var open by remember { mutableStateOf(false) }
    Box {
        IconButton(onClick = { open = true }) { Icon(Icons.Filled.MoreVert, stringResource(R.string.more), tint = b.slate) }
        DropdownMenu(open, { open = false }) {
            items.forEach { (label, run) -> DropdownMenuItem(text = { Text(label) }, onClick = { open = false; run() }) }
        }
    }
}

/** A stack's menu (#617): Unsubscribe from its topic on every device. */
@Composable
private fun TopicMenu(topic: String, unsubscribe: (String) -> Unit) {
    val b = LocalBrand.current
    var open by remember { mutableStateOf(false) }
    Box {
        IconButton(onClick = { open = true }) { Icon(Icons.Filled.MoreVert, stringResource(R.string.more), tint = b.slate) }
        DropdownMenu(open, { open = false }) {
            DropdownMenuItem(text = { Text(stringResource(R.string.unsubscribe_from, topic)) }, onClick = { open = false; unsubscribe(topic) })
        }
    }
}

/** Unsubscribing from [topic], which carries approvals, also hides approval prompts (SPEC 2.3), so it asks first. Null shows nothing. */
@Composable
private fun ConfirmUnsubscribe(topic: String?, dismiss: () -> Unit, go: (String) -> Unit) {
    topic ?: return
    val b = LocalBrand.current
    AlertDialog(
        onDismissRequest = dismiss,
        title = { Text(stringResource(R.string.subs_agents_title, topic)) },
        text = { Text(stringResource(R.string.subs_agents_text)) },
        confirmButton = { TextButton(onClick = { dismiss(); go(topic) }) { Text(stringResource(R.string.unsubscribe), color = b.signal, fontWeight = FontWeight.SemiBold) } },
        dismissButton = { TextButton(onClick = dismiss) { Text(stringResource(R.string.link_cancel)) } },
    )
}

/**
 * The Topics setting (#617): what a new topic starts as, then a switch per topic with its open count and last
 * message, searchable when the list is long. Every topic shows, so one turned off can always be turned on again.
 * [gone] says the server has no subscriptions route after all.
 */
@Composable
private fun SubscriptionSettings(app: App, resumes: Int, gone: () -> Unit) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val a = remember { app.shown.value } // the server this screen was drawn for; a job queued before a switch still writes to it (#78)
    val subs by app.subs.collectAsState()
    val filtered = subs?.filtered == true
    var list by remember { mutableStateOf<SubsList?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var find by rememberSaveable { mutableStateOf("") }
    var asking by remember { mutableStateOf<String?>(null) }
    var reading by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(subs) { if (reading?.let { subs?.subscribed(it) } == true) reading = null } // subscribed elsewhere: the cards are in the inbox now
    val why = { e: Exception -> (e as? HttpError)?.message ?: ctx.getString(R.string.sound_no_connection) }
    LaunchedEffect(subs, resumes) { // a change from another device comes as a new settings message
        if (!filtered) return@LaunchedEffect
        val api = app.api()
        app.io.execute {
            try { a.loadSubs(api)?.let { list = it; error = null } ?: gone() } catch (e: Exception) { error = why(e) }
        }
    }
    val sending = remember { java.util.concurrent.atomic.AtomicInteger() }
    val put = { body: org.json.JSONObject ->
        // The switch flips at once, so a second tap on a slow connection sends the other value.
        val t = body.optJSONObject("topics")
        list = list?.let { l -> l.copy(newTopics = body.optBoolean("new_topics", l.newTopics), topics = l.topics.map { s -> if (t?.has(s.topic) == true) s.copy(subscribed = t.getBoolean(s.topic)) else s }) }
        val api = app.api()
        sending.incrementAndGet()
        app.io.execute { // one thread, so changes reach the server in tap order; only the last answer shows
            try { a.putSubs(body, api).let { if (sending.decrementAndGet() == 0) list = it }; error = null } catch (e: Exception) {
                error = why(e)
                if (sending.decrementAndGet() == 0) runCatching { a.loadSubs(api) }.getOrNull()?.let { list = it } // nothing changed on the server
            }
        }
    }
    if (!filtered) return Text(stringResource(R.string.subs_unfiltered), color = b.slate, fontSize = 13.sp)
    error?.let { Text(stringResource(R.string.subs_failed, it), color = b.signal, fontSize = 13.sp) }
    val l = list ?: return
    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.weight(1f)) {
            Text(stringResource(R.string.subs_new), color = b.ink)
            Text(stringResource(R.string.subs_new_explain), color = b.slate, fontSize = 13.sp)
        }
        Switch(checked = l.newTopics, onCheckedChange = { put(org.json.JSONObject().put("new_topics", it)) }, modifier = Modifier.testTag("subs-new"))
    }
    if (l.topics.size > 8) OutlinedTextField(
        find, { find = it }, label = { Text(stringResource(R.string.subs_search)) }, singleLine = true,
        modifier = Modifier.fillMaxWidth().testTag("subs-search"),
    )
    l.topics.filter { it.topic.contains(find.trim(), true) }.forEach { t ->
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.weight(1f)) {
                Text(t.topic, color = b.ink)
                val last = t.lastAt?.let(::millisOf)?.let { ago(ctx, it) } ?: stringResource(R.string.subs_never)
                Text(stringResource(R.string.subs_open, t.open) + "  ·  " + last, style = Mono, color = b.slate)
                if (t.approvals) Text(stringResource(R.string.subs_agents_note), color = b.signal, fontSize = 13.sp)
            }
            if (!t.subscribed) TextButton({ reading = t.topic }, Modifier.testTag("read-${t.topic}")) { Text(stringResource(R.string.subs_read), color = b.ink) }
            Switch(checked = t.subscribed, modifier = Modifier.testTag("sub-${t.topic}"), onCheckedChange = { on ->
                if (!on && t.approvals) asking = t.topic else put(topicBody(t.topic, on))
            })
        }
    }
    ConfirmUnsubscribe(asking, { asking = null }) { put(topicBody(it, false)) }
    reading?.let { topic -> TopicReader(app, topic, resumes, { reading = null }) { put(topicBody(topic, true)); reading = null } }
}

/**
 * An unsubscribed topic's open cards, read without subscribing (SPEC 2.3, #624). The rows stay out of Room
 * and the tray. A snapshot, no stream of its own: it reloads on open, resume, a reset, and an action's answer.
 */
@Composable
private fun TopicReader(app: App, topic: String, resumes: Int, close: () -> Unit, subscribe: () -> Unit) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    var rows by remember { mutableStateOf<List<Notif>?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    val again by app.outside.collectAsState()
    LaunchedEffect(topic, resumes, again) {
        val api = app.api()
        app.io.execute {
            try { rows = api.listOpen("&subscribed=all&topic=" + android.net.Uri.encode(topic)).rows; error = null }
            catch (e: Exception) { error = (e as? HttpError)?.message ?: ctx.getString(R.string.sound_no_connection) }
        }
    }
    androidx.compose.ui.window.Dialog(close, androidx.compose.ui.window.DialogProperties(usePlatformDefaultWidth = false)) {
        Surface(color = b.paper, modifier = Modifier.fillMaxSize().testTag("reader")) {
            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text(topic, color = b.ink, fontSize = 20.sp, fontWeight = FontWeight.SemiBold)
                        Text(stringResource(R.string.subs_reading), color = b.slate, fontSize = 13.sp)
                    }
                    TextButton(subscribe, Modifier.testTag("reader-subscribe")) { Text(stringResource(R.string.subs_subscribe), color = b.ink) }
                    TextButton(close) { Text(stringResource(R.string.subs_close), color = b.slate) }
                }
                error?.let { Text(it, color = b.signal, fontSize = 13.sp) }
                if (rows?.isEmpty() == true) Text(stringResource(R.string.subs_read_empty), color = b.slate)
                LazyColumn(verticalArrangement = Arrangement.spacedBy(10.dp)) { items(rows.orEmpty(), key = { it.id }) { Card(it) } }
            }
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

/**
 * Seen or Actioned, with an optional note and link, as on the web (#451). By hand only: opening a card
 * never marks it. A mark clears nothing and never rings. Remove takes only this phone's own mark.
 */
@Composable
private fun MarkButton(app: App, n: Notif) {
    val b = LocalBrand.current
    val me by app.markId.collectAsState()
    val mine = n.markBy(me)
    var open by remember { mutableStateOf(false) }
    val label = when (mine?.optString("mark")) {
        "actioned" -> stringResource(R.string.marked_actioned)
        "seen" -> stringResource(R.string.marked_seen)
        else -> stringResource(R.string.mark)
    }
    IconButton(onClick = { open = true }) { Icon(Icons.Filled.Check, label, tint = if (mine != null) b.ink else b.slate) }
    if (!open) return
    var note by remember(mine != null) { mutableStateOf(mine?.optString("note").orEmpty()) } // refill if ready names our mark late
    var link by remember(mine != null) { mutableStateOf(mine?.optString("link").orEmpty()) }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val send = { kind: String? ->
        busy = true
        app.mark(n.id, kind, note.trim(), link.trim()) { e -> busy = false; error = e; if (e == null) open = false }
    }
    AlertDialog(
        onDismissRequest = { if (!busy) open = false }, // a pending send would close a reopened dialog
        title = { Text(label) },
        text = {
            Column {
                OutlinedTextField(note, { note = it }, label = { Text(stringResource(R.string.mark_note)) }, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(link, { link = it }, label = { Text(stringResource(R.string.mark_link)) }, singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri), modifier = Modifier.fillMaxWidth())
                error?.let { Text(stringResource(R.string.mark_failed, it), color = b.signal, modifier = Modifier.padding(top = 8.dp)) }
                if (mine != null) TextButton(onClick = { send(null) }, enabled = !busy) { Text(stringResource(R.string.mark_remove), color = b.slate) }
            }
        },
        confirmButton = {
            Row {
                TextButton(onClick = { send("seen") }, enabled = !busy) { Text(stringResource(R.string.mark_seen), color = b.ink) }
                TextButton(onClick = { send("actioned") }, enabled = !busy) { Text(stringResource(R.string.mark_actioned), color = b.ink, fontWeight = FontWeight.SemiBold) }
            }
        },
        dismissButton = { TextButton(onClick = { open = false }, enabled = !busy) { Text(stringResource(R.string.link_cancel)) } },
    )
}

/** The reply box for a text action (#153): sends the trimmed text, 1 to 1000 characters; a refused one comes back. */
@Composable
private fun ReplyBox(n: Notif, a: TrayButton, close: () -> Unit) {
    val b = LocalBrand.current
    val app = App.of(LocalContext.current)
    val drafts = remember { app.drafts } // the server this box opened on, after a switch too (#860)
    var text by rememberSaveable { mutableStateOf(drafts[n.id].orEmpty()) }
    val seq = rememberSaveable { n.seq } // the question as the box opened: an edit since then gets 409, never this answer
    val body = TrayButton.typed(a.reply!!, text, seq)
    // The box leaving for any reason but Send or Cancel (a 409, or an edit that drops the typed answer) keeps its text
    // for the next box (#516, #539), a rotation too, since the box closes when the screen is rebuilt.
    var ended by remember { mutableStateOf(false) }
    androidx.compose.runtime.DisposableEffect(Unit) { onDispose { keepDraft(drafts, n.id, text, ended) } }
    // An earlier answer came back 409: this box answers the old question too. Close it.
    val changed0 = remember { app.changed[n.id] ?: 0 }
    LaunchedEffect(app.changed[n.id]) { if ((app.changed[n.id] ?: 0) != changed0) close() }
    val count = text.trim().let { it.codePointCount(0, it.length) }
    AlertDialog(
        onDismissRequest = close,
        title = { Text(a.label) },
        text = {
            Column {
                OutlinedTextField(text, { text = it }, modifier = Modifier.fillMaxWidth())
                Text(stringResource(R.string.reply_text_count, count), style = Mono, color = if (count > 1000) b.signal else b.slate, modifier = Modifier.padding(top = 4.dp))
            }
        },
        confirmButton = {
            TextButton(onClick = { ended = true; drafts.remove(n.id); app.act(n.id, "reply", body!!); close() }, enabled = body != null) {
                Text(stringResource(R.string.reply_text_send), color = b.ink, fontWeight = FontWeight.SemiBold)
            }
        },
        dismissButton = { TextButton(onClick = { ended = true; close() }) { Text(stringResource(R.string.link_cancel)) } },
    )
}

/** Keeps [text] as the draft for [id] when a reply box goes away unasked ([ended] false); an emptied box drops it. */
internal fun keepDraft(drafts: MutableMap<String, String>, id: String, text: String, ended: Boolean) {
    if (ended) return
    if (text.isBlank()) drafts.remove(id) else drafts[id] = text
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
    // The server says if this phone may change it (#433); an older server sends no can_approve.
    val me = list.firstOrNull { it.optBoolean("this") } ?: return emptyList()
    if (!me.optBoolean("can_approve", me.optString("approve") == "allowed")) return emptyList()
    val i = approveOrder.indexOf(d.optString("approve"))
    return if (i >= 0 && isOwn(list, d)) approveOrder.take(i + 1) else approveOrder
}

/**
 * A refused device change in plain words. The server's 403 `approve_denied` means this phone is not
 * allowed to change it (an allowed device can); anything else keeps the server's own message.
 */
internal fun refusal(ctx: Context, e: Throwable): String =
    if ((e as? HttpError)?.let { it.code == 403 && it.reason == "approve_denied" } == true) ctx.getString(R.string.device_not_allowed)
    else ctx.getString(R.string.device_approve_failed, e.message)

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
    var closed by remember(server) { mutableStateOf(false) } // the server refused the list: a publish-only phone
    var error by remember { mutableStateOf<String?>(null) }
    var clearsError by remember { mutableStateOf<String?>(null) } // shown by the switch, which sits far above the list's error line
    var showLog by rememberSaveable { mutableStateOf(false) }
    var menu by remember { mutableStateOf<String?>(null) }
    var lower by remember { mutableStateOf<Pair<org.json.JSONObject, String>?>(null) }
    // The server decides (docs/SPEC.md "Who may approve"): a refusal shows as the error line.
    var busy by remember { mutableStateOf(false) } // a change is on its way; the menu waits for the fresh list
    val pending = remember { java.util.concurrent.atomic.AtomicInteger() } // changes not yet sent, so a reload queued before one keeps the controls off (#777)
    val change: (org.json.JSONObject, String, org.json.JSONObject) -> Unit = { d, what, body ->
        busy = true
        pending.incrementAndGet()
        val api = app.api()
        app.io.execute {
            val out = runCatching { api.put("/api/v1/devices/${android.net.Uri.encode(d.optString("id"))}/$what", body.toString()) }
            // The server answers with the device: show it at once, so a failed reload never leaves the old value up.
            out.getOrNull()?.let { r -> runCatching { org.json.JSONObject(r) }.getOrNull() }?.let { nd ->
                devices = devices?.map { o -> if (o.optString("id") != nd.optString("id")) o else org.json.JSONObject(o.toString()).also { c -> nd.keys().forEach { k -> if (k != "this") c.put(k, nd.get(k)) } } }
            }
            val e = out.exceptionOrNull()?.let { refusal(ctx, it) }
            if (what == "clears") { clearsError = e; error = null } else { error = e; clearsError = null }
            pending.decrementAndGet()
            reload++
        }
    }
    val setApprove: (org.json.JSONObject, String) -> Unit = { d, v -> change(d, "approve", org.json.JSONObject().put("approve", v)) }
    val changed by app.devicesChanged.collectAsState() // another device changed this phone's approve setting, or one came or went
    LaunchedEffect(server, resumes, reload, changed) {
        val api = app.api()
        app.io.execute {
            val got = runCatching { objects(org.json.JSONObject(api.get("/api/v1/devices?all=1")), "devices") }
            got.getOrNull()?.let { devices = it }
            val refused = got.exceptionOrNull() as? HttpError
            if (refused?.isAuth == true) devices = null // a signed-out phone shows no old list
            closed = refused?.code == 403
            // checked on the main thread, where a tap sets busy, so a tap between the check and the write is not lost
            android.os.Handler(android.os.Looper.getMainLooper()).post { busy = pending.get() > 0 }
            runCatching { objects(org.json.JSONObject(api.get("/api/v1/devices/log")), "events") }.getOrNull()?.let { log = it }
        }
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
                        error = runCatching { api.delete(unlinkPath(d)) }
                            .exceptionOrNull()?.let { ctx.getString(R.string.device_unlink_failed, it.message) }
                        reload++
                    }
                }) { Text(stringResource(R.string.device_unlink), color = b.signal) }
            },
            dismissButton = { TextButton(onClick = { unlink = null }) { Text(stringResource(R.string.link_cancel)) } },
        )
    }
    val list = devices ?: run {
        // A publish-only phone may not read the list, but it may still unlink itself (#643).
        if (closed) {
            Spacer(Modifier.height(8.dp))
            Text(stringResource(R.string.devices_title), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
            val me = stringResource(R.string.this_phone)
            TextButton(onClick = { unlink = org.json.JSONObject().put("this", true).put("name", me) }) { Text(stringResource(R.string.device_unlink_this), color = b.signal) }
            error?.let { Text(it, color = b.signal, fontSize = 13.sp) }
        }
        return
    }
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
    // Clears everywhere (#767): shown to every phone so it sees where its clears go; the server lets only an
    // allowed one change it, and its refusal shows as the error line. Absent from a server older than #767.
    list.firstOrNull { it.optBoolean("this") }?.takeIf { it.has("clears_everywhere") }?.let { me ->
        Spacer(Modifier.height(8.dp))
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.weight(1f)) {
                Text(stringResource(R.string.clears_title), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
                Text(stringResource(if (me.optBoolean("clears_everywhere")) R.string.clears_on else R.string.clears_off), color = b.slate, fontSize = 13.sp)
            }
            Switch(checked = me.optBoolean("clears_everywhere"), enabled = !busy, modifier = Modifier.testTag("clears-everywhere"),
                onCheckedChange = { change(me, "clears", org.json.JSONObject().put("everywhere", it)) })
        }
        clearsError?.let { Text(it, color = b.signal, fontSize = 13.sp) }
    }
    Spacer(Modifier.height(8.dp))
    Text(stringResource(R.string.devices_title), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
    Text(stringResource(R.string.devices_explain), color = b.slate, fontSize = 13.sp)
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
}

/** This phone unlinks itself by "this", which needs no read scope and no id (#643). */
internal fun unlinkPath(d: org.json.JSONObject) =
    if (d.optBoolean("this")) "/api/v1/devices/this" else "/api/v1/devices/${android.net.Uri.encode(d.optString("id"))}"

/** A sender's kind (docs/SPEC.md section 5.1) in the web's words. */
internal fun senderKind(kind: String) = when (kind) {
    "person" -> R.string.sender_person
    "ntfy_user" -> R.string.sender_ntfy_user
    "gotify_app" -> R.string.sender_gotify_app
    "pushover_app" -> R.string.sender_pushover_app
    "anonymous" -> R.string.sender_anonymous
    else -> R.string.sender_token
}

/** "alerts (3), ci (1)": a sender's topics by name, as the web lists them. */
internal fun senderTopics(s: org.json.JSONObject): String {
    val t = s.optJSONObject("topics") ?: return ""
    return t.keys().asSequence().sorted().joinToString(", ") { it + " (" + t.optLong(it) + ")" }
}

/** What a sender's detail says in place of its recent list, or null when it has some (#530: never "nothing" while it has sent). */
internal fun senderEmpty(s: org.json.JSONObject) = when {
    (s.optJSONArray("recent")?.length() ?: 0) > 0 -> null
    s.optLong("count") > 0 -> R.string.sender_none_stored
    else -> R.string.sender_none_sent
}

/**
 * Every token, app and person that has sent here (#119, docs/SPEC.md section 5.1), read only.
 * Only a full admin may list them; for any other token the server says no and this stays hidden.
 */
@Composable
private fun SenderSettings(app: App, resumes: Int, state: androidx.compose.runtime.MutableState<List<org.json.JSONObject>?>) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val server by app.saved.collectAsState()
    var senders by state // fetched by SettingsScreen, which hides the Integrations section while it is null
    var open by remember(server) { mutableStateOf<String?>(null) }
    var detail by remember(server) { mutableStateOf<org.json.JSONObject?>(null) }
    var reload by remember { mutableStateOf(0) }
    LaunchedEffect(open, resumes, reload) {
        val id = open ?: return@LaunchedEffect
        val api = app.api()
        app.io.execute {
            val path = "/api/v1/senders/${android.net.Uri.encode(id)}"
            // Its change log rides along in the same object, under "log" (a key the server never sends).
            runCatching { org.json.JSONObject(api.get(path)).put("log", runCatching { org.json.JSONObject(api.get("$path/changes")).optJSONArray("changes") }.getOrNull()) }
                .onSuccess { detail = it }
                .onFailure { if ((it as? HttpError)?.isAuth == true) { senders = null; detail = null } } // no longer a full admin: hide it all
        }
    }
    val list = senders ?: return
    Spacer(Modifier.height(8.dp))
    Text(stringResource(R.string.senders_title), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
    Text(stringResource(if (list.isEmpty()) R.string.senders_none else R.string.senders_explain), color = b.slate, fontSize = 13.sp)
    list.forEach { s ->
        val id = s.optString("id")
        Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(8.dp)).clickable(role = Role.Button) { open = if (open == id) null else id }.padding(vertical = 4.dp)) {
            Text(s.optString("name"), color = b.ink)
            val kind = stringResource(senderKind(s.optString("kind")))
            val template = s.optString("template").takeIf { it.isNotEmpty() }?.let { ", " + ctx.getString(R.string.sender_template, it) }.orEmpty()
            val seen = millisOf(s.optString("last_seen"))?.let { ago(ctx, it) }.orEmpty()
            Text(ctx.getString(R.string.sender_line, kind + template, s.optLong("count"), seen), color = b.slate, fontSize = 13.sp)
            senderTopics(s).takeIf { it.isNotEmpty() }?.let { Text(it, style = Mono, color = b.slate, fontSize = 12.sp) }
        }
        // Only the detail for the sender open now: a slow answer for one closed since is not shown under another.
        val d = detail?.takeIf { open == id && it.optString("id") == id } ?: return@forEach
        Column(Modifier.padding(start = 12.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            d.optString("user_agent").takeIf { it.isNotEmpty() }?.let { Text(ctx.getString(R.string.sender_agent, it), color = b.slate, fontSize = 13.sp) }
            d.optString("address").takeIf { it.isNotEmpty() }?.let { Text(ctx.getString(R.string.sender_address, it), color = b.slate, fontSize = 13.sp) }
            millisOf(d.optString("first_seen"))?.takeIf { it > 0 }?.let { Text(ctx.getString(R.string.sender_first_seen, ago(ctx, it)), color = b.slate, fontSize = 13.sp) }
            Text(stringResource(R.string.sender_recent), color = b.ink, fontWeight = FontWeight.SemiBold)
            senderEmpty(d)?.let { Text(stringResource(it), color = b.slate, fontSize = 13.sp) }
            objects(d, "recent").forEach { n ->
                val at = millisOf(n.optString("created_at"))?.let { ago(ctx, it) }.orEmpty()
                Text(n.optString("title").ifEmpty { n.optString("message") }, color = b.ink, maxLines = 2)
                val state = stateLabel(n.optString("state"))?.let { ctx.getString(it) }.orEmpty()
                Text(ctx.getString(R.string.device_log_line, at, n.optString("topic"), state).trimEnd(), style = Mono, color = b.slate, fontSize = 12.sp)
            }
            SenderRules(app, d) { reload++ }
        }
    }
}

/** A sender's rules as the server keeps them (docs/SPEC.md section 5.1), in a few words, like the web's change log. */
internal fun rulesSummary(ctx: Context, r: org.json.JSONObject?): String {
    r ?: return ctx.getString(R.string.rules_none)
    val p = mutableListOf<String>()
    if (r.optBoolean("paused")) p += ctx.getString(R.string.rules_paused)
    if (r.optBoolean("muted")) p += ctx.getString(R.string.rules_muted)
    val lo = r.optInt("priority_min")
    val hi = r.optInt("priority_max")
    if (lo > 0 || hi > 0) p += ctx.getString(R.string.rules_priority, lo.takeIf { it > 0 } ?: 1, hi.takeIf { it > 0 } ?: 5)
    val routes = r.optJSONObject("routes")
    routes?.keys()?.asSequence()?.sorted()?.forEach { k ->
        val to = routes.optJSONArray(k)?.let { a -> List(a.length()) { a.optString(it) } }.orEmpty()
        p += ctx.getString(R.string.rules_route, if (k == "*") ctx.getString(R.string.rules_any_topic) else k,
            to.joinToString(", ").ifEmpty { ctx.getString(R.string.rules_nowhere) })
    }
    r.optString("expect").takeIf { it.isNotEmpty() }?.let { p += ctx.getString(R.string.rules_quiet, it) }
    r.optString("alert_topic").takeIf { it.isNotEmpty() }?.let { p += ctx.getString(R.string.rules_alarm_to, it) }
    return p.joinToString(", ").ifEmpty { ctx.getString(R.string.rules_none) }
}

/** Routes as the box edits them, "topic: target, target" one a line, the same form as the web. */
internal fun routeLines(r: org.json.JSONObject?): String {
    val routes = r?.optJSONObject("routes") ?: return ""
    return routes.keys().asSequence().sorted().joinToString("") { k ->
        val a = routes.optJSONArray(k)
        k + ": " + List(a?.length() ?: 0) { a!!.optString(it) }.joinToString(", ") + "\n"
    }
}

/** The routes typed in the box as the server takes them, or null for a line with no topic and colon, or a topic twice (as the web refuses). */
internal fun parseRoutes(text: String): org.json.JSONObject? {
    val out = org.json.JSONObject()
    text.lines().map { it.trim() }.filter { it.isNotEmpty() }.forEach { l ->
        val k = l.substringBefore(':', "").trim()
        if (k.isEmpty() || out.has(k)) return null
        out.put(k, org.json.JSONArray(l.substringAfter(':').split(',').map { it.trim() }.filter { it.isNotEmpty() }))
    }
    return out
}

/**
 * Pause, mute, priority range, routes and the quiet alarm for one sender, and its change log with Undo (#119, docs/SPEC.md 5.1).
 * Each control sends only its own field, so a screen loaded before someone else's change never undoes theirs.
 */
@Composable
internal fun SenderRules(app: App, d: org.json.JSONObject, changed: () -> Unit) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val rules = d.optJSONObject("rules") ?: org.json.JSONObject()
    val path = "/api/v1/senders/${android.net.Uri.encode(d.optString("id"))}"
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember(d) { mutableStateOf(false) } // stays on until the reloaded sender arrives, so a control never shows stale rules
    val send: (() -> Unit) -> Unit = { call ->
        busy = true
        error = null
        app.io.execute {
            runCatching(call).onSuccess { changed() }
                .onFailure { error = ctx.getString(R.string.rules_failed, it.message); busy = false }
        }
    }
    val api = app.api()
    val put: (org.json.JSONObject) -> Unit = { body -> send { api.put(path, body.toString()) } }
    Spacer(Modifier.height(8.dp))
    Text(stringResource(R.string.rules_title), color = b.ink, fontWeight = FontWeight.SemiBold)
    Text(stringResource(R.string.rules_explain), color = b.slate, fontSize = 13.sp)
    listOf("paused" to R.string.rules_pause, "muted" to R.string.rules_mute).forEach { (key, label) ->
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Text(stringResource(label), color = b.ink, modifier = Modifier.weight(1f))
            Switch(checked = rules.optBoolean(key), enabled = !busy, onCheckedChange = { put(org.json.JSONObject().put(key, it)) })
        }
    }
    val names = listOf(R.string.rules_no_limit, R.string.send_p1, R.string.send_p2, R.string.send_p3, R.string.send_p4, R.string.send_p5)
    listOf("priority_min" to R.string.rules_lowest, "priority_max" to R.string.rules_highest).forEach { (key, label) ->
        var menu by remember { mutableStateOf(false) }
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Text(stringResource(label), color = b.ink, modifier = Modifier.weight(1f))
            Box {
                TextButton(onClick = { menu = true }, enabled = !busy) { Text(stringResource(names[rules.optInt(key).coerceIn(0, 5)]), color = b.ink) }
                DropdownMenu(menu, { menu = false }) {
                    names.forEachIndexed { i, n ->
                        DropdownMenuItem(text = { Text(stringResource(n)) }, onClick = { menu = false; if (i != rules.optInt(key)) put(org.json.JSONObject().put(key, i)) })
                    }
                }
            }
        }
    }
    // Typed text is kept while editing and after a refusal; a new saved value replaces it.
    @Composable fun Field(key: String, saved: String, label: Int, hint: Int, save: Int, lines: Int, value: (String) -> Any?) {
        var text by remember(d.optString("id"), saved) { mutableStateOf(saved) }
        OutlinedTextField(text, { text = it }, enabled = !busy, label = { Text(stringResource(label)) }, minLines = lines, singleLine = lines == 1,
            textStyle = Mono, modifier = Modifier.fillMaxWidth().testTag("rule-$key"))
        Text(stringResource(hint), color = b.slate, fontSize = 12.sp)
        TextButton(enabled = !busy && text != saved, onClick = {
            value(text)?.let { put(org.json.JSONObject().put(key, it)) } ?: run { error = ctx.getString(R.string.rules_routes_bad) }
        }) { Text(stringResource(save), color = b.ink) }
    }
    Field("routes", routeLines(rules), R.string.rules_routes, R.string.rules_routes_hint, R.string.rules_save_routes, 2, ::parseRoutes)
    // The quiet alarm (#119 part 3): off until set, an emptied field turns it off; the server checks the value.
    Field("expect", rules.optString("expect"), R.string.rules_expect, R.string.rules_expect_hint, R.string.rules_save_expect, 1) { it.trim() }
    Field("alert_topic", rules.optString("alert_topic"), R.string.rules_alert_topic, R.string.rules_alert_topic_hint, R.string.rules_save_alert_topic, 1) { it.trim() }
    d.optString("alarm").takeIf { it.isNotEmpty() }?.let { Text(stringResource(R.string.rules_alarm_open, it), color = b.signal, fontSize = 13.sp) }
    error?.let { Text(it, color = b.signal, fontSize = 13.sp) }
    Text(stringResource(R.string.rules_changes), color = b.ink, fontWeight = FontWeight.SemiBold)
    val log = objects(d, "log")
    if (log.isEmpty()) Text(stringResource(R.string.rules_no_changes), color = b.slate, fontSize = 13.sp)
    log.forEach { c ->
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.weight(1f)) {
                Text(rulesSummary(ctx, c.optJSONObject("after")), color = b.ink, fontSize = 14.sp)
                val at = millisOf(c.optString("at"))?.let { ago(ctx, it) }.orEmpty()
                Text(ctx.getString(R.string.rules_was, rulesSummary(ctx, c.optJSONObject("before")), c.optString("by"), at), color = b.slate, fontSize = 12.sp)
            }
            TextButton(enabled = !busy, onClick = { send { api.post("$path/changes/${c.optLong("seq")}/undo") } }) { Text(stringResource(R.string.rules_undo), color = b.ink) }
        }
    }
}

/**
 * One send from the Send box at a time (#523). Held on [App], not in the box, so a box rebuilt after
 * a rotation still sees the send in flight and its result. [sent] counts successes for Home to notice.
 */
class Sender(private val run: (Runnable) -> Unit) {
    var busy by mutableStateOf(false); private set
    var err by mutableStateOf<String?>(null)
    var sent by mutableStateOf(0); private set
    /** This box's notification went out; no more sends until the box opens again, even in the moment before it closes. */
    var done by mutableStateOf(false); private set
    private var shown = 0 // sends whose Sent message finished showing

    /** Runs [show] for the Sent message once per send. A screen rebuilt while it showed (a rotation) shows it again (#532). */
    suspend fun confirm(show: suspend () -> Unit) {
        val n = sent
        if (n <= shown) return
        show()
        shown = n
    }

    /** A fresh box: no old reason, ready to send. Does nothing while a send is in flight. */
    fun open() { if (!busy) { err = null; done = false } }

    /** Starts [post] unless a send is in flight or already done; false then. [why] turns a failure into the line the box shows. */
    fun send(post: () -> Unit, why: (Throwable) -> String): Boolean {
        if (busy || done) return false
        busy = true
        err = null
        run(Runnable {
            runCatching(post).onSuccess { done = true; sent++ }.onFailure { err = why(it) }
            busy = false
        })
        return true
    }
}

/** The body of POST /api/v1/notifications from the Send box (#150), or null without a topic or without both title and message. */
internal fun sendBody(topic: String, title: String, message: String, priority: Int): org.json.JSONObject? {
    if (topic.isBlank() || (message.isBlank() && title.isBlank())) return null // the server needs only a topic; a blank one says nothing
    return org.json.JSONObject().put("topic", topic.trim()).put("message", message.trim()).put("priority", priority)
        .apply { if (title.isNotBlank()) put("title", title.trim()) }
}

/**
 * Send a notification from this phone with its own token (#150), like the web inbox's Send box:
 * topic, title, message, priority. An error keeps the box open with the server's reason.
 */
@Composable
private fun SendBox(app: App, topic0: String, close: () -> Unit) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    var topic by rememberSaveable { mutableStateOf(topic0) }
    var title by rememberSaveable { mutableStateOf("") }
    var message by rememberSaveable { mutableStateOf("") }
    var priority by rememberSaveable { mutableStateOf(3) }
    val s = app.sender
    val busy = s.busy || s.done
    val body = sendBody(topic, title, message, priority)
    AlertDialog(
        onDismissRequest = { if (!busy) close() },
        title = { Text(stringResource(R.string.send_title)) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(stringResource(R.string.send_hint), color = b.slate, fontSize = 13.sp)
                OutlinedTextField(topic, { topic = it }, label = { Text(stringResource(R.string.send_topic)) }, singleLine = true, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(title, { title = it }, label = { Text(stringResource(R.string.send_title_field)) }, singleLine = true, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(message, { message = it }, label = { Text(stringResource(R.string.send_message)) }, minLines = 3, modifier = Modifier.fillMaxWidth())
                Text(stringResource(R.string.send_priority), color = b.slate, fontSize = 13.sp)
                Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    listOf(R.string.send_p1, R.string.send_p2, R.string.send_p3, R.string.send_p4, R.string.send_p5).forEachIndexed { i, label ->
                        FilterChip(selected = priority == i + 1, onClick = { priority = i + 1 }, label = { Text(stringResource(label)) })
                    }
                }
                s.err?.let { Text(it, color = b.signal, fontSize = 13.sp) }
            }
        },
        confirmButton = {
            TextButton(enabled = body != null && !busy, onClick = { // one tap, one notification, across a rotation too
                val api = app.api()
                s.send({ api.post("/api/v1/notifications", body.toString()) }) {
                    ctx.getString(R.string.send_failed, (it as? HttpError)?.message?.substringAfter(": ")?.ifBlank { it.message } ?: it.message)
                }
            }) { Text(stringResource(R.string.send)) }
        },
        dismissButton = { TextButton(enabled = !busy, onClick = close) { Text(stringResource(R.string.link_cancel)) } },
    )
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

/**
 * Which buttons each swipe shows (#161), saved on the server so every device shows the same. A tap adds a button
 * last on that side (moving it from the other) or takes it off. A token that may not change them sees them read-only.
 */
/** The display switches (#107), shared with every device through the server like the swipe buttons. */
@Composable
private fun DisplaySettings(app: App) {
    val b = LocalBrand.current
    val locked by app.swipesLocked.collectAsState()
    val a = remember { app.shown.value } // the server this screen was drawn for; a job queued before a switch still writes to it (#78)
    var result by remember { mutableStateOf<String?>(null) }
    Text(stringResource(R.string.formatting_settings), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
    Text(stringResource(if (locked) R.string.formatting_locked else R.string.formatting_explain), color = b.slate, fontSize = 13.sp)
    listOf(
        Triple("render_formatting", app.formatting, R.string.render_formatting), Triple("autolink", app.autolink, R.string.autolink),
        Triple("favicons", app.favicons, R.string.favicons), Triple("link_previews", app.linkPreviews, R.string.link_previews),
    ).forEach { (key, flow, label) ->
        val on by flow.collectAsState()
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Text(stringResource(label), color = b.body, fontSize = 14.sp, modifier = Modifier.weight(1f))
            Switch(checked = on, enabled = !locked, modifier = Modifier.testTag("display-$key"), onCheckedChange = { v -> app.io.execute { result = a.saveDisplay(key, v) } })
        }
    }
    result?.let { Text(it, color = b.signal, fontSize = 13.sp) }
}

@Composable
private fun SwipeSettings(app: App) {
    val b = LocalBrand.current
    val s by app.swipes.collectAsState()
    val a = remember { app.shown.value } // the server this screen was drawn for; a job queued before a switch still writes to it (#78)
    val locked by app.swipesLocked.collectAsState()
    var result by remember { mutableStateOf<String?>(null) }
    val full = stringResource(R.string.swipe_full, Swipes.MAX)
    val names = mapOf(
        "clear" to stringResource(R.string.clear), "snooze" to stringResource(R.string.tray_snooze),
        "ack" to stringResource(R.string.ack), "resolve" to stringResource(R.string.swipe_resolve),
    )
    Spacer(Modifier.height(8.dp))
    Text(stringResource(R.string.swipe_settings), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
    Text(stringResource(if (locked) R.string.swipe_locked else R.string.swipe_explain), color = b.slate, fontSize = 13.sp)
    val rtl = LocalLayoutDirection.current == LayoutDirection.Rtl // the gestures mirror, so the words swap (#568)
    listOf(true, false).forEach { toStart ->
        val side = if (toStart != rtl) R.string.swipe_left else R.string.swipe_right
        val mine = if (toStart) s.start else s.end
        Text(stringResource(side), color = b.body, fontSize = 14.sp)
        Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Swipes.NAMES.forEach { name ->
                val at = mine.indexOf(name)
                val label = names.getValue(name)
                FilterChip(
                    selected = at >= 0, enabled = !locked,
                    onClick = {
                        // From the latest value on io, not this frame's: two quick taps both count.
                        app.io.execute { val next = a.swipes.value.toggle(name, toStart); result = if (next == null) full else a.saveSwipes(next) }
                    },
                    label = { Text(if (at >= 0) stringResource(R.string.swipe_place, at + 1, label) else label) },
                )
            }
        }
    }
    result?.let { Text(stringResource(R.string.snooze_not_saved, it), color = b.body, fontSize = 14.sp) }
}

@Composable
private fun SnoozeSettings(app: App) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val s by app.snooze.collectAsState()
    val server by app.saved.collectAsState() // a switch to another server drops unsaved edits
    var times by rememberSaveable(server) { mutableStateOf(s.times.joinToString(", ")) }
    var def by rememberSaveable(server) { mutableStateOf(s.default) }
    var result by remember { mutableStateOf<String?>(null) }
    // Unsaved edits win over a settings message from another device; after a save the server's answer shows.
    var edited by rememberSaveable(server) { mutableStateOf(false) }
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
    val a = remember { app.shown.value } // the server this screen was drawn for; a job queued before a switch still writes to it (#78)
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
                a.soundNow.value = name
                app.io.execute { why = a.pickSound(name); saves++ }
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

/** Display, Appearance (#583): System, Light or Dark, applied at once. */
@Composable
internal fun AppearancePicker(app: App) {
    val b = LocalBrand.current
    val look by app.appearance.collectAsState()
    listOf("system" to R.string.appearance_system, "light" to R.string.appearance_light, "dark" to R.string.appearance_dark).forEach { (v, label) ->
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth().selectable(look == v, role = Role.RadioButton) { app.pickAppearance(v) }) {
            RadioButton(selected = look == v, onClick = null)
            Text(stringResource(label), color = b.ink, modifier = Modifier.padding(start = 12.dp))
        }
    }
}

/** Settings in the sections of docs/SPEC.md section 9.1, with search at the top (#575). */
@Composable
internal fun SettingsScreen(app: App, battery: Intent?, resumes: Int, scan: () -> Unit) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    val saved by app.saved.collectAsState() // a linked phone's new server and token show here at once
    val a = remember { app.shown.value } // the server this screen was drawn for; a job queued before a switch still writes to it (#78)
    var muted by remember { mutableStateOf(app.muted) }
    var stack by remember { mutableStateOf(app.stack) }
    var drop by remember { mutableStateOf(app.dropCleared) }
    var fast by remember { mutableStateOf(app.fastSwipe) }
    var shade by remember { mutableStateOf(app.shadeClears) }
    var query by rememberSaveable { mutableStateOf("") }
    val topics by app.db.dao().topics().collectAsState(emptyList())
    val status by app.status.collectAsState()
    // Only a full admin may list senders; for any other token this stays null and the setting is left out.
    val senders = remember(saved) { mutableStateOf<List<org.json.JSONObject>?>(null) }
    // Snooze times and linked devices need a token the server takes for reads; a rejected or publish-only one hides them (#604).
    var reads by remember(saved) { mutableStateOf(false) }
    val rejected = status == SyncClient.Status.AUTH_FAILED
    LaunchedEffect(saved, resumes, rejected) {
        if (rejected) { senders.value = null; reads = false } // nothing fetched with the old answer stays on screen
        if (rejected || !app.configured) return@LaunchedEffect
        val api = app.api()
        app.io.execute {
            runCatching { api.get("/api/v1/settings") }.onSuccess { reads = true }.onFailure { if ((it as? HttpError)?.isAuth == true) reads = false }
            senders.value = runCatching { objects(org.json.JSONObject(api.get("/api/v1/senders")), "senders") }.getOrNull()
        }
    }
    // A live stream needs scope read too, so it proves the same after an offline start; a late probe answer never beats a rejection.
    val canRead = !rejected && (reads || status == SyncClient.Status.CONNECTED)
    val subs by app.subs.collectAsState()
    var subsGone by remember(saved) { mutableStateOf(false) }
    val open = mapOf(
        "subscriptions" to (app.configured && subs != null && !subsGone),
        "battery" to (battery != null && app.configured), "muted-topics" to topics.isNotEmpty(),
        "swipe" to app.configured, "formatting" to app.configured, "snooze" to (app.configured && canRead), "devices" to (app.configured && (canRead || rejected)), // rejected: a publish-only phone may still unlink itself (#643)
        "senders" to (app.configured && !rejected && senders.value != null),
    )
    val shown = SETTINGS.filter {
        open[it.id] != false && settingMatches(query, ctx.getString(it.label), it.desc?.let(ctx::getString), it.keywords)
    }

    @Composable fun Title(label: Int) = Text(stringResource(label), color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
    @Composable fun Explain(text: Int) = Text(stringResource(text), color = b.slate, fontSize = 13.sp)
    @Composable fun Toggle(label: Int, explain: Int, on: Boolean, set: (Boolean) -> Unit) =
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.weight(1f)) { Title(label); Explain(explain) }
            Switch(checked = on, onCheckedChange = set)
        }

    /** The address and token form: the shown server's, or [adding] another one through [App.saveTo]. */
    @Composable fun ServerForm(adding: Boolean) {
        var url by rememberSaveable(saved) { mutableStateOf(if (adding) "https://" else app.serverUrl.ifEmpty { "https://" }) }
        var token by rememberSaveable(saved) { mutableStateOf(if (adding) "" else app.token) }
        var result by remember { mutableStateOf<String?>(null) }
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
                    // One slot per address: another try at a server this phone has would take its token and wipe its
                    // notifications. Checked here, in turn, so a second quick tap sees the server the first one added.
                    // An edit may not take another server's address either (#860): a pairing link for it would find the wrong slot.
                    if (app.accountAt(newUrl, if (adding) -1 else a.id) != null) { result = ctx.getString(R.string.server_already, newUrl); return@execute }
                    // Another server is tried first and kept only once it answers, so a typo adds nothing (#78).
                    if (!adding) a.saveServer(newUrl, newToken)
                    // /api/v1/topics needs the read scope, so this checks the URL and the token together.
                    result = try {
                        (if (adding) Api(app.http, newUrl, newToken) else a.api()).get("/api/v1/topics")
                        if (adding) app.saveTo(newUrl, newToken) // shows it, so this screen is drawn again for it
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
        result?.let { Text(it, color = b.body, fontSize = 14.sp) }
    }

    @Composable fun Block(id: String) {
        when (id) {
            "server" -> {
                Title(R.string.server)
                Explain(R.string.server_explain)
                ServerForm(false)
            }
            "servers" -> {
                Title(R.string.servers)
                Explain(R.string.servers_explain)
                ServerList(app, saved)
                var adding by rememberSaveable { mutableStateOf(false) }
                if (adding) {
                    ServerForm(true)
                    TextButton(onClick = scan) { Text(stringResource(R.string.scan_qr), color = b.ink) } // a new address in a code is added (#78)
                } else TextButton(onClick = { adding = true }) { Text(stringResource(R.string.server_add), color = b.ink) }
            }
            "link-phone" -> {
                Title(R.string.link_phone)
                Explain(R.string.link_phone_explain)
                TextButton(onClick = scan) { Text(stringResource(R.string.scan_qr), color = b.ink) }
            }
            "client-cert" -> {
                Title(R.string.client_cert)
                Explain(R.string.client_cert_explain)
                val alias by a.clientCert.collectAsState() // the server this screen was opened for (#78)
                Text(alias ?: stringResource(R.string.client_cert_none), color = b.body, fontSize = 14.sp)
                Row {
                    TextButton(onClick = {
                        val host = a.serverUrl.toHttpUrlOrNull()
                        val at = a.certOrigin() // the address the picker shows; a pick that returns after a move is dropped
                        // Android's own picker: the key stays in the key store, clrd only learns its name.
                        (ctx as? android.app.Activity)?.let { android.security.KeyChain.choosePrivateKeyAlias(it, { pick -> if (pick != null) a.pickClientCert(pick, at) }, null, null, host?.host, host?.port ?: -1, alias) }
                    }) { Text(stringResource(R.string.client_cert_pick), color = b.ink) }
                    if (alias != null) TextButton(onClick = { a.pickClientCert(null) }) { Text(stringResource(R.string.client_cert_remove), color = b.ink) }
                }
            }
            "battery" -> battery?.let { prompt ->
                Title(R.string.battery)
                Explain(R.string.battery_explain)
                TextButton(onClick = { openBattery(ctx, prompt) }) { Text(stringResource(R.string.battery_button), color = b.ink) }
            }
            "subscriptions" -> {
                Title(R.string.subs_title)
                Explain(R.string.subs_explain)
                SubscriptionSettings(app, resumes) { subsGone = true }
            }
            "muted-topics" -> {
                Title(R.string.topics)
                Explain(R.string.topics_explain)
                topics.forEach { t ->
                    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                        Text(t, color = b.ink, modifier = Modifier.weight(1f))
                        Text(stringResource(if (t in muted) R.string.topic_muted else R.string.topic_on), style = Mono, color = b.slate)
                        Spacer(Modifier.width(8.dp))
                        Switch(checked = t !in muted, onCheckedChange = { on ->
                            muted = if (on) muted - t else muted + t
                            a.muted = muted
                            app.io.execute { RoomStore(app, a).reconcile() }
                        })
                    }
                }
            }
            "stack" -> Toggle(R.string.stack, R.string.stack_explain, stack) { on ->
                stack = on
                app.stack = on
                app.redrawTrays() // reposts every row in or out of its stack, on every server
            }
            "drop-cleared" -> Toggle(R.string.drop_cleared, R.string.drop_cleared_explain, drop) { on ->
                drop = on
                app.dropCleared = on
                app.io.execute { a.pruneCleared() }
            }
            "appearance" -> {
                Title(R.string.appearance)
                Explain(R.string.appearance_explain)
                AppearancePicker(app)
            }
            "shade-clears" -> Toggle(R.string.shade_clears, R.string.shade_clears_explain, shade) { on -> shade = on; app.shadeClears = on }
            "full-swipe" -> Toggle(R.string.fast_swipe, R.string.fast_swipe_explain, fast) { on -> fast = on; app.fastSwipe = on }
            "swipe" -> SwipeSettings(app)
            "formatting" -> DisplaySettings(app)
            "sound" -> SoundSettings(app, resumes)
            "snooze" -> SnoozeSettings(app)
            "devices" -> DeviceSettings(app, resumes)
            "senders" -> SenderSettings(app, resumes, senders)
            "about" -> AboutBlock(app, a) { Title(R.string.about_title) }
        }
    }

    val keep = androidx.compose.runtime.saveable.rememberSaveableStateHolder() // a setting hidden by search keeps its unsaved edits
    val scroll = rememberScrollState()
    // Scroll up so the signed-out line is seen even if Settings was scrolled down.
    LaunchedEffect(status) { if (status == SyncClient.Status.AUTH_FAILED) scroll.animateScrollTo(0) }
    Column(Modifier.verticalScroll(scroll).padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        // Unlinked from another device, revoked, or mistyped: this is where the phone signs in again (#264).
        if (app.configured && status == SyncClient.Status.AUTH_FAILED) Text(stringResource(R.string.signed_out), color = b.signal, fontSize = 14.sp)
        OutlinedTextField(
            query, { query = it }, label = { Text(stringResource(R.string.settings_search)) }, singleLine = true,
            modifier = Modifier.fillMaxWidth().testTag("settings-search"),
        )
        if (shown.isEmpty()) {
            Text(stringResource(R.string.settings_none, query.trim()), color = b.slate, fontSize = 14.sp)
            TextButton(onClick = { query = "" }) { Text(stringResource(R.string.settings_clear), color = b.ink) }
        }
        shown.groupBy { it.section }.forEach { (section, items) ->
            Spacer(Modifier.height(8.dp))
            Text(stringResource(section).uppercase(), style = Mono, color = b.slate, fontSize = 13.sp)
            items.forEach {
                Column(Modifier.fillMaxWidth().testTag("setting-${it.id}"), verticalArrangement = Arrangement.spacedBy(12.dp)) { keep.SaveableStateProvider(it.id) { Block(it.id) } }
            }
        }
        Spacer(Modifier.height(8.dp))
        val uri = androidx.compose.ui.platform.LocalUriHandler.current
        TextButton(onClick = { runCatching { uri.openUri(REPO) } }) { Text(stringResource(R.string.source_code), color = b.ink) }
        TextButton(onClick = { runCatching { uri.openUri("$REPO/issues") } }) { Text(stringResource(R.string.report_problem), color = b.ink) }
        Text(stringResource(R.string.about, BuildConfig.VERSION_NAME), style = Mono, color = b.slate)
    }
}

internal const val REPO = "https://github.com/thejdubb02/clrd"

/**
 * The server a tray tap opens the app on (#78). [Tray]'s open intent has no action and CLEAR_TOP; account 0's
 * has no data, as before #78, so it shows account 0, and another server's names itself in [Tray.OPEN] data.
 * The launcher (ACTION_MAIN) and the sync notice (no CLEAR_TOP) leave the shown server as it is.
 */
internal fun openedAccount(i: Intent): Int? {
    if (i.action != null || (i.flags and Intent.FLAG_ACTIVITY_CLEAR_TOP) == 0) return null
    val d = i.data ?: return 0
    return if (d.toString().startsWith(Tray.OPEN)) d.getQueryParameter(Tray.QUERY_ACCOUNT)?.toIntOrNull() ?: 0 else null
}
