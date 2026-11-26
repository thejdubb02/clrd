package me.itsjustin.clrd

import android.app.Application
import android.content.Context
import org.json.JSONObject
import java.util.concurrent.Executors

/** App-wide singletons. Small enough that a DI framework would be the bigger thing. */
class App : Application() {
    /** The server this phone talks to (#78). Its settings, database and sync state; the members below that name a server forward to it. */
    val account by lazy { Account(this, 0) }
    val db get() = cur.db
    /** The shown server's client certificate (#152, #78). Each server has its own, offered only to its own host. */
    val clientCert get() = cur.clientCert
    /** Account 0's client: a new address is tried with it, which offers its certificate to account 0's host only. */
    val http get() = account.http
    val tray get() = cur.tray

    private val others = HashMap<Int, Account>() // guarded by this
    /** The accounts past account 0, in the order added, from the phone part of the "clrd" file. None is the old one-server install. */
    private fun ids() = prefs.getString("servers", null).orEmpty().split(',').mapNotNull { it.toIntOrNull() }

    /** Every server on this phone (#78): account 0 first, then the ones added. One instance per id, as its state lives on it. */
    val accounts: List<Account> get() = listOf(account) + ids().mapNotNull(::account)

    /** The account for [id], or null when there is none (removed, or never made): a tap for it is ignored. */
    @Synchronized fun account(id: Int): Account? = when {
        id == 0 -> account
        id in ids() -> others.getOrPut(id) { Account(this, id) }
        else -> null
    }

    val anyConfigured get() = accounts.any { it.configured }
    /** The servers set up on this phone. More than one brings the inbox switcher, the Servers list and the host in tray entries. */
    val servers get() = accounts.filter { it.configured }

    /**
     * The server the screens show (#78), remembered on this phone. Account 0 until another is picked, and when the
     * remembered one is gone. Only the screens follow it: the sync service, the tray and the receivers keep their own.
     */
    val shown by lazy { kotlinx.coroutines.flow.MutableStateFlow(account(prefs.getInt("shown_server", 0)) ?: account) }

    /** Shows server [id] on the screens and remembers it. An unknown id changes nothing. */
    fun show(id: Int) {
        val a = account(id) ?: return
        prefs.edit().putInt("shown_server", id).apply()
        shown.value = a
    }

    /** The server at [url]'s address, if this phone has it. */
    fun accountAt(url: String) = accounts.firstOrNull { it.serverUrl.isNotEmpty() && serverAddress(it.serverUrl) == serverAddress(url) }

    /**
     * Saves a server from a setup link or Add server, then shows it. An address this phone has goes to that server,
     * an empty account 0 is filled as before #78, and any other address is added as another server, never
     * replacing one. Call on [io].
     */
    fun saveTo(url: String, token: String): Account {
        val a = accountAt(url) ?: account.takeIf { !it.configured }
        if (a != null) a.saveServer(url, token)
        val got = a ?: addAccount(url, token).also { redrawTrays() } // the entries already up now name their server
        show(got.id)
        return got
    }

    /** Reposts every server's tray entries quietly, such as after the stack switch or a server added or removed. */
    fun redrawTrays() = io.execute(::redraw)
    // Not an emptied account 0: its sweep would write its keys back into the phone's file.
    private fun redraw() = servers.forEach { RoomStore(this, it).reconcile() }

    /** Adds a server. Ids are never used twice, so a stale tray tap for a removed one never lands on this one. */
    @Synchronized fun addAccount(url: String, token: String): Account {
        val id = prefs.getInt("next_server_id", 1)
        prefs.edit().putInt("next_server_id", id + 1).putString("servers", (ids() + id).joinToString(",")).commit()
        return account(id)!!.also {
            it.serverUrl = url
            it.token = token
            it.tray.createChannels() // a sound another server never used may have no channels yet
            if (it.configured) SyncService.start(this)
        }
    }

