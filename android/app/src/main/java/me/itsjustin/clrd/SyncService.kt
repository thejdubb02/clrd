package me.itsjustin.clrd

import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.os.Build
import android.os.IBinder
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.RemoteInput
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import org.json.JSONObject

/** Foreground service that keeps the WebSocket open (docs/SPEC.md section 8). */
class SyncService : Service() {
    private val app get() = App.of(this)
    private var netCallback: ConnectivityManager.NetworkCallback? = null
    private var lastNet: Network? = null // touched only on the callback thread

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val all = app.accounts
        // Called again on every app launch: an already running sync keeps its real status, not "Connecting".
        all.forEach { if (it.sync == null) it.status.value = SyncClient.Status.CONNECTING }
        foreground()
        // One connection per server (#78). Store and credentials are read together, so a Settings switch cannot
        // pair one server's store with another's API.
        val ready = all.mapNotNull { a -> synchronized(a.configLock) { if (a.configured) Triple(a, RoomStore(app, a), a.api()) else null } }
        if (ready.isEmpty()) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (intent?.action == ACTION_RESTART) all.forEach { a -> a.sync?.stop()?.also { a.sync = null } }
        for ((a, store, api) in ready) synchronized(a.configLock) { // a server removed meanwhile gets no client
            if (a.sync != null || a.removed) return@synchronized
            app.io.execute { store.reconcile() } // the tray is empty after a reboot; refill it quietly
            // The API's own client: a certificate pick between two reads must not split them (#887).
            a.sync = SyncClient(api.http, api, store, onStatus = { status(a, it) }, onSettings = store::settings, onDevices = { app.devicesChanged.value++ }).also { it.start() }
        }
        if (netCallback == null) {
            netCallback = object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) {
                    val moved = lastNet != null && lastNet != network // the socket may still sit on the old network
                    lastNet = network
                    app.accounts.forEach { it.sync?.kick(force = moved) }
                }
            }.also { getSystemService(ConnectivityManager::class.java).registerDefaultNetworkCallback(it) }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        app.accounts.forEach {
            it.sync?.stop()
            it.sync = null
        }
        netCallback?.let { getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(it) }
        app.accounts.forEach { it.status.value = SyncClient.Status.OFFLINE }
        super.onDestroy()
    }

    private fun status(a: Account, s: SyncClient.Status) {
        a.status.value = s
        foreground()
    }

    /** Locked, and reads the status inside the lock, so a status change racing an app launch cannot be overwritten by an older one. */
    @Synchronized private fun foreground() {
        val type = if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0
        // One server reads as before; several say how many are in sync, or the worst status among them (#78).
        val on = app.accounts.filter { it.configured }.ifEmpty { listOf(app.account) }
        val worst = WORST.firstOrNull { s -> on.any { it.status.value == s } } ?: SyncClient.Status.CONNECTED
        ServiceCompat.startForeground(this, 1, notice(this, if (on.size == 1) on[0].status.value else worst, on.size), type)
    }

    companion object {
        const val ACTION_RESTART = "me.itsjustin.clrd.RESTART"
        private val WORST = listOf(SyncClient.Status.AUTH_FAILED, SyncClient.Status.OFFLINE, SyncClient.Status.BUSY, SyncClient.Status.CONNECTING)

        /**
         * The always-on sync notice. Its own group keeps it out of the bundle Android makes from an app's
         * ungrouped notifications, so alerts are never collected under it and never show twice.
         */
        fun notice(ctx: Context, s: SyncClient.Status, servers: Int = 1) =
            NotificationCompat.Builder(ctx, Tray.SERVICE_CHANNEL)
                .setSmallIcon(R.drawable.ic_stat_clrd)
                .setContentTitle(ctx.getString(R.string.app_name))
                .setContentText(
                    if (servers > 1 && s == SyncClient.Status.CONNECTED) ctx.getString(R.string.service_servers, servers) else ctx.getString(
                        when (s) {
                            SyncClient.Status.CONNECTING -> R.string.status_connecting
                            SyncClient.Status.CONNECTED -> R.string.status_in_sync
                            SyncClient.Status.OFFLINE -> R.string.service_offline
                            SyncClient.Status.BUSY -> R.string.service_busy
                            SyncClient.Status.AUTH_FAILED -> R.string.service_token_rejected
                        },
                    ),
                )
                .setGroup(Tray.SERVICE_CHANNEL)
                .setOngoing(true)
                .setSilent(true)
                .setContentIntent(PendingIntent.getActivity(ctx, 0, Intent(ctx, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE))
                .build()

        /** Start (or restart after a settings change). Safe to call from anywhere; background start limits are logged, not thrown. */
        fun start(ctx: Context, restart: Boolean = false) {
            val i = Intent(ctx, SyncService::class.java)
            if (restart) i.action = ACTION_RESTART
            try {
                ContextCompat.startForegroundService(ctx, i)
            } catch (e: IllegalStateException) {
                Log.w("clrd", "could not start sync service now", e)
            }
        }
    }
}

