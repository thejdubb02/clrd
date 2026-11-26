package me.itsjustin.clrd

import android.content.Context
import android.content.SharedPreferences
import androidx.room.Room
import kotlinx.coroutines.flow.MutableStateFlow
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import org.json.JSONObject

/**
 * One server this phone talks to (#78): its address, token, settings file and database, and the state that
 * comes from them (settings, sync status, the sync client). Account 0 is the install as it was before there
 * could be more than one: the "clrd" file and "clrd.db", so an update reads them as they are, with no copy.
 * Choices that belong to the phone stay in [App.prefs].
 */
class Account(private val ctx: Context, val id: Int) {
    private val name = if (id == 0) "clrd" else "clrd-$id"
    val prefs: SharedPreferences = ctx.getSharedPreferences(name, Context.MODE_PRIVATE)
    private val dbOnce = lazy { Room.databaseBuilder(ctx, Db::class.java, "$name.db").build() }
    val db by dbOnce
    private val app get() = App.of(ctx)

    /** This server's tray entries. Account 0 posts as before #78; the others tag theirs with the account id. */
    val tray by lazy { Tray(ctx, this) }

    /** The server origin the certificate was picked for. A moved server gets no certificate until it is picked again. */
    private fun origin(u: okhttp3.HttpUrl) = "${u.scheme}://${u.host}:${u.port}"
    /** This server's origin now, which a pick is made for. */
    fun certOrigin() = serverUrl.toHttpUrlOrNull()?.let(::origin)
    /** The picked alias and the origin it was picked for, as one value, so a handshake never pairs one pick's alias with another's origin. */
    @Volatile private var certPick: Pair<String?, String?> = prefs.getString("client_cert", null) to prefs.getString("client_cert_origin", null)
    /** The alias a handshake may offer and the host and port it is for, or null: none picked, or the server moved since. */
    internal fun certOffer(): Pair<String, Pair<String, Int>>? {
        val (alias, at) = certPick
        val u = serverUrl.toHttpUrlOrNull()
        return if (alias != null && u != null && origin(u) == at) alias to (u.host to u.port) else null
    }
    private val certKeys by lazy {
        ClientCertKeys(::certOffer) { a ->
            runCatching { // a removed key or revoked access means no certificate, not a crash
                val k = android.security.KeyChain.getPrivateKey(ctx, a)
                val c = android.security.KeyChain.getCertificateChain(ctx, a)
                if (k != null && c != null) k to c else null
            }.getOrNull()
        }
    }

    /**
     * This server's own client (#78): its own client certificate (#152) and its own connection pool, so no
     * server ever gets another's certificate or reuses a connection that sent it.
     */
    val http by lazy {
        okhttp3.OkHttpClient.Builder()
            .sslSocketFactory(certKeys.socketFactory, certKeys.trustManager)
            .pingInterval(45, java.util.concurrent.TimeUnit.SECONDS) // notices a dead connection the server ping alone would not
            .connectTimeout(15, java.util.concurrent.TimeUnit.SECONDS)
            .build()
    }

    /**
     * Picks or removes this server's client certificate (#152) for origin [at], where the picker was opened. A pick
     * for an address the server has since left is dropped. Open connections used the old one, so they close and sync reconnects.
     */
    fun pickClientCert(alias: String?, at: String? = certOrigin()) {
        if (at != certOrigin()) return
        certPick = alias to at
        prefs.edit().putString("client_cert", alias).putString("client_cert_origin", at).apply()
        clientCert.value = alias
        app.io.execute {
            certKeys.renew()
            http.connectionPool.evictAll()
            if (configured) SyncService.start(app, restart = true)
        }
    }

    private companion object { const val CONFIG_GEN = "config_gen" }

    /** Forgets the pick when account 0 is emptied; its file loses the keys in the same removal. */
    internal fun dropClientCert() {
        certPick = null to null
        clientCert.value = null
    }

    /** Set by [App.removeAccount]. A store, tap or redraw still in flight for it then changes nothing. */
    @Volatile var removed = false

