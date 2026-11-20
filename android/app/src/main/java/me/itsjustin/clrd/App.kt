package me.itsjustin.clrd

import android.app.Application
import android.content.Context
import androidx.room.Room
import kotlinx.coroutines.flow.MutableStateFlow
import okhttp3.OkHttpClient
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

    /** Stack tray notifications by topic (#108). On by default: it is how Android groups anyway. */
    var stack: Boolean
        get() = prefs.getBoolean("stack", true)
        set(v) = prefs.edit().putBoolean("stack", v).apply()

    /**
     * The server database the saved seq belongs to. Saved after the rows, not with them: a crash
     * in between only means one more reset on the next connect.
     */
    var epoch: String
        get() = prefs.getString("epoch", "").orEmpty()
        set(v) = prefs.edit().putString("epoch", v).apply()

    /** The shared snooze times, as the server last sent them (every connect sends them). */
    val snooze by lazy { MutableStateFlow(prefs.getString("snooze", null)?.let { runCatching { Snooze.from(org.json.JSONObject(it)) }.getOrNull() } ?: Snooze()) }

    /** Keeps the snooze part of a server `settings` message. */
    fun saveSettings(o: org.json.JSONObject) {
        val s = Snooze.from(o) ?: return
        prefs.edit().putString("snooze", s.json()).apply()
        snooze.value = s
    }

    val configured get() = serverUrl.isNotEmpty() && token.isNotEmpty()

    fun api() = Api(http, serverUrl, token)

    /** Bumped on every [saveServer], so Settings shows a server and token saved from elsewhere (a scanned link). */
    val saved = MutableStateFlow(0)

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
    }

    /**
     * A user action from the tray or the inbox. Saved first, shown locally at once, then sent.
     * If sending fails it stays queued and goes out before the next WebSocket connect.
     * [server] is the server the tray item came from (null = the inbox, which always shows the current one).
     * An action for a server that is no longer configured is dropped, never sent to the new one, and so is
     * one that waited across a Settings switch.
     */
    fun act(id: String, action: String, body: String = "{}", server: String? = null, done: () -> Unit = {}) =
        actAll(listOf(id), action, body, server, done)

    /** Swiping a topic's stack away (#108): every row it held, queued together so they go out as one clear call. */
    fun dismissAll(ids: List<String>, server: String?, done: () -> Unit = {}) = actAll(ids, "dismiss", "{}", server, done)

    private fun actAll(ids: List<String>, action: String, body: String, server: String?, done: () -> Unit) {
        val gen = configGen // read now, not when the save runs
        acts.execute {
            try {
                val local = when (action) {
                    "dismiss" -> Notif.DISMISSED
                    "snooze" -> Notif.SNOOZED
                    "ack", "reply" -> Notif.ACKNOWLEDGED
                    "reopen" -> Notif.ACTIVE
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
                            if (local != null) dao.setState(id, local)
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
    }
}

/** [Store] backed by Room, driving the tray. */
class RoomStore(private val app: App) : Store {
    private val dao = app.db.dao()

    /** The settings generation this store syncs for. After a switch in Settings its reads and writes are dropped. */
    private val gen = app.configGen
    private inline fun ifMine(f: () -> Unit) = synchronized(app.configLock) { if (gen == app.configGen) f() }

    override fun lastSeq() = dao.lastSeq() ?: 0

    override fun apply(notifs: Collection<Notif>, seq: Long, quiet: Boolean) = ifMine {
        // Only rows Room kept reach the tray, so an older event can never undo a newer state there.
        val kept = dao.applyAll(notifs, seq)
        val muted = app.muted
        val waiting = waiting()
        val acting = dao.pending().filter { it.action == "reopen" }.map { it.notifId }.toSet() // our own reopen's event may beat its answer here
        for (n in kept) {
            val step = if (n.id in waiting) TrayStep.Cancel(n.id) else TrayStep.plan(n, muted)
            app.tray.apply(if ((quiet || n.id in acting) && step is TrayStep.Show) step.copy(silent = true) else step)
        }
        dao.prune(System.currentTimeMillis() - 7 * 24 * 3600_000L)
    }

    override fun replaceAll(open: List<Notif>, seq: Long, epoch: String) = ifMine {
        val waiting = waiting()
        val sameDb = epoch.isEmpty() || app.epoch.isEmpty() || epoch == app.epoch
        app.tray.reconcile(dao.replaceAll(open, seq, sameDb).filter { it.id !in waiting }, app.muted)
        if (epoch.isNotEmpty()) app.epoch = epoch
    }

    override fun epoch() = synchronized(app.configLock) { if (gen == app.configGen) app.epoch else "" } // another server's epoch says nothing here
    override fun saveEpoch(epoch: String) = ifMine { app.epoch = epoch }

    override fun pending() = synchronized(app.configLock) { if (gen == app.configGen) dao.pending() else emptyList() } // never send the new server's queue to the old one
    /**
     * The queued action no longer hides the row, so the tray shows what Room says (another device may have reopened it).
     * Quietly: it follows the person's own tap (a reopen from the inbox), and a row that alerted already did so once.
     */
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
