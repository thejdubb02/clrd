package me.itsjustin.clrd

import android.Manifest
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
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
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.OutlinedTextField
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
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch
import java.text.DateFormat
import java.util.Date

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        if (Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        val app = App.of(this)
        if (app.configured) SyncService.start(this)
        setContent { ClrdTheme { Home(app) } }
    }
}

private enum class Tab(val label: String, val icon: ImageVector) {
    Inbox("Inbox", Icons.Filled.Notifications),
    Snoozed("Snoozed", Icons.Filled.DateRange),
    Cleared("Cleared", Icons.Filled.CheckCircle),
    Settings("Settings", Icons.Filled.Settings),
}

@Composable
private fun Home(app: App) {
    val b = LocalBrand.current
    var tab by rememberSaveable { mutableStateOf(if (app.configured) Tab.Inbox else Tab.Settings) }
    var topic by rememberSaveable { mutableStateOf<String?>(null) }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    val dao = app.db.dao()

    // Clear with an Undo toast, as on the web inbox.
    val clear: (Notif) -> Unit = { n ->
        app.act(n.id, "dismiss")
        scope.launch {
            if (snackbar.showSnackbar("Cleared everywhere", "Undo", duration = androidx.compose.material3.SnackbarDuration.Short) == SnackbarResult.ActionPerformed) {
                app.act(n.id, "reopen")
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
                        icon = { Icon(t.icon, null) }, label = { Text(t.label) },
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
                Tab.Settings -> SettingsScreen(app)
                else -> {
                    val flow = when (tab) { Tab.Inbox -> dao.inbox(); Tab.Snoozed -> dao.snoozed(); else -> dao.cleared() }
                    val rows by remember(tab) { flow }.collectAsState(emptyList())
                    val topics = rows.map { it.topic }.distinct().sorted()
                    val picked = topic?.takeIf { it in topics }
                    if (topics.size > 1) Pills(topics, picked) { topic = it }
                    val shown = rows.filter { picked == null || it.topic == picked }
                    if (shown.isEmpty()) Empty(tab) else LazyColumn(
                        contentPadding = PaddingValues(16.dp, 8.dp, 16.dp, 16.dp),
                        verticalArrangement = Arrangement.spacedBy(10.dp),
                    ) {
                        items(shown, key = { it.id }) { n ->
                            if (tab == Tab.Cleared) Card(n) { TextButton(onClick = { app.act(n.id, "reopen") }) { Text("Restore", color = b.ink) } }
                            else Swipeable(n, onClear = { clear(n) }) {
                                Card(n) {
                                    if (n.state == Notif.ACTIVE) TextButton(onClick = { app.act(n.id, "ack") }) { Text("Ack", color = b.ink) }
                                    if (tab == Tab.Inbox) TextButton(onClick = { app.act(n.id, "snooze", """{"for":"1h"}""") }) { Text("Snooze 1h", color = b.ink) }
                                    TextButton(onClick = { clear(n) }) { Text("Clear", color = b.clear, fontWeight = FontWeight.SemiBold) }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun Header(app: App) {
    val b = LocalBrand.current
    val status by app.status.collectAsState()
    val (dot, label) = when {
        !app.configured -> b.slate to "Not set up"
        status == SyncClient.Status.CONNECTED -> b.clear to "In sync"
        status == SyncClient.Status.CONNECTING -> b.snooze to "Connecting"
        status == SyncClient.Status.AUTH_FAILED -> b.signal to "Token rejected"
        else -> b.slate to "Offline"
    }
    Row(Modifier.fillMaxWidth().padding(20.dp, 12.dp, 20.dp, 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Text("clrd", style = Wordmark, color = b.ink)
        Spacer(Modifier.weight(1f))
        Box(Modifier.size(8.dp).clip(CircleShape).background(dot))
        Spacer(Modifier.width(6.dp))
        Text(label, style = Mono, color = b.slate)
    }
}

@Composable
private fun Pills(topics: List<String>, selected: String?, onPick: (String?) -> Unit) {
    val b = LocalBrand.current
    LazyRow(contentPadding = PaddingValues(horizontal = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        items(listOf<String?>(null) + topics) { t ->
            val on = t == selected
            Text(
                t ?: "All",
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
                Text("Clear", color = b.paper, fontWeight = FontWeight.SemiBold)
            }
        },
        content = { content() },
    )
}

@Composable
private fun Card(n: Notif, actions: @Composable () -> Unit) {
    val b = LocalBrand.current
    Surface(
        color = b.surface, shape = RoundedCornerShape(16.dp), border = BorderStroke(1.dp, b.line),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(Modifier.padding(start = 16.dp, top = 14.dp, end = 8.dp, bottom = 4.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(end = 8.dp)) {
                // Orange only when something needs you.
                val dot = when { n.isFinal -> b.clear; n.priority >= 4 -> b.signal; n.priority == 3 -> b.slate; else -> b.line }
                Box(Modifier.size(8.dp).clip(CircleShape).background(dot))
                Spacer(Modifier.width(10.dp))
                Text(
                    n.title.ifEmpty { n.topic }, color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold,
                    maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f),
                )
                if (n.state != Notif.ACTIVE) StateChip(n.state)
            }
            if (n.message.isNotEmpty()) {
                Spacer(Modifier.height(4.dp))
                Text(n.message, color = b.body, fontSize = 15.sp, maxLines = 6, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(end = 8.dp))
            }
            Spacer(Modifier.height(8.dp))
            Text(meta(n), style = Mono, color = b.slate, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Row(horizontalArrangement = Arrangement.End, modifier = Modifier.fillMaxWidth()) { actions() }
        }
    }
}

@Composable
private fun StateChip(state: String) {
    val b = LocalBrand.current
    val (bg, fg) = chip(state)
    Text(
        state, style = Mono, color = fg,
        modifier = Modifier.clip(RoundedCornerShape(50)).background(bg)
            .border(1.dp, if (bg == Color.Transparent) b.line else bg, RoundedCornerShape(50))
            .padding(horizontal = 8.dp, vertical = 2.dp),
    )
}

private fun meta(n: Notif): String {
    val parts = mutableListOf(n.topic, ago(n.createdAt))
    when (n.state) {
        Notif.ACKNOWLEDGED -> parts += "acked on ${n.stateBy ?: "?"}"
        Notif.SNOOZED -> n.snoozeUntil?.let { parts += "until " + DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(it)) }
        Notif.DISMISSED -> parts += "cleared on ${n.stateBy ?: "?"}"
        Notif.RESOLVED -> parts += "resolved"
    }
    return parts.joinToString("  ·  ")
}

private fun ago(t: Long): String {
    val m = (System.currentTimeMillis() - t) / 60_000
    return when { m < 1 -> "now"; m < 60 -> "${m}m"; m < 1440 -> "${m / 60}h"; else -> "${m / 1440}d" }
}

@Composable
private fun Empty(tab: Tab) {
    val b = LocalBrand.current
    Column(Modifier.fillMaxSize(), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.Center) {
        Icon(Icons.Filled.CheckCircle, null, tint = b.clear, modifier = Modifier.size(48.dp))
        Spacer(Modifier.height(12.dp))
        Text(
            when (tab) { Tab.Inbox -> "Nothing needs you."; Tab.Snoozed -> "Nothing snoozed."; else -> "Nothing cleared yet." },
            color = b.slate, fontSize = 15.sp,
        )
    }
}

@Composable
private fun SettingsScreen(app: App) {
    val b = LocalBrand.current
    val ctx = LocalContext.current
    var url by rememberSaveable { mutableStateOf(app.serverUrl.ifEmpty { "https://" }) }
    var token by rememberSaveable { mutableStateOf(app.token) }
    var result by remember { mutableStateOf<String?>(null) }
    var muted by remember { mutableStateOf(app.muted) }
    val topics by app.db.dao().topics().collectAsState(emptyList())

    Column(Modifier.verticalScroll(rememberScrollState()).padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text("Server", color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
        OutlinedTextField(
            url, { url = it }, label = { Text("Server URL") }, singleLine = true, modifier = Modifier.fillMaxWidth(),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
        )
        if (url.trim().startsWith("http://", ignoreCase = true)) Text("Plain http sends your token unencrypted. Use https outside your home network.", color = b.signal, fontSize = 13.sp)
        OutlinedTextField(
            token, { token = it }, label = { Text("Token") }, singleLine = true, modifier = Modifier.fillMaxWidth(),
            visualTransformation = PasswordVisualTransformation(),
        )
        Button(
            onClick = {
                result = "Testing"
                val newUrl = url.trim().trimEnd('/')
                val newToken = token.trim()
                app.io.execute {
                    if (newUrl != app.serverUrl || newToken != app.token) {
                        // Old server's rows, cursor and queue must not reach the new one. Blanking the token
                        // fences the old client's writes (RoomStore.ifMine) and keeps the service from starting
                        // one; a crash before the final save leaves the app unconfigured, never mixed.
                        synchronized(app.configLock) { app.token = "" }
                        app.sync?.stop()
                        app.sync = null
                        synchronized(app.configLock) {
                            app.db.dao().forget()
                            app.tray.reconcile(emptyList(), app.muted)
                            app.serverUrl = newUrl
                            app.token = newToken
                        }
                    } else {
                        app.serverUrl = newUrl
                        app.token = newToken
                    }
                    // /api/v1/topics needs the read scope, so this checks the URL and the token together.
                    result = try {
                        app.api().get("/api/v1/topics")
                        SyncService.start(ctx, restart = true)
                        "Connected. Syncing now."
                    } catch (e: HttpError) {
                        if (e.isAuth) "Server reached, but it rejected the token." else "Server answered ${e.code}."
                    } catch (e: Exception) {
                        "Could not reach the server: ${e.message}"
                    }
                }
            },
            colors = ButtonDefaults.buttonColors(containerColor = b.ink, contentColor = b.paper),
        ) { Text("Save and test connection") }
        result?.let { Text(it, color = b.body, fontSize = 14.sp) }

        if (topics.isNotEmpty()) {
            Spacer(Modifier.height(8.dp))
            Text("Topics", color = b.ink, fontSize = 17.sp, fontWeight = FontWeight.SemiBold)
            Text("A muted topic stays in the inbox but never pops up on this phone.", color = b.slate, fontSize = 13.sp)
            topics.forEach { t ->
                Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                    Text(t, color = b.ink, modifier = Modifier.weight(1f))
                    Text(if (t in muted) "muted" else "on", style = Mono, color = b.slate)
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
        Text("clrd ${BuildConfig.VERSION_NAME}. No tracking, no Google services.", style = Mono, color = b.slate)
    }
}