    /** Closes the database if it was opened; for [App.removeAccount], before the file goes. */
    internal fun close() { if (dbOnce.isInitialized()) db.close() }

    var serverUrl: String
        get() = prefs.getString("url", "").orEmpty()
        set(v) = prefs.edit().putString("url", v.trim().trimEnd('/')).apply()
    var token: String
        get() = prefs.getString("token", "").orEmpty()
        set(v) = prefs.edit().putString("token", v.trim()).apply()

    /**
     * The server database the saved seq belongs to. Saved after the rows, not with them: a crash
     * in between only means one more reset on the next connect.
     */
    var epoch: String
        get() = prefs.getString("epoch", "").orEmpty()
        set(v) = prefs.edit().putString("epoch", v).apply()

    val configured get() = serverUrl.isNotEmpty() && token.isNotEmpty()

    /** The address without its scheme, as the switcher, the Servers list and tray entries name this server. */
    val host get() = serverUrl.substringAfter("://")

    /** Address and token as one pair, read under the lock [saveServer] writes them under, so a token never meets another address. */
    fun creds() = synchronized(configLock) { serverUrl to token }
    fun api() = creds().let { (u, t) -> Api(http, u, t) }

    /** The Send box's send in flight, kept here so a rotation does not forget it (#523), and one per server (#78). */
    val sender by lazy { Sender { app.io.execute(it) } }

    /** Typed answers this server refused, by notification id, so the reply box opens with them again (#153); per server, as ids are (#860). */
    val drafts = java.util.concurrent.ConcurrentHashMap<String, String>()

    /** Held while this server's queue is sent (#860): one sender per server, so another server's slow send never waits on it. */
    internal val sendLock = Any()

    /** Client certificate alias picked in Settings (#152), or none. Offered only to the server's own host. */
    val clientCert by lazy { MutableStateFlow(prefs.getString("client_cert", null)) }

    val status = MutableStateFlow(SyncClient.Status.OFFLINE)

    /** Live sync client while the service runs; null otherwise. */
    @Volatile var sync: SyncClient? = null

    /**
     * Held for every change that writes Room and then the tray (sync, user actions, reconcile) and while
     * Settings switches server. Without it a slower writer can put back a tray item a faster one just cleared.
     */
    val configLock = Any()

    /**
     * Bumped on every server or token switch. A store made before the bump is fenced off. Saved, as tray taps carry it
     * (#860): one from before a new token at the same address is refused, after a restart too.
     */
    @Volatile var configGen = prefs.getInt(CONFIG_GEN, 0)

    private var sendNotBefore = 0L // System.nanoTime(); a busy server's Retry-After, touched only on [App.sends]
    private var sendNotBeforeGen = 0 // the settings it came from; another server is not busy

    var muted: Set<String>
        get() = prefs.getStringSet("muted", emptySet()).orEmpty()
        set(v) = prefs.edit().putStringSet("muted", v).apply()

    /**
     * Rows swiped out of the shade (#265). A swipe, a stack swipe or Clear all only hides them on this
     * phone; clearing happens in the app, the web or the API. A new alert for one shows it again.
     */
    var trayHidden: Set<String>
        get() = prefs.getStringSet("tray_hidden", emptySet()).orEmpty()
        set(v) = prefs.edit().putStringSet("tray_hidden", v).apply()

    /** This phone's notification sound (docs/SPEC.md section 2.2). Unset means the phone's own default. Change it with [saveSound]. */
    val sound: String get() = Sounds.known(prefs.getString("sound", null))

    /**
     * Switches the sound: new channels first, then the choice, then the old channels go and the tray is
     * reposted quietly onto the new ones (a post to a missing channel is dropped). Call on [App.io].
     * True when the change took the Urgent channel's Do Not Disturb pass with it.
     */
    fun saveSound(name: String): Boolean = synchronized(configLock) {
        val s = Sounds.known(name)
        // Under the phone-wide channel lock (#860): another server's change must not read the old sound while the new channels exist.
        val dropped = synchronized(app) { tray.createChannels(s).also { prefs.edit().putString("sound", s).commit() } }
        soundNow.value = s
        RoomStore(app, this).reconcile()
        dropped
    }