    /**
     * Removes a server: its connection stops at once, then on [io] its tray entries go, and its file and
     * database are deleted. Account 0's file also holds the phone's choices, so only its server keys go.
     */
    fun removeAccount(id: Int) {
        val a = synchronized(this) {
            val a = account(id) ?: return
            if (id != 0) {
                // With a marker, so a removal the process did not live to finish is finished on the next start.
                prefs.edit().putString("servers", (ids() - id).joinToString(",")).putString("removing", (removing() + id).joinToString(",")).commit()
                others.remove(id)
                a.removed = true
            }
            // At once, before its database closes: the screens move to a server still set up, else account 0.
            if (shown.value.id == id) show((servers.firstOrNull { it.id != id } ?: account).id)
            saved.value++ // the switcher and the Servers list read the list again
            a
        }
        synchronized(a.configLock) { // with the service's start under the same lock, no client can arrive after this
            a.configGen++
            a.sync?.stop()
            a.sync = null
        }
        io.execute {
            if (id == 0) {
                a.saveServer("", "")
                synchronized(a.configLock) {
                    a.db.dao().forget()
                    // Keep-list, not a list of server keys: a key added later and forgotten here errs toward wiping a token.
                    prefs.edit().apply { prefs.all.keys.filter { it !in PHONE_KEYS }.forEach(::remove) }.commit()
                }
                a.clientCert.value = null
                a.soundNow.value = a.sound
                a.tray.createChannels() // back on the default sound, whose channels an earlier change may have deleted
                a.tray.clear()
            } else purge(a)
            redraw() // already on io, so a drain after the removal covers it; one server left drops the host
            if (!anyConfigured) stopService(android.content.Intent(this, SyncService::class.java)) else SyncService.start(this)
        }
    }
    private fun removing() = prefs.getString("removing", null).orEmpty().split(',').mapNotNull { it.toIntOrNull() }

    /** A removed server's file, database and tray entries go, then its marker. Call on [io]. */
    private fun purge(a: Account) {
        a.close()
        deleteSharedPreferences("clrd-${a.id}")
        deleteDatabase("clrd-${a.id}.db") // with its -wal and -shm
        a.tray.clear()
        synchronized(this) { prefs.edit().putString("removing", (removing() - a.id).joinToString(",")).commit() }
    }

    /** Finishes removals a killed process left behind; [onCreate] runs it. */
    internal fun purgeRemoved() = removing().forEach { id -> io.execute { purge(Account(this, id).also { it.removed = true }) } }

    val images by lazy { Images(this) }
    /** This phone's own choices, whichever server it talks to. Per-server settings live in [Account.prefs]. */
    val prefs by lazy { getSharedPreferences("clrd", Context.MODE_PRIVATE) }
    val io = Executors.newSingleThreadExecutor()
    /** User actions only, and they never wait on the network, so a tray tap is saved within its goAsync() deadline. */
    val acts = Executors.newSingleThreadExecutor()
    /** Typed answers the server refused, by notification id, so the reply box opens with them again (#153). */
    val drafts = java.util.concurrent.ConcurrentHashMap<String, String>()
    /** Typed answers refused because the question changed (409), counted by notification id, so a reply box open on it closes (#516). */
    val changed = androidx.compose.runtime.mutableStateMapOf<String, Int>()
    val sender get() = cur.sender
    /** Sends for [act] while no sync service runs. One at a time, so a hung send queues the next instead of piling up threads. */
    internal val sends = Executors.newSingleThreadExecutor()

    // Forwarders to the shown server ([shown]), so the screens read it as they read the one server before. Explicit
    // getters, not "by account::": that would open the account before the app has a context. Background paths
    // (the sync service, the tray, the receivers) hold their own [Account] and never read these.
    private val cur get() = shown.value
    val status get() = cur.status
    var sync: SyncClient?
        get() = cur.sync
        set(v) { cur.sync = v }
    val configLock get() = cur.configLock
    var configGen: Int
        get() = cur.configGen
        set(v) { cur.configGen = v }
    var serverUrl: String
        get() = cur.serverUrl
        set(v) { cur.serverUrl = v }
    var token: String
        get() = cur.token
        set(v) { cur.token = v }
    var epoch: String
        get() = cur.epoch
        set(v) { cur.epoch = v }
    var muted: Set<String>
        get() = cur.muted
        set(v) { cur.muted = v }
    var trayHidden: Set<String>
        get() = cur.trayHidden
        set(v) { cur.trayHidden = v }
    val configured get() = cur.configured
    fun api() = cur.api()
    val sound get() = cur.sound
    val soundNow get() = cur.soundNow
    val soundPending get() = cur.soundPending
    fun saveSound(name: String) = cur.saveSound(name)
    fun pickSound(name: String) = cur.pickSound(name)
    fun pushSound() = cur.pushSound()
    internal fun moveSound() = cur.moveSound()
    val snooze get() = cur.snooze
    val swipes get() = cur.swipes
    val favicons get() = cur.favicons
    val formatting get() = cur.formatting
    val autolink get() = cur.autolink
    val linkPreviews get() = cur.linkPreviews
    val topicPreviews get() = cur.topicPreviews
    val swipesLocked get() = cur.swipesLocked
    val settingsRev get() = cur.settingsRev
    fun saveSwipes(next: Swipes, api: Api = api()) = cur.saveSwipes(next, api)
    fun saveDisplay(key: String, on: Boolean, api: Api = api()) = cur.saveDisplay(key, on, api)
    internal val subs get() = cur.subs
    internal fun loadSubs(api: Api = api()) = cur.loadSubs(api)
    internal fun putSubs(body: JSONObject, api: Api = api()) = cur.putSubs(body, api)
    fun subscribe(topic: String, on: Boolean, api: Api = api()) = cur.subscribe(topic, on, api)
    val markId get() = cur.markId
    fun mark(id: String, kind: String?, note: String, link: String, done: (String?) -> Unit) = cur.mark(id, kind, note, link, done)
    fun saveSettings(o: JSONObject) = cur.saveSettings(o)
    fun pruneCleared(now: Long = System.currentTimeMillis()) = cur.pruneCleared(now)
    /** Saves the server and token from Settings or a scanned link. Call on [io]. */
    fun saveServer(newUrl: String, newToken: String) = cur.saveServer(newUrl, newToken)

