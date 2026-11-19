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
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat

/** Foreground service that keeps the WebSocket open (docs/SPEC.md section 8). */
class SyncService : Service() {
    private val app get() = App.of(this)
    private var netCallback: ConnectivityManager.NetworkCallback? = null
    private var lastNet: Network? = null // touched only on the callback thread

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        foreground(R.string.status_connecting)
        // Store and credentials are read together, so a Settings switch cannot pair one server's store with another's API.
        val (store, api) = synchronized(app.configLock) { if (app.configured) RoomStore(app) to app.api() else null to null }
        if (store == null || api == null) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (intent?.action == ACTION_RESTART) app.sync?.stop()?.also { app.sync = null }
        if (app.sync == null) {
            app.io.execute { store.reconcile() } // the tray is empty after a reboot; refill it quietly
            app.sync = SyncClient(app.http, api, store, onStatus = ::status).also { it.start() }
        }
        if (netCallback == null) {
            netCallback = object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) {
                    val moved = lastNet != null && lastNet != network // the socket may still sit on the old network
                    lastNet = network
                    app.sync?.kick(force = moved)
                }
            }.also { getSystemService(ConnectivityManager::class.java).registerDefaultNetworkCallback(it) }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        app.sync?.stop()
        app.sync = null
        netCallback?.let { getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(it) }
        app.status.value = SyncClient.Status.OFFLINE
        super.onDestroy()
    }

    private fun status(s: SyncClient.Status) {
        app.status.value = s
        foreground(
            when (s) {
                SyncClient.Status.CONNECTING -> R.string.status_connecting
                SyncClient.Status.CONNECTED -> R.string.status_in_sync
                SyncClient.Status.OFFLINE -> R.string.service_offline
                SyncClient.Status.BUSY -> R.string.service_busy
                SyncClient.Status.AUTH_FAILED -> R.string.service_token_rejected
            },
        )
    }

    private fun foreground(text: Int) {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val n = NotificationCompat.Builder(this, Tray.SERVICE_CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_clrd)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(getString(text))
            .setOngoing(true)
            .setSilent(true)
            .setContentIntent(open)
            .build()
        val type = if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0
        ServiceCompat.startForeground(this, 1, n, type)
    }

    companion object {
        const val ACTION_RESTART = "me.itsjustin.clrd.RESTART"

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
        if (App.of(ctx).configured) SyncService.start(ctx)
    }
}

/** Ack / Snooze 1h / Clear buttons and the swipe-away (delete intent) on a tray notification. */
class ActionReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        val id = intent.getStringExtra(Tray.EXTRA_ID) ?: return
        val server = intent.getStringExtra(Tray.EXTRA_SERVER) ?: return
        val (action, body) = when (intent.action) {
            Tray.ACTION_ACK -> "ack" to "{}"
            Tray.ACTION_SNOOZE -> "snooze" to """{"for":"1h"}"""
            Tray.ACTION_DISMISS -> "dismiss" to "{}"
            else -> return
        }
        val r = goAsync() // keep the process alive until the action is saved (and probably sent)
        App.of(ctx).act(id, action, body, server) { r.finish() }
    }
}
