package me.itsjustin.clrd

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat

/** What the phone tray should do for one notification. Pure, so it is unit tested on the JVM. */
sealed interface TrayStep {
    data class Show(val n: Notif, val silent: Boolean) : TrayStep
    data class Cancel(val id: String) : TrayStep

    companion object {
        /**
         * active: show and alert. acknowledged: keep showing, quietly, with who acked it.
         * snoozed: hide (the server re-activates it later, which shows it again).
         * dismissed / resolved: cancel everywhere. That is the headline feature.
         */
        fun plan(n: Notif, muted: Set<String>): TrayStep = when {
            n.topic in muted -> Cancel(n.id)
            n.state == Notif.ACTIVE -> Show(n, silent = false)
            n.state == Notif.ACKNOWLEDGED -> Show(n, silent = true)
            else -> Cancel(n.id)
        }
    }
}

/** Posts and cancels system notifications. tag = clrd notification id, int id is always 0. */
class Tray(private val ctx: Context) {
    private val nm = ctx.getSystemService(NotificationManager::class.java)

    fun createChannels() {
        nm.createNotificationChannels(
            listOf(
                NotificationChannel("p1", "Min priority", NotificationManager.IMPORTANCE_MIN),
                NotificationChannel("p2", "Low priority", NotificationManager.IMPORTANCE_LOW),
                NotificationChannel("p3", "Default priority", NotificationManager.IMPORTANCE_DEFAULT),
                NotificationChannel("p4", "High priority", NotificationManager.IMPORTANCE_HIGH),
                NotificationChannel("p5", "Urgent", NotificationManager.IMPORTANCE_HIGH),
                NotificationChannel(SERVICE_CHANNEL, "Sync connection", NotificationManager.IMPORTANCE_MIN),
            ),
        )
    }

    fun apply(step: TrayStep) {
        when (step) {
            is TrayStep.Cancel -> nm.cancel(step.id, 0)
            is TrayStep.Show -> post(step.n, step.silent)
        }
    }

    /**
     * Make the tray match the open rows: post what is missing, cancel what should be gone.
     * Android caps an app at about 50 notifications.
     * ponytail: newest 40 only; a "N more" summary if people hit it.
     */
    fun reconcile(rows: List<Notif>, muted: Set<String>) {
        val want = rows.map { TrayStep.plan(it, muted) }.filterIsInstance<TrayStep.Show>()
            .sortedByDescending { it.n.createdAt }.take(40)
        val wantIds = want.map { it.n.id }.toSet()
        val showing = nm.activeNotifications.filter { it.tag != null }.map { it.tag }.toSet()
        showing.filter { it !in wantIds }.forEach { nm.cancel(it, 0) }
        want.forEach { post(it.n, silent = true) } // reposting a showing one updates it (state, buttons) without a sound
    }

    private fun post(n: Notif, silent: Boolean) {
        if (android.os.Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) return
        val b = NotificationCompat.Builder(ctx, "p${n.priority}")
            .setSmallIcon(R.drawable.ic_stat_clrd)
            .setContentTitle(n.title.ifEmpty { n.topic })
            .setContentText(n.message)
            .setStyle(NotificationCompat.BigTextStyle().bigText(n.message))
            .setSubText(if (n.state == Notif.ACKNOWLEDGED) "Acknowledged on ${n.stateBy ?: "another device"}" else n.topic)
            .setWhen(n.createdAt)
            .setShowWhen(true)
            .setPriority(n.priority - 3) // PRIORITY_MIN..MAX for API < 26 builders; channels rule on 26+
            .setOnlyAlertOnce(true) // updates to a showing notification stay quiet
            .setSilent(silent)
            .setAutoCancel(false)
            .setContentIntent(open(n))
            .setDeleteIntent(action(n, ACTION_DISMISS)) // swipe away here = dismiss everywhere
        if (n.priority >= 5) b.setColor(0xFFC2410C.toInt())
        if (n.state != Notif.ACKNOWLEDGED) b.addAction(0, "Ack", action(n, ACTION_ACK))
        b.addAction(0, "Snooze 1h", action(n, ACTION_SNOOZE))
        b.addAction(0, "Clear", action(n, ACTION_DISMISS))
        nm.notify(n.id, 0, b.build())
    }

    private fun open(n: Notif): PendingIntent {
        val i = n.click?.takeIf { it.startsWith("http") }?.let { Intent(Intent.ACTION_VIEW, Uri.parse(it)) }
            ?: Intent(ctx, MainActivity::class.java).setFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        return PendingIntent.getActivity(ctx, n.id.hashCode(), i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    private fun action(n: Notif, action: String): PendingIntent {
        val server = App.of(ctx).serverUrl // a tap after a server switch must not reach the new server
        val i = Intent(ctx, ActionReceiver::class.java)
            .setAction(action)
            // unique per (server, id, action) so PendingIntents do not collide or take over each other's extras
            .setData(Uri.parse("clrd://n/${n.id}/$action").buildUpon().appendQueryParameter("server", server).build())
            .putExtra(EXTRA_ID, n.id)
            .putExtra(EXTRA_SERVER, server)
        return PendingIntent.getBroadcast(ctx, 0, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    companion object {
        const val SERVICE_CHANNEL = "service"
        const val ACTION_ACK = "me.itsjustin.clrd.ACK"
        const val ACTION_SNOOZE = "me.itsjustin.clrd.SNOOZE"
        const val ACTION_DISMISS = "me.itsjustin.clrd.DISMISS"
        const val EXTRA_ID = "id"
        const val EXTRA_SERVER = "server"
    }
}
