package me.itsjustin.clrd

import android.app.Application
import android.content.Context
import androidx.room.Room
import kotlinx.coroutines.flow.MutableStateFlow
import okhttp3.OkHttpClient
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** App-wide singletons. Small enough that a DI framework would be the bigger thing. */
class App : Application() {
    val db by lazy { Room.databaseBuilder(this, Db::class.java, "clrd.db").build() }
    val http by lazy {
        OkHttpClient.Builder()
            .pingInterval(45, TimeUnit.SECONDS) // notices a dead connection the server ping alone would not
            .connectTimeout(15, TimeUnit.SECONDS)
            .build()
    }
    val tray by lazy { Tray(this) }
    val images by lazy { Images(this) }
    val prefs by lazy { getSharedPreferences("clrd", Context.MODE_PRIVATE) }
    val io = Executors.newSingleThreadExecutor()
    /** User actions only, and they never wait on the network, so a tray tap is saved within its goAsync() deadline. */
    val acts = Executors.newSingleThreadExecutor()
    /** Typed answers the server refused, by notification id, so the reply box opens with them again (#153). */
    val drafts = java.util.concurrent.ConcurrentHashMap<String, String>()
    /** Typed answers refused because the question changed (409), counted by notification id, so a reply box open on it closes (#516). */
    val changed = androidx.compose.runtime.mutableStateMapOf<String, Int>()
    /** The Send box's send in flight, kept here so a rotation does not forget it (#523). */
    val sender by lazy { Sender { io.execute(it) } }
    /** Sends for [act] while no sync service runs. One at a time, so a hung send queues the next instead of piling up threads. */
    private val sends = Executors.newSingleThreadExecutor()
    private var sendNotBefore = 0L // System.nanoTime(); a busy server's Retry-After, touched only on [sends]
    private var sendNotBeforeGen = 0 // the settings it came from; another server is not busy

    val status = MutableStateFlow(SyncClient.Status.OFFLINE)

    /** Live sync client while the service runs; null otherwise. */
    @Volatile var sync: SyncClient? = null

    /**
     * Held for every change that writes Room and then the tray (sync, user actions, reconcile) and while
     * Settings switches server. Without it a slower writer can put back a tray item a faster one just cleared.
     */
    val configLock = Any()

    /** Bumped on every server or token switch. A store made before the bump is fenced off. */
    @Volatile var configGen = 0

    var serverUrl: String
        get() = prefs.getString("url", "").orEmpty()
        set(v) = prefs.edit().putString("url", v.trim().trimEnd('/')).apply()
    var token: String
        get() = prefs.getString("token", "").orEmpty()
        set(v) = prefs.edit().putString("token", v.trim()).apply()
    var muted: Set<String>
        get() = prefs.getStringSet("muted", emptySet()).orEmpty()
        set(v) = prefs.edit().putStringSet("muted", v).apply()

    /**
     * Stack by topic (#108), in the tray and the app's list. Off until a person turns it on (#465), as on the web;
     * a phone that never touched it reads the default, so it moves to off, and a stored choice is kept.
     */
    var stack: Boolean
        get() = prefs.getBoolean("stack", false)
        set(v) = prefs.edit().putBoolean("stack", v).apply()

    /** Remove cleared items from this phone as soon as they are cleared (#151). Off by default; the server keeps its own copy either way. */
    var dropCleared: Boolean
        get() = prefs.getBoolean("drop_cleared", false)
        set(v) = prefs.edit().putBoolean("drop_cleared", v).apply()

    /** This phone's notification sound (docs/SPEC.md section 2.2). Unset means the phone's own default. Change it with [saveSound]. */
    val sound: String get() = Sounds.known(prefs.getString("sound", null))

    /**
     * Switches the sound: new channels first, then the choice, then the old channels go and the tray is
     * reposted quietly onto the new ones (a post to a missing channel is dropped). Call on [io].
     * True when the change took the Urgent channel's Do Not Disturb pass with it.
     */
    fun saveSound(name: String): Boolean = synchronized(configLock) {
        val s = Sounds.known(name)
        val dropped = tray.createChannels(s)
        prefs.edit().putString("sound", s).commit()
        soundNow.value = s
        RoomStore(this).reconcile()
        dropped
    }

