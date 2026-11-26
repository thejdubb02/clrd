package me.itsjustin.clrd

import android.app.Application
import android.content.Context
import androidx.room.Room
import kotlinx.coroutines.flow.MutableStateFlow
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.OkHttpClient
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** App-wide singletons. Small enough that a DI framework would be the bigger thing. */
class App : Application() {
    val db by lazy { Room.databaseBuilder(this, Db::class.java, "clrd.db").build() }
    /** Client certificate alias picked in Settings (#152), or none. Offered only to the server's own host. */
    val clientCert by lazy { MutableStateFlow(prefs.getString("client_cert", null)) }
    /** The server origin the certificate was picked for. A moved server gets no certificate until it is picked again. */
    private fun origin(u: okhttp3.HttpUrl) = "${u.scheme}://${u.host}:${u.port}"
    private val certKeys by lazy {
        ClientCertKeys({ clientCert.value }, {
            serverUrl.toHttpUrlOrNull()?.takeIf { origin(it) == prefs.getString("client_cert_origin", null) }?.let { it.host to it.port }
        }) { a ->
            runCatching { // a removed key or revoked access means no certificate, not a crash
                val k = android.security.KeyChain.getPrivateKey(this, a)
                val c = android.security.KeyChain.getCertificateChain(this, a)
                if (k != null && c != null) k to c else null
            }.getOrNull()
        }
    }
    val http by lazy {
        OkHttpClient.Builder()
            .sslSocketFactory(certKeys.socketFactory, certKeys.trustManager)
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
    fun swiped(id: String, server: String?) {
        val at = android.os.SystemClock.uptimeMillis()
        android.os.Handler(android.os.Looper.getMainLooper()).postDelayed({
            if (synchronized(leftShade) { lone(leftShade, at) }) act(id, "dismiss", "{}", server, VIA_TRAY_SWIPE)
        }, LONE_MS)
    }

    /** A swipe all the way across runs the first button it shows (#161). Off out of the box: a swipe only shows the buttons. */
    var fastSwipe: Boolean
        get() = prefs.getBoolean("fast_swipe", false)
        set(v) = prefs.edit().putBoolean("fast_swipe", v).apply()

    var dropCleared: Boolean
        get() = prefs.getBoolean("drop_cleared", false)
        set(v) = prefs.edit().putBoolean("drop_cleared", v).apply()

    /** Picks or removes the client certificate (#152). Open connections used the old one, so they close and sync reconnects. */
    fun pickClientCert(alias: String?) {
        prefs.edit().putString("client_cert", alias).putString("client_cert_origin", serverUrl.toHttpUrlOrNull()?.let(::origin)).apply()
        clientCert.value = alias
        io.execute {
            certKeys.renew()
            http.connectionPool.evictAll()
            if (configured) SyncService.start(this, restart = true)
        }
    }

    /** Display, Appearance (#583): "system", "light" or "dark", this phone only. Applies at once through [ClrdTheme]. */
    val appearance by lazy { MutableStateFlow(prefs.getString("appearance", null) ?: "system") }
    fun pickAppearance(v: String) { appearance.value = v; prefs.edit().putString("appearance", v).apply() }

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

    /** The buttons a swipe on a card shows (#161), as the server last sent them; the defaults until it does. */
    val swipes by lazy { MutableStateFlow(Swipes.parse(prefs.getString("swipes", null))) }

    /** The favicons setting (#107): a site icon beside each link on a card. On until the server says otherwise. */
    val favicons by lazy { MutableStateFlow(prefs.getBoolean("favicons", true)) }

    /** The render_formatting setting (#107): off shows every message as plain text. On until the server says otherwise. */
    val formatting by lazy { MutableStateFlow(prefs.getBoolean("render_formatting", true)) }

    /** The autolink setting (#107): off leaves bare web addresses as text; written links stay links. On until the server says otherwise. */
    val autolink by lazy { MutableStateFlow(prefs.getBoolean("autolink", true)) }

    /** The link_previews setting (#107): a card with the page's title and picture under an open message's first link. Off until the server says otherwise. */
    val linkPreviews by lazy { MutableStateFlow(prefs.getBoolean("link_previews", false)) }

    /** topic_previews (#107): a topic's own preview choice, over [linkPreviews]. */
    val topicPreviews by lazy { MutableStateFlow(onOff(prefs.getString("topic_previews", null)?.let { runCatching { org.json.JSONObject(it) }.getOrNull() })) }

    /** The four display switches by their server names, each with its out-of-the-box value. */
    private val display get() = mapOf("render_formatting" to (formatting to true), "autolink" to (autolink to true), "favicons" to (favicons to true), "link_previews" to (linkPreviews to false))

    /** This token got 403 changing [swipes] or a display switch, so Settings shows them read-only. In memory: a token's grants can widen. */
    val swipesLocked = MutableStateFlow(false)

    /** Goes up on every settings message and every save the server took (#818), so an open preview asks again after a switch. */
    val settingsRev = MutableStateFlow(0)

    /**
     * Sends [next] from Settings (#161). It shows at once; the server's settings message confirms it to every device.
     * Null when the server took it or the token may not change it ([swipesLocked]), else why not. Call on [io].
     */
    fun saveSwipes(next: Swipes, api: Api = api()): String? {
        // On success the server has it: a restart before its echo keeps it.
        return putSettings(api, next.json(), { swipes.value = next }, { prefs.edit().putString("swipes", next.json()).apply() }) { swipes.value = Swipes.parse(prefs.getString("swipes", null)) }
    }

    /** Sends one display switch from Settings (#107), like [saveSwipes]. A formatting change redraws the shade at once, quietly. Call on [io]. */
    fun saveDisplay(key: String, on: Boolean, api: Api = api()): String? {
        val (flow, default) = display.getValue(key)
        fun set(v: Boolean) { if (flow.value != v) { flow.value = v; if (key == "render_formatting") RoomStore(this).reconcile() } }
        return putSettings(api, org.json.JSONObject().put(key, on).toString(), { set(on) }, { prefs.edit().putBoolean(key, on).apply() }) { set(prefs.getBoolean(key, default)) }
    }

    /** Shows [body] at once and PUTs it; null when the server took it or this token may not change it ([swipesLocked]), else why not. */
    private fun putSettings(api: Api, body: String, show: () -> Unit, keep: () -> Unit, undo: () -> Unit): String? = try {
        val rev = synchronized(configLock) { show(); settingsRev.value } // no settings message between the two
        api.put("/api/v1/settings", body)
        // A settings message that landed meanwhile is newer than this reply (#818): keep it, not what was sent.
        synchronized(configLock) { if (settingsRev.value == rev) keep(); settingsRev.value++ }
        null
    } catch (e: Exception) { // a bad saved address throws IllegalArgumentException, not IOException
        // Back to what the server last said. Under the lock a settings message cannot land between the read and the write (#568).
        synchronized(configLock) { undo() }
        if (e is HttpError && e.code == 403) { swipesLocked.value = true; null } else (e as? HttpError)?.message ?: getString(R.string.sound_no_connection)
    }

    /** This account's topic subscriptions (#617), as the server last sent them; null from a server without them. */
    internal val subs by lazy { MutableStateFlow(Subs.of(prefs.getString("subs", null)?.let { runCatching { org.json.JSONObject(it) }.getOrNull() })) }

    /** `GET /api/v1/subscriptions`; null from a server without the route. Call on [io]. */
    internal fun loadSubs(api: Api = api()): SubsList? = try {
        SubsList.from(org.json.JSONObject(api.get("/api/v1/subscriptions")))
    } catch (e: HttpError) { if (e.code == 404) null else throw e }

    /**
     * Sends only the fields in [body]; the reply is the new set. The server then sends every device of this
     * account its settings and a reset, which drops or brings back the topic's cards and tray entries. Call on [io].
     */
    internal fun putSubs(body: org.json.JSONObject, api: Api = api()): SubsList =
        SubsList.from(org.json.JSONObject(api.put("/api/v1/subscriptions", body.toString())))

    /** Subscribes this account to [topic] or not, on every device. Null when the server took it, else why not. Call on [io]. */
    fun subscribe(topic: String, on: Boolean, api: Api = api()): String? = try {
        putSubs(topicBody(topic, on), api); null
    } catch (e: Exception) { (e as? HttpError)?.message ?: getString(R.string.sound_no_connection) }

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

    /** Keeps a server `settings` message. The sync client calls it through [RoomStore.settings], so an old server's message is dropped. */
    fun saveSettings(o: org.json.JSONObject) {
        settingsRev.value++
        if (o.has("sound")) serverSound(o.optString("sound"))
        if (o.has("keep_cleared_days")) { // the server's keep for cleared rows (#261): drop what it deleted
            val keep = org.json.JSONObject().put("days", o.optInt("keep_cleared_days", KEEP_DAYS))
                .put("topics", o.optJSONObject("topic_keep_cleared_days") ?: org.json.JSONObject())
            prefs.edit().putString("keep", keep.toString()).apply()
            io.execute { pruneCleared() }
        }
        if (o.has("swipe_start") || o.has("swipe_end")) { // the buttons a swipe shows (#161); an older server sends neither
            val w = Swipes.from(o, Swipes.parse(prefs.getString("swipes", null)))
            prefs.edit().putString("swipes", w.json()).apply()
            swipes.value = w
        }
        for ((key, d) in display) {
            if (!o.has(key)) continue
            val on = o.optBoolean(key, d.second)
            prefs.edit().putBoolean(key, on).apply()
            d.first.value = on
        }
        if (o.has("topic_previews")) {
            val t = o.optJSONObject("topic_previews")
            prefs.edit().apply { if (t == null) remove("topic_previews") else putString("topic_previews", t.toString()) }.apply()
            topicPreviews.value = onOff(t)
        }
        // Every settings message is the whole view, so no subscriptions object means a server without them (#617).
        val sub = o.optJSONObject("subscriptions")
        prefs.edit().apply { if (sub == null) remove("subs") else putString("subs", sub.toString()) }.apply()
        subs.value = Subs.of(sub)
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
        if (dropCleared) dao.dropCleared() else {
            dao.prune(before(k.optInt("days", KEEP_DAYS)), own)
            own.forEach { dao.pruneTopic(it, before(topics.optInt(it, KEEP_DAYS))) }
        }
        dao.pruneAnswers()
    }

    val configured get() = serverUrl.isNotEmpty() && token.isNotEmpty()

    fun api() = Api(http, serverUrl, token)

    /** Bumped on every [saveServer], so Settings shows a server and token saved from elsewhere (a scanned link). */
    val saved = MutableStateFlow(0)

    /** Bumped when the stream sends the device list again, so Settings reads it fresh (#345). */
    val devicesChanged = MutableStateFlow(0)

    /** Bumped on a reset and when an action's answer is for a topic this phone does not keep, so a topic read without subscribing reloads (#624). */
    val outside = MutableStateFlow(0)

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
                prefs.edit().remove("snooze").remove("sound_pending").remove("keep").remove("mark_id").remove("swipes").remove("subs").remove("favicons").remove("render_formatting").remove("autolink").remove("link_previews").remove("topic_previews").apply() // a choice the old server refused is not the new one's
                snooze.value = Snooze()
                swipes.value = Swipes()
                favicons.value = true
                formatting.value = true
                autolink.value = true
                linkPreviews.value = false
                topicPreviews.value = emptyMap()
                swipesLocked.value = false // another token may change them
                markId.value = ""
                subs.value = null // the new token's own set arrives with its settings message
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
                            if (local != null) moving(listOf(id to Moved(0, false, local, null))) // before Room shows it (#598)
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
        const val VIA_TRAY_SWIPE = "tray_swipe"
        const val LONE_MS = 1000L
        const val VIA_APP_BULK = "app_bulk"
        /** The most days of cleared history this phone keeps, whatever the server keeps. */
        const val KEEP_DAYS = 7
    }
}

