package me.itsjustin.clrd

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.media.AudioAttributes
import android.net.Uri
import android.provider.Settings
import androidx.core.app.NotificationCompat
import androidx.core.app.RemoteInput
import androidx.core.content.ContextCompat
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.atomic.AtomicBoolean

/** What the phone tray should do for one notification. Pure, so it is unit tested on the JVM. */
sealed interface TrayStep {
    /** [again]: a `repeat` event, so it alerts even while it is still showing. */
    data class Show(val n: Notif, val silent: Boolean, val again: Boolean = false) : TrayStep
    data class Cancel(val id: String) : TrayStep

    companion object {
        /**
         * active: show and alert. acknowledged: keep showing, quietly, with who acked it.
         * snoozed: hide (the server re-activates it later, which shows it again).
         * dismissed / resolved: cancel everywhere. That is the headline feature.
         */
        fun plan(n: Notif, muted: Set<String>): TrayStep = when {
            n.topic in muted -> Cancel(n.id)
            n.state == Notif.ACTIVE -> Show(n, silent = false) // a silenced one still counts as an alert here, so a swiped one comes back; post() keeps it quiet
            n.state == Notif.ACKNOWLEDGED -> Show(n, silent = true)
            else -> Cancel(n.id)
        }
    }
}

/** One tray button: a sender link or reply (docs/SPEC.md section 2), or one of ours, by its [Tray] action. */
data class TrayButton(val label: String = "", val res: Int = 0, val url: String? = null, val action: String? = null, val reply: String? = null, val device: Int? = null, val text: Boolean = false) {
    companion object {
        val ACK = TrayButton(res = R.string.tray_ack, action = Tray.ACTION_ACK)
        val SNOOZE = TrayButton(res = R.string.tray_snooze, action = Tray.ACTION_SNOOZE)
        val CLEAR = TrayButton(res = R.string.tray_clear, action = Tray.ACTION_CLEAR) // not the delete intent's, so the log tells them apart

        /**
         * Android shows 3 buttons at most. The sender's replies come first (at most 3, until someone answers),
         * then up to 2 sender links (http and https only) or device actions (http and broadcast, #449), then ours fill what is left in this order:
         * Ack (unless acked already), Clear, Snooze.
         * A swipe still clears, so Clear is never the only way out. Pure, so it is unit tested on the JVM.
         */
        fun pick(n: Notif): List<TrayButton> {
            val objs = objs(n)
            // An answer approves the whole text (#366), so a text the tray may cut answers in the app, where it shows in full.
            // A picture's tray style shows the text as a short summary only.
            val replies = if (fitsTray(n.message) && n.file?.isImage != true) replies(n) else emptyList()
            val links = objs.mapIndexedNotNull { i, o ->
                val url = o.optString("url")
                if (DeviceAction.isDevice(o)) return@mapIndexedNotNull TrayButton(o.optString("label"), if (o.optString("label").isBlank()) R.string.tray_open else 0, device = i)
                // Web links only, and short: a huge URL would not fit through Binder and would crash the post.
                if (url.length > 2048 || (!url.startsWith("https://", true) && !url.startsWith("http://", true))) null
                // Android matches intent schemes case-sensitively, so HTTPS://x must become https://x
                else TrayButton(o.optString("label"), if (o.optString("label").isBlank()) R.string.tray_open else 0, url.substringBefore(':').lowercase() + url.substring(url.indexOf(':')))
            }.take(minOf(2, 3 - replies.size))
            val ours = listOfNotNull(ACK.takeIf { n.state != Notif.ACKNOWLEDGED }, CLEAR, SNOOZE).take(3 - replies.size - links.size)
            return replies + links + listOf(ACK, SNOOZE, CLEAR).filter { it in ours } // ours keep their usual order
        }

        /** The sender's answer buttons, at most 3, until someone answers. The app card shows them too. */
        fun replies(n: Notif): List<TrayButton> = if (n.reply.isNotEmpty()) emptyList() else objs(n).mapNotNull { o ->
            o.optString("reply").takeIf { it.isNotEmpty() }?.let { TrayButton(o.optString("label").ifBlank { it }, reply = it, text = o.optBoolean("text")) }
        }.take(3)

        /** A typed answer's body (#153): the text trimmed, 1 to 1000 characters (code points), or null when it cannot be sent. */
        fun typed(reply: String, text: String, seq: Long): String? {
            val t = text.trim { it.isWhitespace() || it.code == 0x85 } // as the server trims (Go unicode.IsSpace)
            if (t.isEmpty() || t.codePointCount(0, t.length) > 1000) return null
            return JSONObject().put("reply", reply).put("text", t).put("seq", seq).toString()
        }

        /** The sender's action objects; a device button's index points into this list. */
        fun objs(n: Notif): List<JSONObject> {
            val a = runCatching { JSONArray(n.actions) }.getOrNull() ?: JSONArray()
            return (0 until a.length()).mapNotNull { a.optJSONObject(it) }
        }

        // ponytail: a guess at what an expanded tray row shows on a small phone, not measured. Lower it if a phone cuts sooner.
        const val TRAY_CHARS = 400
        const val TRAY_LINES = 8
        fun fitsTray(text: String) = text.length <= TRAY_CHARS && text.count { it == '\n' } < TRAY_LINES
    }
}