    /** [sound] for Settings to follow, so a change from the server or an agent shows while it is open. */
    val soundNow by lazy { MutableStateFlow(sound) }

    /**
     * A choice made on this phone that the server does not have yet (refused, offline, or made before #289).
     * It wins over the server's until the server takes it, the same as the web inbox.
     */
    val soundPending: String? get() = prefs.getString("sound_pending", null)

    /** The person picked [name] in Settings: play it here at once, then tell the server. Call on [io]. Returns [pushSound]'s answer. */
    fun pickSound(name: String): String? {
        prefs.edit().putString("sound_pending", Sounds.known(name)).commit() // first, so a crash after it cannot let the server's old choice win
        saveSound(name)
        return pushSound()
    }

    /**
     * Sends [soundPending] to the server (docs/SPEC.md section 2.2), one at a time on [io].
     * Null when the server has it (or nothing waits), else why it stays on this phone only.
     */
    fun pushSound(): String? {
        val s = soundPending ?: return null
        if (!configured) return getString(R.string.sound_phone_only, getString(R.string.sound_no_server))
        return try {
            api().put("/api/v1/settings", JSONObject().put("sound", s).toString())
            if (soundPending == s) prefs.edit().remove("sound_pending").commit()
            null
        } catch (e: Exception) { // a bad saved address throws IllegalArgumentException, not IOException
            getString(R.string.sound_phone_only, (e as? HttpError)?.message ?: getString(R.string.sound_no_connection))
        }
    }

    /**
     * The server's sound for this phone, from a settings message. Applied on the sync thread before the
     * next event, so a notification sent right after the change plays the new sound. A pending choice is
     * sent instead of being overwritten.
     */
    private fun serverSound(name: String) {
        if (soundPending != null) io.execute { pushSound() }
        else if (Sounds.known(name) != sound && saveSound(name)) tray.dndNotice() // not picked here, so Settings' note may go unseen (#380)
    }

    /**
     * The server database the saved seq belongs to. Saved after the rows, not with them: a crash
     * in between only means one more reset on the next connect.
     */
    var epoch: String
        get() = prefs.getString("epoch", "").orEmpty()
        set(v) = prefs.edit().putString("epoch", v).apply()

    /** The shared snooze times, as the server last sent them (every connect sends them). */
    val snooze by lazy { MutableStateFlow(prefs.getString("snooze", null)?.let { runCatching { Snooze.from(org.json.JSONObject(it)) }.getOrNull() } ?: Snooze()) }

    /** The `by_id` this phone's marks get on the current server, "" until a `ready` says (#451). */
    val markId by lazy { MutableStateFlow(prefs.getString("mark_id", "").orEmpty()) }

    /**
     * Sets this phone's mark on a card, or removes it when [kind] is null (#451). Sent now, not queued: a mark
     * changes no state, and the person sees at once why one was refused. [done] gets null or the reason, on the main thread.
     */
    fun mark(id: String, kind: String?, note: String, link: String, done: (String?) -> Unit) {
        val (store, api) = synchronized(configLock) { RoomStore(this) to api() }
        io.execute {
            val err = try {
                val path = "/api/v1/notifications/$id/mark"
                val out = if (kind == null) api.delete(path)
                else api.post(path, JSONObject().put("mark", kind).put("note", note).put("link", link).toString())
                store.apply(listOf(Notif.fromJson(JSONObject(out))), 0, quiet = true) // the card shows it now, the stream's copy changes nothing
                null
            } catch (e: Exception) { // a bad saved address throws IllegalArgumentException, not IOException
                (e as? HttpError)?.message ?: getString(R.string.mark_no_connection)
            }
            android.os.Handler(mainLooper).post { done(err) }
        }
    }

    /** Keeps the snooze part of a server `settings` message. */
    fun saveSettings(o: org.json.JSONObject) {
        if (o.has("sound")) serverSound(o.optString("sound"))
        if (o.has("keep_cleared_days")) { // the server's keep for cleared rows (#261): drop what it deleted
            val keep = org.json.JSONObject().put("days", o.optInt("keep_cleared_days", KEEP_DAYS))
                .put("topics", o.optJSONObject("topic_keep_cleared_days") ?: org.json.JSONObject())
            prefs.edit().putString("keep", keep.toString()).apply()
            io.execute { pruneCleared() }
        }
        val s = Snooze.from(o) ?: return
        prefs.edit().putString("snooze", s.json()).apply()
        snooze.value = s
    }