    /**
     * Stack by topic (#108), in the tray and the app's list. Off until a person turns it on (#465), as on the web;
     * a phone that never touched it reads the default, so it moves to off, and a stored choice is kept.
     */
    var stack: Boolean
        get() = prefs.getBoolean("stack", false)
        set(v) = prefs.edit().putBoolean("stack", v).apply()

    /** Remove cleared items from this phone as soon as they are cleared (#151). Off by default; the server keeps its own copy either way. */
    /** A lone swipe in the shade clears everywhere (#271). Off out of the box: a swipe only hides it here (#265). */
    var shadeClears: Boolean
        get() = prefs.getBoolean("shade_clears", false)
        set(v) = prefs.edit().putBoolean("shade_clears", v).apply()

    /** When rows left the shade, uptime ms, so a lone swipe can be told from a stack, a bundle or Clear all (#271). */
    private val leftShade = ArrayList<Long>()

    /** Rows left the shade now: a stack swipe, or a swipe or Clear all on one row. Counted toward a burst either way. */
    fun leftShade(count: Int) = synchronized(leftShade) {
        val now = android.os.SystemClock.uptimeMillis()
        leftShade.removeAll { it < now - 2 * LONE_MS } // only the last burst window matters, so the list stays small
        repeat(count) { leftShade += now }
    }

    /**
     * One row swiped out of the shade with "Swipe clears everywhere" on (#271): Android sends the same delete for a
     * swipe, a bundle swipe and Clear all, so it waits [LONE_MS] and clears only if no other row left near it.
     * The receiver returns at once: holding it would hold back the next delete in a burst, so the first row would look alone.
     */
    fun swiped(id: String, server: String?, acct: Account = account) {
        val at = android.os.SystemClock.uptimeMillis()
        android.os.Handler(android.os.Looper.getMainLooper()).postDelayed({
            if (synchronized(leftShade) { lone(leftShade, at) }) acct.actAll(listOf(id), "dismiss", "{}", server, VIA_TRAY_SWIPE) {}
        }, LONE_MS)
    }

    /** A swipe all the way across runs the first button it shows (#161). Off out of the box: a swipe only shows the buttons. */
    var fastSwipe: Boolean
        get() = prefs.getBoolean("fast_swipe", false)
        set(v) = prefs.edit().putBoolean("fast_swipe", v).apply()

    var dropCleared: Boolean
        get() = prefs.getBoolean("drop_cleared", false)
        set(v) = prefs.edit().putBoolean("drop_cleared", v).apply()

    fun pickClientCert(alias: String?) = cur.pickClientCert(alias)

    /** Display, Appearance (#583): "system", "light" or "dark", this phone only. Applies at once through [ClrdTheme]. */
    val appearance by lazy { kotlinx.coroutines.flow.MutableStateFlow(prefs.getString("appearance", null) ?: "system") }
    fun pickAppearance(v: String) { appearance.value = v; prefs.edit().putString("appearance", v).apply() }

    /** Bumped on every [saveServer], so Settings shows a server and token saved from elsewhere (a scanned link). */
    val saved = kotlinx.coroutines.flow.MutableStateFlow(0)