    /** [sound] for Settings to follow, so a change from the server or an agent shows while it is open. */
    val soundNow by lazy { MutableStateFlow(sound) }

    /**
     * A choice made on this phone that the server does not have yet (refused, offline, or made before #289).
     * It wins over the server's until the server takes it, the same as the web inbox.
     */
    val soundPending: String? get() = prefs.getString("sound_pending", null)

    /** The person picked [name] in Settings: play it here at once, then tell the server. Call on [App.io]. Returns [pushSound]'s answer. */
    fun pickSound(name: String): String? {
        prefs.edit().putString("sound_pending", Sounds.known(name)).commit() // first, so a crash after it cannot let the server's old choice win
        saveSound(name)
        return pushSound()
    }

    /**
     * Sends [soundPending] to the server (docs/SPEC.md section 2.2), one at a time on [App.io].
     * Null when the server has it (or nothing waits), else why it stays on this phone only.
     */
    fun pushSound(): String? {
        val s = soundPending ?: return null
        if (!configured) return app.getString(R.string.sound_phone_only, app.getString(R.string.sound_no_server))
        return try {
            api().put("/api/v1/settings", JSONObject().put("sound", s).toString())
            if (soundPending == s) prefs.edit().remove("sound_pending").commit()
            null
        } catch (e: Exception) { // a bad saved address throws IllegalArgumentException, not IOException
            app.getString(R.string.sound_phone_only, (e as? HttpError)?.message ?: app.getString(R.string.sound_no_connection))
        }
    }

    /**
     * The server's sound for this phone, from a settings message. Applied on the sync thread before the
     * next event, so a notification sent right after the change plays the new sound. A pending choice is
     * sent instead of being overwritten.
     */
    private fun serverSound(name: String) {
        if (soundPending != null) app.io.execute { pushSound() }
        else if (Sounds.known(name) != sound && saveSound(name)) tray.dndNotice() // not picked here, so Settings' note may go unseen (#380)
    }

    /** A sound picked before the server kept it (#282) goes to the server once, on the next connect. */
    internal fun moveSound() {
        if (!prefs.getBoolean("sound_moved", false)) {
            prefs.edit().apply { if (sound != Sounds.DEFAULT) putString("sound_pending", sound) }.putBoolean("sound_moved", true).apply()
        }
    }

    /** The shared snooze times, as the server last sent them (every connect sends them). */
    val snooze by lazy { MutableStateFlow(prefs.getString("snooze", null)?.let { runCatching { Snooze.from(JSONObject(it)) }.getOrNull() } ?: Snooze()) }

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
    val topicPreviews by lazy { MutableStateFlow(onOff(prefs.getString("topic_previews", null)?.let { runCatching { JSONObject(it) }.getOrNull() })) }

    /** The four display switches by their server names, each with its out-of-the-box value. */
    private val display get() = mapOf("render_formatting" to (formatting to true), "autolink" to (autolink to true), "favicons" to (favicons to true), "link_previews" to (linkPreviews to false))

    /** This token got 403 changing [swipes] or a display switch, so Settings shows them read-only. In memory: a token's grants can widen. */
    val swipesLocked = MutableStateFlow(false)

    /** Goes up on every settings message and every save the server took (#818), so an open preview asks again after a switch. */
    val settingsRev = MutableStateFlow(0)

    /**
     * Sends [next] from Settings (#161). It shows at once; the server's settings message confirms it to every device.
     * Null when the server took it or the token may not change it ([swipesLocked]), else why not. Call on [App.io].
     */
    fun saveSwipes(next: Swipes, api: Api = api()): String? {
        // On success the server has it: a restart before its echo keeps it.
        return putSettings(api, next.json(), { swipes.value = next }, { prefs.edit().putString("swipes", next.json()).apply() }) { swipes.value = Swipes.parse(prefs.getString("swipes", null)) }
    }