    /**
     * Drops cleared rows older than the server keeps them, and never more than [KEEP_DAYS] on this phone.
     * A topic's own keep wins over the global one; 0 means the server keeps them forever.
     */
    fun pruneCleared(now: Long = System.currentTimeMillis()) {
        val k = runCatching { org.json.JSONObject(prefs.getString("keep", null) ?: "{}") }.getOrDefault(org.json.JSONObject())
        fun before(days: Int) = now - (if (days in 1..KEEP_DAYS) days else KEEP_DAYS) * 24 * 3600_000L
        val topics = k.optJSONObject("topics") ?: org.json.JSONObject()
        val own = topics.keys().asSequence().toList()
        val dao = db.dao()
        if (dropCleared) return dao.dropCleared()
        dao.prune(before(k.optInt("days", KEEP_DAYS)), own)
        own.forEach { dao.pruneTopic(it, before(topics.optInt(it, KEEP_DAYS))) }
    }

    val configured get() = serverUrl.isNotEmpty() && token.isNotEmpty()

    fun api() = Api(http, serverUrl, token)

    /** Bumped on every [saveServer], so Settings shows a server and token saved from elsewhere (a scanned link). */
    val saved = MutableStateFlow(0)

    /** Bumped when the stream sends the device list again, so Settings reads it fresh (#345). */
    val devicesChanged = MutableStateFlow(0)

    /** Saves the server and token from Settings or a scanned link. Call on [io]. */
    fun saveServer(newUrl: String, newToken: String) {
        if (newUrl != serverUrl || newToken != token) {
            // Old server's rows, cursor and queue must not reach the new one. Blanking the token
            // and bumping configGen fence the old client (RoomStore.ifMine) and keeps the service from starting
            // one; a crash before the final save leaves the app unconfigured, never mixed.
            synchronized(configLock) { configGen++; token = "" }
            val old = sync
            sync = null
            old?.stop()
            synchronized(configLock) {
                db.dao().forget()
                epoch = ""
                // The old server's snooze times are not the new one's; defaults until its settings message arrives.
                prefs.edit().remove("snooze").remove("sound_pending").remove("keep").remove("mark_id").apply() // a choice the old server refused is not the new one's
                snooze.value = Snooze()
                markId.value = ""
                tray.reconcile(emptyList(), muted)
                serverUrl = newUrl
                token = newToken
            }
        } else {
            serverUrl = newUrl
            token = newToken
        }
        saved.value++
    }

    override fun onCreate() {
        super.onCreate()
        tray.createChannels()
        moveSound()
    }

    /** A sound picked before the server kept it (#282) goes to the server once, on the next connect. */
    internal fun moveSound() {
        if (!prefs.getBoolean("sound_moved", false)) {
            prefs.edit().apply { if (sound != Sounds.DEFAULT) putString("sound_pending", sound) }.putBoolean("sound_moved", true).apply()
        }
    }

    /**
     * A user action from the tray or the inbox. Saved first, shown locally at once, then sent.
     * If sending fails it stays queued and goes out before the next WebSocket connect.
     * [server] is the server the tray item came from (null = the inbox, which always shows the current one).
     * An action for a server that is no longer configured is dropped, never sent to the new one, and so is
     * one that waited across a Settings switch.
     * [via] says how the person did it (docs/SPEC.md "via"), so the server log can answer "I did not clear that".
     */
    fun act(id: String, action: String, body: String = "{}", server: String? = null, via: String = VIA_APP, done: () -> Unit = {}) =
        actAll(listOf(id), action, body, server, via, done)

    /** One action on several rows from the inbox, such as a stack's Clear (#360): the dismisses go out as one clear call. */
    fun act(ids: List<String>, action: String, via: String = VIA_APP) = actAll(ids, action, "{}", null, via) {}

    /** Undo of a Clear (#425): rows come back as they were, so a snoozed one is snoozed again until its old time. */
    fun undoClear(rows: List<Notif>) {
        val now = System.currentTimeMillis()
        val (snoozed, rest) = rows.partition { it.state == Notif.SNOOZED && (it.snoozeUntil ?: 0) > now }
        if (rest.isNotEmpty()) act(rest.map { it.id }, "reopen")
        snoozed.forEach {
            val body = JSONObject().put("until", java.time.Instant.ofEpochMilli(it.snoozeUntil!!).toString()).toString()
            act(it.id, "reopen", body) // #473: one change, never active in between. No follow-up snooze (#508): it could undo another device's ack
        }
    }