/** Posts and cancels system notifications. tag = clrd notification id, int id is always 0. */
class Tray(private val ctx: Context) {
    private val nm = ctx.getSystemService(NotificationManager::class.java)

    /**
     * Makes the channels for [sound] (see [Sounds]) and deletes the other sounds' channels. Posts go to the new ones.
     * True when this change took the Urgent channel's Do Not Disturb pass with it.
     */
    fun createChannels(sound: String = App.of(ctx).sound): Boolean {
        val uri = Sounds.uri(ctx, sound)
        val audio = AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_NOTIFICATION).setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION).build()
        val names = listOf(R.string.channel_p1, R.string.channel_p2, R.string.channel_p3, R.string.channel_p4, R.string.channel_p5)
        val importance = listOf(NotificationManager.IMPORTANCE_MIN, NotificationManager.IMPORTANCE_LOW, NotificationManager.IMPORTANCE_DEFAULT, NotificationManager.IMPORTANCE_HIGH, NotificationManager.IMPORTANCE_HIGH)
        // A channel the user blocked or turned down in system settings stays that way on the new sound.
        val ours = nm.notificationChannels.filter { Sounds.isOurs(it.id) }
        val lowest = ours.groupBy { it.id[1].digitToInt() }.mapValues { (_, cs) -> cs.minOf { it.importance } }
        val mine = (1..5).map { p ->
            NotificationChannel(Sounds.channel(p, sound), ctx.getString(names[p - 1]), minOf(importance[p - 1], lowest[p] ?: importance[p - 1]))
                .apply { if (Sounds.known(sound) != Sounds.DEFAULT) setSound(uri, audio) }
        }
        nm.createNotificationChannels(
            mine + listOf(
                // No badge: the always-on "in sync" notice must not put a dot on the app icon.
                NotificationChannel(SERVICE_CHANNEL, ctx.getString(R.string.channel_service), NotificationManager.IMPORTANCE_MIN)
                    .apply { setShowBadge(false) },
            ),
        )
        val keep = mine.map { it.id }.toSet()
        val gone = ours.map { it.id }.filter { it !in keep }.toSet()
        val noticeUp = nm.activeNotifications.any { it.id == DND_ID && it.tag == null }
        // Cancelled by us first: Android's own cancel on channel delete can fire the delete intent, which reads as a swipe.
        nm.activeNotifications.filter { it.notification.channelId in gone }.forEach { nm.cancel(it.tag, it.id) }
        gone.forEach(nm::deleteNotificationChannel)
        // Android never lets an app change a channel's badge setting after creation, so the old channel is replaced.
        nm.deleteNotificationChannel("service")
        val had = ours.any { it.id in gone && it.id[1] == '5' && it.canBypassDnd() }
        // A notice still up went with its old channel above; it comes back on the new one while the pass is still off.
        return dndLost(sound, had) && (had || noticeUp)
    }

    /**
     * A sound change from the server or an agent took the Do Not Disturb pass (#380). The person is not in
     * Settings to see the note there, so the tray says it once; a tap opens the switch.
     */
    fun dndNotice(sound: String = App.of(ctx).sound) {
        val open = Intent(Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
            .putExtra(Settings.EXTRA_APP_PACKAGE, ctx.packageName)
            .putExtra(Settings.EXTRA_CHANNEL_ID, Sounds.channel(5, sound))
        nm.notify(
            DND_ID,
            NotificationCompat.Builder(ctx, Sounds.channel(5, sound)) // the channel it is about: muted only if urgent ones are
                .setSmallIcon(R.drawable.ic_stat_clrd)
                .setContentTitle(ctx.getString(R.string.dnd_notice_title))
                .setContentText(ctx.getString(R.string.dnd_notice))
                .setStyle(NotificationCompat.BigTextStyle().bigText(ctx.getString(R.string.dnd_notice)))
                .setContentIntent(PendingIntent.getActivity(ctx, DND_ID, open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
                .build(), // no auto cancel: it stays until the pass is back (see [dndLost])
        )
    }

    /** True when a sound change dropped the Urgent channel's Do Not Disturb pass and the person has not set it again (#346). */
    @Synchronized // Settings and a sound change both read then write the flag; a stale false must not undo a fresh true
    fun dndLost(sound: String = App.of(ctx).sound, replacedHadIt: Boolean = false): Boolean {
        val prefs = App.of(ctx).prefs
        val lost = Sounds.dndLost(prefs.getBoolean("dnd_lost", false), replacedHadIt, nm.getNotificationChannel(Sounds.channel(5, sound))?.canBypassDnd() == true)
        prefs.edit().putBoolean("dnd_lost", lost).apply()
        if (!lost) nm.cancel(DND_ID) // turned back on: the tray notice has done its job
        return lost
    }

    /** True when the phone's own settings override the chosen sound (changed sound or importance on p3..p5). */
    fun soundOverridden(sound: String = App.of(ctx).sound): Boolean =
        Sounds.overridden(
            Sounds.uri(ctx, sound),
            (3..5).associateWith { p -> nm.getNotificationChannel(Sounds.channel(p, sound))?.let { it.sound to it.importance } },
        )

    /** Post one row again, quietly, as Room has it: clears the spinner Android shows after an inline reply (#153). */
    fun repost(id: String) {
        val n = App.of(ctx).db.dao().get(id) ?: return
        apply(TrayStep.plan(n, App.of(ctx).muted).let { if (it is TrayStep.Show) TrayStep.Show(n, silent = true) else it })
    }

    fun apply(step: TrayStep) {
        when (step) {
            is TrayStep.Cancel -> nm.cancel(step.id, 0)
            is TrayStep.Show -> post(step.n, step.silent, step.again)
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
        dndLost() // a pass turned back on in Android's settings takes the #380 notice with it on the next sync or launch
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

    private fun post(n: Notif, silent: Boolean, again: Boolean = false) {
        if (android.os.Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) return
        val app = App.of(ctx)
        if (n.id in app.trayHidden) { // hidden by a bulk clear on this phone (#265): only a new alert brings it back
            if (silent) return
            app.trayHidden -= n.id
        }
        val text = Md.tray(n.message, n.isMarkdown)
        val b = NotificationCompat.Builder(ctx, Sounds.channel(n.priority, app.sound))
            .setSmallIcon(R.drawable.ic_stat_clrd)
            .setContentTitle(n.title.ifEmpty { n.topic })
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setSubText(
                when {
                    n.reply.isNotEmpty() -> ctx.getString(R.string.tray_answered, n.replyText.ifEmpty { n.reply }, n.stateBy ?: ctx.getString(R.string.tray_another_device))
                    n.state == Notif.ACKNOWLEDGED -> ctx.getString(R.string.tray_acked, n.stateBy ?: ctx.getString(R.string.tray_another_device))
                    else -> n.topic
                },
            )
            .setWhen(n.createdAt)
            .setShowWhen(true)
            .setPriority(n.priority - 3) // PRIORITY_MIN..MAX for API < 26 builders; channels rule on 26+
            .setOnlyAlertOnce(!again) // updates to a showing notification stay quiet; a repeat rings again (#259)
            .setSilent(silent || n.silenced) // a server silence matched it (#328): show, no sound or vibration
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
            val label = if (it.res != 0) ctx.getString(it.res) else it.label
            // A text action answers inline (#153); it never touches the delete intent, so a swipe still only hides.
            if (it.text) b.addAction(
                NotificationCompat.Action.Builder(0, label, action(n, ACTION_REPLY, it.reply, typed = true))
                    .addRemoteInput(RemoteInput.Builder(EXTRA_TEXT).setLabel(label).build()).setAllowGeneratedReplies(false).build(),
            )
            else b.addAction(0, label, it.url?.let(::link) ?: it.reply?.let { r -> action(n, ACTION_REPLY, r) }
                ?: it.device?.let { d -> action(n, ACTION_DEVICE, index = d) } ?: action(n, it.action!!))
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

    private fun action(n: Notif, action: String, reply: String? = null, index: Int? = null, typed: Boolean = false): PendingIntent {
        val server = App.of(ctx).serverUrl // a tap after a server switch must not reach the new server
        val i = Intent(ctx, ActionReceiver::class.java)
            .setAction(action)
            // unique per (server, id, action, reply) so PendingIntents do not collide or take over each other's extras
            .setData(
                Uri.parse("clrd://n/${n.id}/$action").buildUpon().appendQueryParameter("server", server)
                    // a reply also carries the version shown, so a tap on an older post cannot send a newer seq
                    .apply { if (reply != null) appendQueryParameter("reply", reply).appendQueryParameter("seq", "${n.seq}") }
                    .apply { if (typed) appendQueryParameter("text", "1") }
                    // a device button carries its index and the version shown, so a tap runs only the button it showed
                    .apply { if (index != null) appendQueryParameter("i", "$index").appendQueryParameter("seq", "${n.seq}") }.build(),
            )
            .putExtra(EXTRA_ID, n.id)
            .putExtra(EXTRA_SERVER, server)
            .putExtra(EXTRA_REPLY, reply)
            .putExtra(EXTRA_SEQ, n.seq)
            .putExtra(EXTRA_INDEX, index ?: -1)
        // Only the typed answer is mutable: Android writes the text into it. The explicit receiver keeps it ours.
        val mutable = if (typed && android.os.Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else if (typed) 0 else PendingIntent.FLAG_IMMUTABLE
        return PendingIntent.getBroadcast(ctx, 0, i, mutable or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    companion object {
        const val SERVICE_CHANNEL = "sync"
        const val ACTION_ACK = "me.itsjustin.clrd.ACK"
        const val ACTION_SNOOZE = "me.itsjustin.clrd.SNOOZE"
        const val ACTION_DISMISS = "me.itsjustin.clrd.DISMISS" // the delete intent only
        const val ACTION_CLEAR = "me.itsjustin.clrd.CLEAR" // the Clear button
        const val ACTION_REPLY = "me.itsjustin.clrd.REPLY"
        const val ACTION_DEVICE = "me.itsjustin.clrd.DEVICE"
        const val ACTION_DISMISS_STACK = "me.itsjustin.clrd.DISMISS_STACK"
        const val EXTRA_IDS = "ids"
        const val GROUP = "topic:"
        const val SUMMARY_ID = 1
        const val DND_ID = 2 // no tag, so the tray rebuild (tagged, id 0 or 1) never touches it
        const val EXTRA_ID = "id"
        const val EXTRA_SERVER = "server"
        const val EXTRA_REPLY = "reply"
        const val EXTRA_SEQ = "seq"
        const val EXTRA_TEXT = "text" // the RemoteInput key of a typed answer (#153)
        const val EXTRA_INDEX = "index"
    }
}