    /** Bumped when the stream sends the device list again, so Settings reads it fresh (#345). */
    val devicesChanged = kotlinx.coroutines.flow.MutableStateFlow(0)

    /** Bumped on a reset and when an action's answer is for a topic this phone does not keep, so a topic read without subscribing reloads (#624). */
    val outside = kotlinx.coroutines.flow.MutableStateFlow(0)

    override fun onCreate() {
        super.onCreate()
        account.tray.createChannels()
        account.moveSound()
        purgeRemoved()
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
        cur.actAll(listOf(id), action, body, server, via, done)

    /** One action on several rows from the inbox, such as a stack's Clear (#360): the dismisses go out as one clear call. */
    fun act(ids: List<String>, action: String, via: String = VIA_APP) = cur.actAll(ids, action, "{}", null, via) {}

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

    /** The app is on screen (#598): only then may a change move a card. */
    @Volatile var front = false

    /** Rows changed live while the app was in front, by id (#598). The inbox moves only these, and only while fresh. */
    val moved = java.util.concurrent.ConcurrentHashMap<String, Moved>()

    fun moving(rows: List<Pair<String, Moved>>) {
        if (!front) return
        val now = android.os.SystemClock.uptimeMillis()
        moved.values.removeIf { now - it.at > FRESH_MS }
        rows.forEach { (id, m) -> moved[id] = m.copy(at = now) }
    }

    companion object {
        fun of(ctx: Context) = ctx.applicationContext as App

        // How an action was made, sent as "via" (docs/SPEC.md).
        const val VIA_APP = "app"
        const val VIA_TRAY_BUTTON = "tray_button"
        const val VIA_TRAY_SWIPE = "tray_swipe"
        const val LONE_MS = 1000L
        const val VIA_APP_BULK = "app_bulk"
        /** The most days of cleared history this phone keeps, whatever the server keeps. */
        const val KEEP_DAYS = 7

        /** The keys in the "clrd" file that belong to the phone, not to account 0's server (#78). */
        val PHONE_KEYS = setOf("stack", "shade_clears", "fast_swipe", "drop_cleared", "appearance", "dnd_lost", "install_seed", "servers", "next_server_id", "shown_server")
    }
}

/** Set when this phone starts following a topic set (#764), cleared once a full reload lands. */
private const val FOLLOW_RELOAD = "follow_reload"

/** [Store] backed by Room, driving the tray. */
class RoomStore(private val app: App, private val acct: Account = app.account) : Store {
    private val dao = acct.db.dao()

    /** The settings generation this store syncs for. After a switch in Settings its reads and writes are dropped. */
    private val gen = acct.configGen
    private inline fun ifMine(f: () -> Unit) = synchronized(acct.configLock) { if (gen == acct.configGen && !acct.removed) f() }

    override fun lastSeq() = dao.lastSeq() ?: 0

    override fun apply(notifs: Collection<Notif>, seq: Long, quiet: Boolean, again: Boolean, answer: Boolean, shown: Map<String, Long>) = ifMine {
        // Only rows Room kept reach the tray, so an older event can never undo a newer state there.
        // A row fetched outside the stream (seq 0, such as a queued action's answer) is not filtered by the server:
        // one from a topic unsubscribed meanwhile must not come back after the reset dropped it (#617).
        val subs = acct.subs.value
        val (keep, out) = if (seq == 0L && subs?.filtered == true) notifs.partition { subs.subscribed(it.topic) } else notifs.toList() to emptyList<Notif>()
        if (out.isNotEmpty()) app.outside.value++
        val saved = dao.applyAll(keep, seq, answer)
        // #500: a live repeat Room dropped (our own mark's answer got in first) still rings the newer row, if that one shows unacked.
        // Only when the newer row differs by marks alone (#504): after a Clear and Undo it is a new state, and an Undo stays quiet.
        // And never when the repeat is older than this phone's own state change: when it made every change the rows can match (#550).
        val kept = if (!again) saved else saved + notifs.filter { n -> saved.none { it.id == n.id } && n.seq >= (dao.answerOf(n.id) ?: 0) }.mapNotNull { n ->
            dao.get(n.id)?.takeIf { it.copy(marks = n.marks, seq = n.seq, updatedAt = n.updatedAt, silenced = n.silenced) == n }
        }
        val muted = acct.muted
        val waiting = waiting()
        val acting = dao.pending().filter { it.action == "reopen" }.map { it.notifId }.toSet() // our own reopen's event may beat its answer here
        for (n in kept) {
            val step = if (n.id in waiting) TrayStep.Cancel(n.id) else TrayStep.plan(n, muted)
            acct.tray.apply(
                when {
                    step !is TrayStep.Show -> step
                    quiet || n.id in acting -> step.copy(silent = true)
                    // #589: a show after a hide in the replay rings like a new one, unless it is this phone's own change.
                    else -> step.copy(again = !step.silent && (again || (shown[n.id] ?: 0) > (dao.answerOf(n.id) ?: 0)))
                },
            )
        }
        acct.pruneCleared()
    }

