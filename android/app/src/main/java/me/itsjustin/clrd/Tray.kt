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
import org.json.JSONArray
import java.util.concurrent.atomic.AtomicBoolean

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

/** One tray button: a sender link or reply (docs/SPEC.md section 2), or one of ours, by its [Tray] action. */
data class TrayButton(val label: String = "", val res: Int = 0, val url: String? = null, val action: String? = null, val reply: String? = null) {
    companion object {
        val ACK = TrayButton(res = R.string.tray_ack, action = Tray.ACTION_ACK)
        val SNOOZE = TrayButton(res = R.string.tray_snooze, action = Tray.ACTION_SNOOZE)
        val CLEAR = TrayButton(res = R.string.tray_clear, action = Tray.ACTION_CLEAR) // not the delete intent's, so the log tells them apart

        /**
         * Android shows 3 buttons at most. The sender's replies come first (at most 3, until someone answers),
         * then up to 2 sender links (http and https only), then ours fill what is left in this order:
         * Ack (unless acked already), Clear, Snooze.
         * A swipe still clears, so Clear is never the only way out. Pure, so it is unit tested on the JVM.
         */
        fun pick(n: Notif): List<TrayButton> {
            val a = runCatching { JSONArray(n.actions) }.getOrNull() ?: JSONArray()
            val objs = (0 until a.length()).mapNotNull { a.optJSONObject(it) }
            val replies = if (n.reply.isNotEmpty()) emptyList() else objs.mapNotNull { o ->
                o.optString("reply").takeIf { it.isNotEmpty() }?.let { TrayButton(o.optString("label").ifBlank { it }, reply = it) }
            }.take(3)
            val links = objs.mapNotNull { o ->
                val url = o.optString("url")
                // Web links only, and short: a huge URL would not fit through Binder and would crash the post.
                if (url.length > 2048 || (!url.startsWith("https://", true) && !url.startsWith("http://", true))) null
                // Android matches intent schemes case-sensitively, so HTTPS://x must become https://x
                else TrayButton(o.optString("label"), if (o.optString("label").isBlank()) R.string.tray_open else 0, url.substringBefore(':').lowercase() + url.substring(url.indexOf(':')))
            }.take(minOf(2, 3 - replies.size))
            val ours = listOfNotNull(ACK.takeIf { n.state != Notif.ACKNOWLEDGED }, CLEAR, SNOOZE).take(3 - replies.size - links.size)
            return replies + links + listOf(ACK, SNOOZE, CLEAR).filter { it in ours } // ours keep their usual order
        }
    }
}

/** Posts and cancels system notifications. tag = clrd notification id, int id is always 0. */
class Tray(private val ctx: Context) {
    private val nm = ctx.getSystemService(NotificationManager::class.java)

    fun createChannels() {
        nm.createNotificationChannels(
            listOf(
                NotificationChannel("p1", ctx.getString(R.string.channel_p1), NotificationManager.IMPORTANCE_MIN),
                NotificationChannel("p2", ctx.getString(R.string.channel_p2), NotificationManager.IMPORTANCE_LOW),
                NotificationChannel("p3", ctx.getString(R.string.channel_p3), NotificationManager.IMPORTANCE_DEFAULT),
                NotificationChannel("p4", ctx.getString(R.string.channel_p4), NotificationManager.IMPORTANCE_HIGH),
                NotificationChannel("p5", ctx.getString(R.string.channel_p5), NotificationManager.IMPORTANCE_HIGH),
                // No badge: the always-on "in sync" notice must not put a dot on the app icon.
                NotificationChannel(SERVICE_CHANNEL, ctx.getString(R.string.channel_service), NotificationManager.IMPORTANCE_MIN)
                    .apply { setShowBadge(false) },
            ),
        )
        // Android never lets an app change a channel's badge setting after creation, so the old channel is replaced.
        nm.deleteNotificationChannel("service")
    }

    fun apply(step: TrayStep) {
        when (step) {
            is TrayStep.Cancel -> nm.cancel(step.id, 0)
            is TrayStep.Show -> post(step.n, step.silent)
        }
        summaries()
    }