    /** Sends one display switch from Settings (#107), like [saveSwipes]. A formatting change redraws the shade at once, quietly. Call on [App.io]. */
    fun saveDisplay(key: String, on: Boolean, api: Api = api()): String? {
        val (flow, default) = display.getValue(key)
        fun set(v: Boolean) { if (flow.value != v) { flow.value = v; if (key == "render_formatting") RoomStore(app, this).reconcile() } }
        return putSettings(api, JSONObject().put(key, on).toString(), { set(on) }, { prefs.edit().putBoolean(key, on).apply() }) { set(prefs.getBoolean(key, default)) }
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
        if (e is HttpError && e.code == 403) { swipesLocked.value = true; null } else (e as? HttpError)?.message ?: app.getString(R.string.sound_no_connection)
    }

    /** This account's topic subscriptions (#617), as the server last sent them; null from a server without them. */
    internal val subs by lazy { MutableStateFlow(Subs.of(prefs.getString("subs", null)?.let { runCatching { JSONObject(it) }.getOrNull() })) }

    /** `GET /api/v1/subscriptions`; null from a server without the route. Call on [App.io]. */
    internal fun loadSubs(api: Api = api()): SubsList? = try {
        SubsList.from(JSONObject(api.get("/api/v1/subscriptions")))
    } catch (e: HttpError) { if (e.code == 404) null else throw e }

    /**
     * Sends only the fields in [body]; the reply is the new set. The server then sends every device of this
     * account its settings and a reset, which drops or brings back the topic's cards and tray entries. Call on [App.io].
     */
    internal fun putSubs(body: JSONObject, api: Api = api()): SubsList =
        SubsList.from(JSONObject(api.put("/api/v1/subscriptions", body.toString())))

    /** Subscribes this account to [topic] or not, on every device. Null when the server took it, else why not. Call on [App.io]. */
    fun subscribe(topic: String, on: Boolean, api: Api = api()): String? = try {
        putSubs(topicBody(topic, on), api); null
    } catch (e: Exception) { (e as? HttpError)?.message ?: app.getString(R.string.sound_no_connection) }

    /** The `by_id` this phone's marks get on this server, "" until a `ready` says (#451). */
    val markId by lazy { MutableStateFlow(prefs.getString("mark_id", "").orEmpty()) }

    /**
     * Sets this phone's mark on a card, or removes it when [kind] is null (#451). Sent now, not queued: a mark
     * changes no state, and the person sees at once why one was refused. [done] gets null or the reason, on the main thread.
     */
    fun mark(id: String, kind: String?, note: String, link: String, done: (String?) -> Unit) {
        val (store, api) = synchronized(configLock) { RoomStore(app, this) to api() }
        app.io.execute {
            val err = try {
                val path = "/api/v1/notifications/$id/mark"
                val out = if (kind == null) api.delete(path)
                else api.post(path, JSONObject().put("mark", kind).put("note", note).put("link", link).toString())
                store.apply(listOf(Notif.fromJson(JSONObject(out))), 0, quiet = true) // the card shows it now, the stream's copy changes nothing
                null
            } catch (e: Exception) { // a bad saved address throws IllegalArgumentException, not IOException
                (e as? HttpError)?.message ?: app.getString(R.string.mark_no_connection)
            }
            android.os.Handler(app.mainLooper).post { done(err) }
        }
    }