    override fun live(notifs: Collection<Notif>, arrive: Boolean) = ifMine { app.moving(notifs.map { it.id to Moved(0, arrive, it.state, it.stateBy) }) }

    override fun replaceAll(open: List<Notif>, seq: Long, epoch: String) = ifMine {
        val waiting = waiting()
        val sameDb = epoch.isEmpty() || acct.epoch.isEmpty() || epoch == acct.epoch
        acct.tray.reconcile(dao.replaceAll(open, seq, sameDb).filter { it.id !in waiting }, acct.muted)
        if (epoch.isNotEmpty()) acct.epoch = epoch
        acct.prefs.edit().remove(FOLLOW_RELOAD).apply() // what it holds is the followed set now (#764)
        app.outside.value++
    }

    override fun epoch() = synchronized(acct.configLock) { if (gen == acct.configGen && !acct.removed) acct.epoch else "" } // another server's epoch says nothing here
    override fun saveEpoch(epoch: String) = ifMine { acct.epoch = epoch }
    /** A settings message, kept only while this is still the server in Settings: one already in hand at a switch would undo the reset (#568). */
    fun settings(o: org.json.JSONObject): Boolean {
        var reload = false
        ifMine {
            val was = acct.subs.value?.filtered == true
            val fmt = acct.formatting.value
            acct.saveSettings(o)
            if (fmt != acct.formatting.value) acct.tray.reconcile(dao.openNow().filter { it.id !in waiting() }, acct.muted) // #809: quietly, in the new look
            val now = acct.subs.value?.filtered == true
            if (!was && now) acct.prefs.edit().putBoolean(FOLLOW_RELOAD, true).apply()
            reload = now && acct.prefs.getBoolean(FOLLOW_RELOAD, false)
        }
        // Now following a set it did not before (#764): what this phone holds may include topics it no longer
        // shows. The mark stays until a reload lands, so a failed one is tried again on the next connect.
        return reload
    }
    override fun saveMarkId(id: String) = ifMine { if (acct.markId.value != id) { acct.prefs.edit().putString("mark_id", id).apply(); acct.markId.value = id } }

    override fun pending() = synchronized(acct.configLock) { if (gen == acct.configGen && !acct.removed) dao.pending() else emptyList() } // never send the new server's queue to the old one
    /**
     * The queued action no longer hides the row, so the tray shows what Room says (another device may have reopened it).
     * Quietly: it follows the person's own tap (a reopen from the inbox), and a row that alerted already did so once.
     */
    /** Says it plainly, whether the tap came from the app or the tray (#330). */
    override fun replyOutcome(p: Pending, outcome: String) = ifMine {
        dao.told(p.key) // before the message: a restart after it may cost the message, never repeat it
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
        if (n != null && n.id !in waiting()) acct.tray.apply(TrayStep.plan(n, acct.muted).let { if (it is TrayStep.Show) it.copy(silent = true) else it })
    }

    /** Under the lock, so a sync write cannot land between reading the rows and posting them. */
    fun reconcile() = ifMine {
        val waiting = waiting()
        acct.tray.reconcile(dao.openNow().filter { it.id !in waiting }, acct.muted)
    }

    /** A queued dismiss or snooze that has not reached the server yet wins over what the server last said. */
    private fun waiting() = dao.pending().filter { it.action == "dismiss" || it.action == "snooze" }.map { it.notifId }.toSet()
}

/** A topic_previews object as topic to on or off; a topic without a boolean has no choice of its own. */
internal fun onOff(o: org.json.JSONObject?): Map<String, Boolean> =
    o?.keys()?.asSequence()?.mapNotNull { k -> (o.opt(k) as? Boolean)?.let { k to it } }?.toMap().orEmpty()

/** True when [at] is the only time in [times] within [App.LONE_MS] either side: a lone swipe, not a burst (#271). */
internal fun lone(times: List<Long>, at: Long) = times.count { kotlin.math.abs(it - at) <= App.LONE_MS } == 1