/** Set when this phone starts following a topic set (#764), cleared once a full reload lands. */
private const val FOLLOW_RELOAD = "follow_reload"

/** [Store] backed by Room, driving the tray. */
class RoomStore(private val app: App) : Store {
    private val dao = app.db.dao()

    /** The settings generation this store syncs for. After a switch in Settings its reads and writes are dropped. */
    private val gen = app.configGen
    private inline fun ifMine(f: () -> Unit) = synchronized(app.configLock) { if (gen == app.configGen) f() }

    override fun lastSeq() = dao.lastSeq() ?: 0

    override fun apply(notifs: Collection<Notif>, seq: Long, quiet: Boolean, again: Boolean, answer: Boolean, shown: Map<String, Long>) = ifMine {
        // Only rows Room kept reach the tray, so an older event can never undo a newer state there.
        // A row fetched outside the stream (seq 0, such as a queued action's answer) is not filtered by the server:
        // one from a topic unsubscribed meanwhile must not come back after the reset dropped it (#617).
        val subs = app.subs.value
        val (keep, out) = if (seq == 0L && subs?.filtered == true) notifs.partition { subs.subscribed(it.topic) } else notifs.toList() to emptyList<Notif>()
        if (out.isNotEmpty()) app.outside.value++
        val saved = dao.applyAll(keep, seq, answer)
        // #500: a live repeat Room dropped (our own mark's answer got in first) still rings the newer row, if that one shows unacked.
        // Only when the newer row differs by marks alone (#504): after a Clear and Undo it is a new state, and an Undo stays quiet.
        // And never when the repeat is older than this phone's own state change: when it made every change the rows can match (#550).
        val kept = if (!again) saved else saved + notifs.filter { n -> saved.none { it.id == n.id } && n.seq >= (dao.answerOf(n.id) ?: 0) }.mapNotNull { n ->
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
                    // #589: a show after a hide in the replay rings like a new one, unless it is this phone's own change.
                    else -> step.copy(again = !step.silent && (again || (shown[n.id] ?: 0) > (dao.answerOf(n.id) ?: 0)))
                },
            )
        }
        app.pruneCleared()
    }

    override fun live(notifs: Collection<Notif>, arrive: Boolean) = ifMine { app.moving(notifs.map { it.id to Moved(0, arrive, it.state, it.stateBy) }) }

    override fun replaceAll(open: List<Notif>, seq: Long, epoch: String) = ifMine {
        val waiting = waiting()
        val sameDb = epoch.isEmpty() || app.epoch.isEmpty() || epoch == app.epoch
        app.tray.reconcile(dao.replaceAll(open, seq, sameDb).filter { it.id !in waiting }, app.muted)
        if (epoch.isNotEmpty()) app.epoch = epoch
        app.prefs.edit().remove(FOLLOW_RELOAD).apply() // what it holds is the followed set now (#764)
        app.outside.value++
    }

    override fun epoch() = synchronized(app.configLock) { if (gen == app.configGen) app.epoch else "" } // another server's epoch says nothing here
    override fun saveEpoch(epoch: String) = ifMine { app.epoch = epoch }
    /** A settings message, kept only while this is still the server in Settings: one already in hand at a switch would undo the reset (#568). */
    fun settings(o: org.json.JSONObject): Boolean {
        var reload = false
        ifMine {
            val was = app.subs.value?.filtered == true
            val fmt = app.formatting.value
            app.saveSettings(o)
            if (fmt != app.formatting.value) app.tray.reconcile(dao.openNow().filter { it.id !in waiting() }, app.muted) // #809: quietly, in the new look
            val now = app.subs.value?.filtered == true
            if (!was && now) app.prefs.edit().putBoolean(FOLLOW_RELOAD, true).apply()
            reload = now && app.prefs.getBoolean(FOLLOW_RELOAD, false)
        }
        // Now following a set it did not before (#764): what this phone holds may include topics it no longer
        // shows. The mark stays until a reload lands, so a failed one is tried again on the next connect.
        return reload
    }
    override fun saveMarkId(id: String) = ifMine { if (app.markId.value != id) { app.prefs.edit().putString("mark_id", id).apply(); app.markId.value = id } }

    override fun pending() = synchronized(app.configLock) { if (gen == app.configGen) dao.pending() else emptyList() } // never send the new server's queue to the old one
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

/** A topic_previews object as topic to on or off; a topic without a boolean has no choice of its own. */
internal fun onOff(o: org.json.JSONObject?): Map<String, Boolean> =
    o?.keys()?.asSequence()?.mapNotNull { k -> (o.opt(k) as? Boolean)?.let { k to it } }?.toMap().orEmpty()

/** True when [at] is the only time in [times] within [App.LONE_MS] either side: a lone swipe, not a burst (#271). */
internal fun lone(times: List<Long>, at: Long) = times.count { kotlin.math.abs(it - at) <= App.LONE_MS } == 1
