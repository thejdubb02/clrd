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
    val prefs by lazy { getSharedPreferences("clrd", Context.MODE_PRIVATE) }
    val io = Executors.newSingleThreadExecutor()

    val status = MutableStateFlow(SyncClient.Status.OFFLINE)

    /** Live sync client while the service runs; null otherwise. */
    @Volatile var sync: SyncClient? = null

    var serverUrl: String
        get() = prefs.getString("url", "").orEmpty()
        set(v) = prefs.edit().putString("url", v.trim().trimEnd('/')).apply()
    var token: String
        get() = prefs.getString("token", "").orEmpty()
        set(v) = prefs.edit().putString("token", v.trim()).apply()
    var muted: Set<String>
        get() = prefs.getStringSet("muted", emptySet()).orEmpty()
        set(v) = prefs.edit().putStringSet("muted", v).apply()

    val configured get() = serverUrl.isNotEmpty() && token.isNotEmpty()

    fun api() = Api(http, serverUrl, token)

    override fun onCreate() {
        super.onCreate()
        tray.createChannels()
    }

    /**
     * A user action from the tray or the inbox. Saved first, shown locally at once, then sent.
     * If sending fails it stays queued and goes out before the next WebSocket connect.
     */
    fun act(id: String, action: String, body: String = "{}", done: () -> Unit = {}) {
        io.execute {
            try {
                val dao = db.dao()
                dao.addPending(Pending(notifId = id, action = action, body = body))
                val local = when (action) {
                    "dismiss" -> Notif.DISMISSED
                    "snooze" -> Notif.SNOOZED
                    "ack" -> Notif.ACKNOWLEDGED
                    "reopen" -> Notif.ACTIVE
                    else -> null
                }
                if (local != null) dao.setState(id, local)
                if (local == Notif.DISMISSED || local == Notif.SNOOZED) tray.apply(TrayStep.Cancel(id))
                val s = sync
                if (s != null) s.flushSoon() else if (configured) runCatching { flushPending(RoomStore(this), api()) }
            } finally {
                done()
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

    override fun lastSeq() = dao.lastSeq() ?: 0

    override fun apply(notifs: Collection<Notif>, seq: Long) {
        dao.applyAll(notifs, seq)
        val muted = app.muted
        // A queued dismiss or snooze that has not reached the server yet wins over what the server last said.
        val waiting = dao.pending().filter { it.action == "dismiss" || it.action == "snooze" }.map { it.notifId }.toSet()
        for (n in notifs) {
            app.tray.apply(if (n.id in waiting) TrayStep.Cancel(n.id) else TrayStep.plan(n, muted))
        }
        dao.prune(System.currentTimeMillis() - 7 * 24 * 3600_000L)
    }

    override fun replaceAll(open: List<Notif>, seq: Long) {
        dao.replaceAll(open, seq)
        app.tray.reconcile(open, app.muted)
    }

    override fun pending() = dao.pending()
    override fun removePending(p: Pending) = dao.removePending(p.key)

    fun reconcile() = app.tray.reconcile(dao.openNow(), app.muted)
}