    /**
     * Rows swiped out of the shade (#265). A swipe, a stack swipe or Clear all only hides them on this
     * phone; clearing happens in the app, the web or the API. A new alert for one shows it again.
     */
    var trayHidden: Set<String>
        get() = prefs.getStringSet("tray_hidden", emptySet()).orEmpty()
        set(v) = prefs.edit().putStringSet("tray_hidden", v).apply()

    private fun actAll(ids: List<String>, action: String, rawBody: String, server: String?, via: String, done: () -> Unit) {
        val json = JSONObject(rawBody)
        val until = if (action == "snooze" || action == "reopen") snoozeEnd(json) else null
        // #508: a snooze sent late (offline) ends when this phone and its Undo say; see snoozeWire
        if (action == "snooze" && until != null) json.put(ENDS, until)
        val body = json.put("via", via).toString()
        val gen = configGen // read now, not when the save runs
        acts.execute {
            try {
                val local = when (action) {
                    "dismiss" -> Notif.DISMISSED
                    "snooze" -> Notif.SNOOZED
                    "ack", "reply" -> Notif.ACKNOWLEDGED
                    "reopen" -> if (until != null) Notif.SNOOZED else Notif.ACTIVE
                    else -> null
                }
                synchronized(configLock) {
                    if (gen != configGen || (server != null && server != serverUrl)) return@execute
                    val dao = db.dao()
                    // One transaction: a stack swipe is saved whole or not at all, never half.
                    val saved = db.runInTransaction<List<String>> {
                        // A stack swipe may also fire each row's own delete intent: those rows are cleared already.
                        ids.filterNot { action == "dismiss" && dao.get(it)?.state == Notif.DISMISSED }.onEach { id ->
                            dao.addPending(Pending(notifId = id, action = action, body = body))
                            if (until != null && local == Notif.SNOOZED) dao.snooze(id, until, action == "reopen")
                            else if (local != null) dao.setState(id, local)
                        }
                    }
                    if (local == Notif.DISMISSED || local == Notif.SNOOZED) saved.forEach { tray.apply(TrayStep.Cancel(it)) }
                }
                val s = sync
                if (s != null) s.flushSoon() else sendAlone()
            } finally {
                done()
            }
        }
    }

    private fun sendAlone() {
        // One snapshot of server, token and store, so a switch never sends one server's queue to another.
        val (gen, store, api) = synchronized(configLock) {
            if (!configured) return
            Triple(configGen, RoomStore(this), api())
        }
        sends.execute {
            if (gen == sendNotBeforeGen && System.nanoTime() < sendNotBefore) return@execute // stays queued for later
            try {
                flushPending(store, api) { configGen == gen }
            } catch (e: Exception) {
                (e as? HttpError)?.retryAfterMs?.let { sendNotBefore = System.nanoTime() + it * 1_000_000; sendNotBeforeGen = gen }
            }
        }
    }

    companion object {
        fun of(ctx: Context) = ctx.applicationContext as App

        // How an action was made, sent as "via" (docs/SPEC.md).
        const val VIA_APP = "app"
        const val VIA_TRAY_BUTTON = "tray_button"
        const val VIA_APP_BULK = "app_bulk"
        /** The most days of cleared history this phone keeps, whatever the server keeps. */
        const val KEEP_DAYS = 7
    }
}

/** [Store] backed by Room, driving the tray. */
class RoomStore(private val app: App) : Store {
    private val dao = app.db.dao()

    /** The settings generation this store syncs for. After a switch in Settings its reads and writes are dropped. */
    private val gen = app.configGen
    private inline fun ifMine(f: () -> Unit) = synchronized(app.configLock) { if (gen == app.configGen) f() }

    override fun lastSeq() = dao.lastSeq() ?: 0