    /** Keeps a server `settings` message. The sync client calls it through [RoomStore.settings], so an old server's message is dropped. */
    fun saveSettings(o: JSONObject) {
        settingsRev.value++
        if (o.has("sound")) serverSound(o.optString("sound"))
        if (o.has("keep_cleared_days")) { // the server's keep for cleared rows (#261): drop what it deleted
            val keep = JSONObject().put("days", o.optInt("keep_cleared_days", App.KEEP_DAYS))
                .put("topics", o.optJSONObject("topic_keep_cleared_days") ?: JSONObject())
            prefs.edit().putString("keep", keep.toString()).apply()
            app.io.execute { pruneCleared() }
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
     * Drops cleared rows older than the server keeps them, and never more than [App.KEEP_DAYS] on this phone.
     * A topic's own keep wins over the global one; 0 means the server keeps them forever.
     */
    fun pruneCleared(now: Long = System.currentTimeMillis()) {
        val k = runCatching { JSONObject(prefs.getString("keep", null) ?: "{}") }.getOrDefault(JSONObject())
        fun before(days: Int) = now - (if (days in 1..App.KEEP_DAYS) days else App.KEEP_DAYS) * 24 * 3600_000L
        val topics = k.optJSONObject("topics") ?: JSONObject()
        val own = topics.keys().asSequence().toList()
        val dao = db.dao()
        if (app.dropCleared) dao.dropCleared() else {
            dao.prune(before(k.optInt("days", App.KEEP_DAYS)), own)
            own.forEach { dao.pruneTopic(it, before(topics.optInt(it, App.KEEP_DAYS))) }
        }
        dao.pruneAnswers()
    }

    /** Saves the server and token from Settings or a scanned link. Call on [App.io]. */
    fun saveServer(newUrl: String, newToken: String) {
        if (newUrl != serverUrl || newToken != token) {
            // Old server's rows, cursor and queue must not reach the new one. Blanking the token
            // and bumping configGen fence the old client (RoomStore.ifMine) and keeps the service from starting
            // one; a crash before the final save leaves the app unconfigured, never mixed.
            synchronized(configLock) { configGen++; prefs.edit().putInt(CONFIG_GEN, configGen).apply(); token = "" }
            drafts.clear() // the old server's typed answers, keyed by its ids (#860)
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
        } else synchronized(configLock) {
            serverUrl = newUrl
            token = newToken
        }
        app.saved.value++
    }

    /**
     * A user action from the tray or the inbox; see [App.act]. Saved first, shown locally at once, then sent.
     * One for a server that is no longer configured, or that waited across a Settings switch, is dropped, and so is
     * a tray tap whose [gen] (the sign-in it was posted for) is not this one.
     */
    internal fun actAll(ids: List<String>, action: String, rawBody: String, server: String?, via: String, gen: Int = configGen, done: () -> Unit) {
        val json = JSONObject(rawBody)
        val until = if (action == "snooze" || action == "reopen") snoozeEnd(json) else null
        // #508: a snooze sent late (offline) ends when this phone and its Undo say; see snoozeWire
        if (action == "snooze" && until != null) json.put(ENDS, until)
        val body = json.put("via", via).toString()
        app.acts.execute {
            try {
                val local = when (action) {
                    "dismiss" -> Notif.DISMISSED
                    "snooze" -> Notif.SNOOZED
                    "ack", "reply" -> Notif.ACKNOWLEDGED
                    "reopen" -> if (until != null) Notif.SNOOZED else Notif.ACTIVE
                    else -> null
                }
                synchronized(configLock) {
                    if (gen != configGen || removed || (server != null && server != serverUrl)) return@execute
                    val dao = db.dao()
                    // One transaction: a stack swipe is saved whole or not at all, never half.
                    val saved = db.runInTransaction<List<String>> {
                        // A stack swipe may also fire each row's own delete intent: those rows are cleared already.
                        ids.filterNot { action == "dismiss" && dao.get(it)?.state == Notif.DISMISSED }.onEach { id ->
                            if (local != null) app.moving(listOf(id to Moved(0, false, local, null))) // before Room shows it (#598)
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
            Triple(configGen, RoomStore(app, this), api())
        }
        app.sends.execute {
            if (gen == sendNotBeforeGen && System.nanoTime() < sendNotBefore) return@execute // stays queued for later
            try {
                flushPending(store, api) { configGen == gen }
            } catch (e: Exception) {
                (e as? HttpError)?.retryAfterMs?.let { sendNotBefore = System.nanoTime() + it * 1_000_000; sendNotBeforeGen = gen }
            }
        }
    }
}