    /**
     * Make the tray match the open rows: post what is missing, cancel what should be gone.
     * Android caps an app at about 50 notifications, stack summaries included (one per 2 or more rows).
     * ponytail: newest 40 only (32 when stacking); a "N more" summary if people hit it.
     */
    fun reconcile(rows: List<Notif>, muted: Set<String>) {
        val open = rows.map { it.id }.toSet()
        App.of(ctx).run { trayHidden = trayHidden.filterTo(HashSet()) { it in open } } // closed rows need no hiding
        val want = rows.map { TrayStep.plan(it, muted) }.filterIsInstance<TrayStep.Show>()
            .sortedByDescending { it.n.createdAt }.take(if (App.of(ctx).stack) 32 else 40)
        val wantIds = want.map { it.n.id }.toSet()
        val showing = nm.activeNotifications.filter { it.tag != null && it.id == 0 }.map { it.tag }.toSet()
        showing.filter { it !in wantIds }.forEach { nm.cancel(it, 0) }
        want.forEach { post(it.n, silent = true) } // reposting a showing one updates it (state, buttons) without a sound
        summaries()
    }

    /**
     * Stack by topic (#108): every topic with 2 or more rows showing gets a group summary, so Android folds
     * them into one stack. Summaries use int id [SUMMARY_ID] and the group key as tag. Rebuilt from what the
     * tray shows after every change. A summary is cancelled only once its stack is empty: Android cancels a
     * summary's children with it, so cancelling it at one row would take that row out of the tray too.
     */
    private fun summaries() {
        val active = nm.activeNotifications
        // Tag check too: the sync notice also uses id 1, with no tag.
        val shown = active.filter { it.id == SUMMARY_ID && it.tag?.startsWith(GROUP) == true }.map { it.tag }.toSet()
        val stacks = active.filter { it.tag != null && it.id == 0 && it.notification.group?.startsWith(GROUP) == true }
            .groupBy { it.notification.group!! }.filter { (g, rows) -> rows.size >= 2 || g in shown }
        shown.filter { it !in stacks.keys }.forEach { nm.cancel(it, SUMMARY_ID) }
        if (stacks.isEmpty()) return
        val server = App.of(ctx).serverUrl
        val main = PendingIntent.getActivity(
            ctx, 0,
            Intent(ctx, MainActivity::class.java).setFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP),
            PendingIntent.FLAG_IMMUTABLE,
        )
        for ((group, rows) in stacks) {
            val topic = group.removePrefix(GROUP)
            val ids = rows.map { it.tag }.toTypedArray()
            // Swiping the stack away dismisses exactly the rows it held, queued like any other swipe (one clear call).
            val delete = Intent(ctx, ActionReceiver::class.java)
                .setAction(ACTION_DISMISS_STACK)
                .setData(Uri.parse("clrd://stack/${Uri.encode(topic)}").buildUpon().appendQueryParameter("server", server).build())
                .putExtra(EXTRA_IDS, ids)
                .putExtra(EXTRA_SERVER, server)
            val b = NotificationCompat.Builder(ctx, rows.maxOf { it.notification.channelId })
                .setSmallIcon(R.drawable.ic_stat_clrd)
                .setContentTitle(topic)
                .setContentText(ctx.resources.getQuantityString(R.plurals.tray_stack, rows.size, rows.size))
                .setSubText(topic)
                .setGroup(group)
                .setGroupSummary(true)
                .setGroupAlertBehavior(NotificationCompat.GROUP_ALERT_CHILDREN) // the rows alert, the summary never does
                .setSilent(true)
                .setContentIntent(main)
                .setDeleteIntent(PendingIntent.getBroadcast(ctx, 0, delete, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
            nm.notify(group, SUMMARY_ID, b.build())
        }
    }

    private fun post(n: Notif, silent: Boolean) {
        if (android.os.Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) return
        val app = App.of(ctx)
        if (n.id in app.trayHidden) { // hidden by a bulk clear on this phone (#265): only a new alert brings it back
            if (silent) return
            app.trayHidden -= n.id
        }
        val text = Md.tray(n.message, n.isMarkdown)
        val b = NotificationCompat.Builder(ctx, "p${n.priority}")
            .setSmallIcon(R.drawable.ic_stat_clrd)
            .setContentTitle(n.title.ifEmpty { n.topic })
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setSubText(
                when {
                    n.reply.isNotEmpty() -> ctx.getString(R.string.tray_answered, n.reply, n.stateBy ?: ctx.getString(R.string.tray_another_device))
                    n.state == Notif.ACKNOWLEDGED -> ctx.getString(R.string.tray_acked, n.stateBy ?: ctx.getString(R.string.tray_another_device))
                    else -> n.topic
                },
            )
            .setWhen(n.createdAt)
            .setShowWhen(true)
            .setPriority(n.priority - 3) // PRIORITY_MIN..MAX for API < 26 builders; channels rule on 26+
            .setOnlyAlertOnce(true) // updates to a showing notification stay quiet
            .setSilent(silent)
            .setAutoCancel(false)
            .setContentIntent(open(n))
            .setDeleteIntent(action(n, ACTION_DISMISS)) // a swipe only hides it on this phone (App.trayHidden)
        if (n.priority >= 5) b.setColor(0xFFC2410C.toInt())
        if (App.of(ctx).stack) b.setGroup(GROUP + n.topic)
        // Pictures never hold up a post: one not loaded yet is fetched in the background, then the tray is redrawn.
        val images = App.of(ctx).images
        n.icon.takeIf { it.isNotEmpty() }?.let { url ->
            images.cached(url, Images.ICON_PX)?.let(b::setLargeIcon) ?: images.prefetch(url, Images.ICON_PX, ::redraw)
        }
        n.file?.takeIf { it.isImage }?.let { f ->
            images.cached(f.url, Images.PICTURE_PX)?.let { b.setStyle(NotificationCompat.BigPictureStyle().bigPicture(it).setSummaryText(text)) }
                ?: images.prefetch(f.url, Images.PICTURE_PX, ::redraw)
        }
        TrayButton.pick(n).forEach {
            b.addAction(0, if (it.res != 0) ctx.getString(it.res) else it.label, it.url?.let(::link) ?: it.reply?.let { r -> action(n, ACTION_REPLY, r) } ?: action(n, it.action!!))
        }
        nm.notify(n.id, 0, b.build())
    }

    private val redrawing = AtomicBoolean()

    /**
     * A picture arrived: redraw the tray from Room, quietly. reconcile re-plans every row under the config lock,
     * so a notification cleared while its picture loaded stays gone. Many pictures at once make one redraw.
     */
    private fun redraw() {
        if (!redrawing.compareAndSet(false, true)) return
        val app = App.of(ctx)
        app.io.execute {
            redrawing.set(false)
            RoomStore(app).reconcile()
        }
    }

    private fun open(n: Notif): PendingIntent {
        val i = n.click?.takeIf { it.startsWith("http") }?.let { Intent(Intent.ACTION_VIEW, Uri.parse(it)) }
            ?: Intent(ctx, MainActivity::class.java).setFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        return PendingIntent.getActivity(ctx, n.id.hashCode(), i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    /** A sender link opens in the browser. The URL itself makes the PendingIntent unique. */
    private fun link(url: String) =
        PendingIntent.getActivity(
            ctx, 0,
            // BROWSABLE: only apps that accept links from a browser may open it, as if tapped on a web page.
            Intent(Intent.ACTION_VIEW, Uri.parse(url)).addCategory(Intent.CATEGORY_BROWSABLE),
            PendingIntent.FLAG_IMMUTABLE,
        )

    private fun action(n: Notif, action: String, reply: String? = null): PendingIntent {
        val server = App.of(ctx).serverUrl // a tap after a server switch must not reach the new server
        val i = Intent(ctx, ActionReceiver::class.java)
            .setAction(action)
            // unique per (server, id, action, reply) so PendingIntents do not collide or take over each other's extras
            .setData(
                Uri.parse("clrd://n/${n.id}/$action").buildUpon().appendQueryParameter("server", server)
                    // a reply also carries the version shown, so a tap on an older post cannot send a newer seq
                    .apply { if (reply != null) appendQueryParameter("reply", reply).appendQueryParameter("seq", "${n.seq}") }.build(),
            )
            .putExtra(EXTRA_ID, n.id)
            .putExtra(EXTRA_SERVER, server)
            .putExtra(EXTRA_REPLY, reply)
            .putExtra(EXTRA_SEQ, n.seq)
        return PendingIntent.getBroadcast(ctx, 0, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    companion object {
        const val SERVICE_CHANNEL = "sync"
        const val ACTION_ACK = "me.itsjustin.clrd.ACK"
        const val ACTION_SNOOZE = "me.itsjustin.clrd.SNOOZE"
        const val ACTION_DISMISS = "me.itsjustin.clrd.DISMISS" // the delete intent only
        const val ACTION_CLEAR = "me.itsjustin.clrd.CLEAR" // the Clear button
        const val ACTION_REPLY = "me.itsjustin.clrd.REPLY"
        const val ACTION_DISMISS_STACK = "me.itsjustin.clrd.DISMISS_STACK"
        const val EXTRA_IDS = "ids"
        const val GROUP = "topic:"
        const val SUMMARY_ID = 1
        const val EXTRA_ID = "id"
        const val EXTRA_SERVER = "server"
        const val EXTRA_REPLY = "reply"
        const val EXTRA_SEQ = "seq"
    }
}