    override fun apply(notifs: Collection<Notif>, seq: Long, quiet: Boolean, again: Boolean) = ifMine {
        // Only rows Room kept reach the tray, so an older event can never undo a newer state there.
        val saved = dao.applyAll(notifs, seq)
        // #500: a live repeat Room dropped (our own mark's answer got in first) still rings the newer row, if that one shows unacked.
        // Only when the newer row differs by marks alone (#504): after a Clear and Undo it is a new state, and an Undo stays quiet.
        val kept = if (!again) saved else saved + notifs.filter { n -> saved.none { it.id == n.id } }.mapNotNull { n ->
            dao.get(n.id)?.takeIf { it.copy(marks = n.marks, seq = n.seq, updatedAt = n.updatedAt, silenced = n.silenced) == n }
        }
        val muted = app.muted
        val waiting = waiting()
        val acting = dao.pending().filter { it.action == "reopen" }.map { it.notifId }.toSet() // our own reopen's event may beat its answer here
        for (n in kept) {
            val step = if (n.id in waiting) TrayStep.Cancel(n.id) else TrayStep.plan(n, muted)
            app.tray.apply(
                when {
                    step !is TrayStep.Show -> step
                    quiet || n.id in acting -> step.copy(silent = true)
                    else -> step.copy(again = again && !step.silent)
                },
            )
        }
        app.pruneCleared()
    }

    override fun replaceAll(open: List<Notif>, seq: Long, epoch: String) = ifMine {
        val waiting = waiting()
        val sameDb = epoch.isEmpty() || app.epoch.isEmpty() || epoch == app.epoch
        app.tray.reconcile(dao.replaceAll(open, seq, sameDb).filter { it.id !in waiting }, app.muted)
        if (epoch.isNotEmpty()) app.epoch = epoch
    }

    override fun epoch() = synchronized(app.configLock) { if (gen == app.configGen) app.epoch else "" } // another server's epoch says nothing here
    override fun saveEpoch(epoch: String) = ifMine { app.epoch = epoch }
    override fun saveMarkId(id: String) = ifMine { if (app.markId.value != id) { app.prefs.edit().putString("mark_id", id).apply(); app.markId.value = id } }

    override fun pending() = synchronized(app.configLock) { if (gen == app.configGen) dao.pending() else emptyList() } // never send the new server's queue to the old one
    /**
     * The queued action no longer hides the row, so the tray shows what Room says (another device may have reopened it).
     * Quietly: it follows the person's own tap (a reopen from the inbox), and a row that alerted already did so once.
     */
    /** Says it plainly, whether the tap came from the app or the tray (#330). */
    override fun replyOutcome(p: Pending, outcome: String) = ifMine {
        val text = when (outcome) {
            "confirm" -> R.string.reply_sent_to_confirm
            "approve_denied" -> R.string.reply_approve_denied
            "own_request" -> R.string.reply_own_request
            "bad_request" -> R.string.reply_text_refused
            else -> R.string.reply_changed
        }
        // A refused typed answer (400, 403) is kept for the reply box (#153); a changed question drops it.
        val typed = runCatching { JSONObject(p.body).optString("text") }.getOrDefault("")
        if (outcome == "bad_request" || outcome == "approve_denied" || outcome == "own_request") { if (typed.isNotEmpty()) app.drafts[p.notifId] = typed }
        else app.drafts.remove(p.notifId)
        android.os.Handler(android.os.Looper.getMainLooper()).post {
            if (outcome == "conflict") app.changed[p.notifId] = (app.changed[p.notifId] ?: 0) + 1 // Compose state, so on the main thread
            android.widget.Toast.makeText(app, text, android.widget.Toast.LENGTH_LONG).show()
        }
    }

    override fun removePending(p: Pending) = ifMine {
        dao.removePending(p.key)
        val n = dao.get(p.notifId)
        if (n != null && n.id !in waiting()) app.tray.apply(TrayStep.plan(n, app.muted).let { if (it is TrayStep.Show) it.copy(silent = true) else it })
    }

    /** Under the lock, so a sync write cannot land between reading the rows and posting them. */
    fun reconcile() = ifMine {
        val waiting = waiting()
        app.tray.reconcile(dao.openNow().filter { it.id !in waiting }, app.muted)
    }

    /** A queued dismiss or snooze that has not reached the server yet wins over what the server last said. */
    private fun waiting() = dao.pending().filter { it.action == "dismiss" || it.action == "snooze" }.map { it.notifId }.toSet()
}