/** Starts sync after a reboot or an app update. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        if (App.of(ctx).anyConfigured) SyncService.start(ctx)
    }
}

/** Ack / Snooze (the default time) / Clear / reply buttons and the swipe-away (delete intent) on a tray notification. */
class ActionReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        val server = intent.getStringExtra(Tray.EXTRA_SERVER) // null on items posted before it was added: the current server
        val app = App.of(ctx)
        // The server it came from (#78), in the data URI; none is account 0. A removed or unknown one is ignored.
        val acct = (intent.data?.getQueryParameter(Tray.QUERY_ACCOUNT) ?: "0").toIntOrNull()?.let(app::account) ?: return
        // Posted for an earlier sign-in at this address (#860): never acts for the new one. Checked again under the lock in actAll.
        val gen = intent.data?.getQueryParameter(Tray.QUERY_GEN)?.toIntOrNull() ?: 0
        if (gen != acct.configGen) return
        if (intent.action == Tray.ACTION_DISMISS_STACK) { // a stack swiped away: hide here only (#265)
            val ids = intent.getStringArrayExtra(Tray.EXTRA_IDS).orEmpty()
            if (!acct.hide(ids.toList(), gen)) return
            app.leftShade(maxOf(ids.size, 2)) // a stack is never a lone swipe, and its rows only hide
            return
        }
        val id = intent.getStringExtra(Tray.EXTRA_ID) ?: return
        if (intent.action == Tray.ACTION_DISMISS) { // a swipe or Clear all: hide here only (#265), unless a lone swipe clears (#271)
            if (!acct.hide(listOf(id), gen)) return
            app.leftShade(1)
            if (app.shadeClears) app.swiped(id, server, acct, gen)
            return
        }
        if (intent.action == Tray.ACTION_DEVICE) { // a sender's copy, HTTP or broadcast button
            val r = goAsync()
            DeviceAction.run(ctx, acct, id, intent.getIntExtra(Tray.EXTRA_INDEX, -1), intent.getLongExtra(Tray.EXTRA_SEQ, -1), server, gen = gen) { r.finish() }
            return
        }
        if (intent.action == Tray.ACTION_REPLY && intent.data?.getQueryParameter("text") == "1") { // typed in the tray (#153)
            val typed = RemoteInput.getResultsFromIntent(intent)?.getCharSequence(Tray.EXTRA_TEXT)?.toString().orEmpty()
            val body = TrayButton.typed(intent.getStringExtra(Tray.EXTRA_REPLY) ?: return, typed, intent.getLongExtra(Tray.EXTRA_SEQ, 0))
            val r = goAsync()
            // Android shows a spinner until the row is posted again: once the answer is saved, or at once if it cannot be sent.
            val repost = { acct.tray.repost(id, gen); r.finish() }
            if (body != null) acct.actAll(listOf(id), "reply", body, server, App.VIA_TRAY_BUTTON, gen = gen, done = repost)
            else app.acts.execute {
                android.os.Handler(android.os.Looper.getMainLooper()).post { android.widget.Toast.makeText(app, R.string.reply_text_invalid, android.widget.Toast.LENGTH_LONG).show() }
                repost()
            }
            return
        }
        val (action, body) = when (intent.action) {
            Tray.ACTION_ACK -> "ack" to "{}"
            Tray.ACTION_SNOOZE -> "snooze" to Snooze.body(acct.snooze.value.default)
            Tray.ACTION_CLEAR -> "dismiss" to "{}"
            // seq = the version the person saw: if the question changed since, the server answers 409 and the tap is dropped
            Tray.ACTION_REPLY -> "reply" to JSONObject().put("reply", intent.getStringExtra(Tray.EXTRA_REPLY) ?: return)
                .put("seq", intent.getLongExtra(Tray.EXTRA_SEQ, 0)).toString()
            else -> return
        }
        val r = goAsync() // keep the process alive until the action is saved (and probably sent)
        acct.actAll(listOf(id), action, body, server, App.VIA_TRAY_BUTTON, gen = gen) { r.finish() }
    }
}
