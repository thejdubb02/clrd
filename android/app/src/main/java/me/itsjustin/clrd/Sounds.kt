package me.itsjustin.clrd

import android.content.Context
import android.net.Uri
import android.provider.Settings

/**
 * The shared sound names (docs/SPEC.md section 2.2). On Android a sound belongs to the notification
 * channel and an app cannot change it afterwards, so each sound gets its own set of channels:
 * `default` keeps p1..p5 (what every phone had before sounds), the others are p1-chime..p5-chime and so on.
 * Picking a sound makes its channels and deletes the rest, so the choice really changes what plays.
 */
object Sounds {
    const val DEFAULT = "default"
    const val NONE = "none"
    val BUILT_IN = listOf("chime", "ding", "pop", "bell", "alert", "soft")
    val NAMES = listOf(DEFAULT, NONE) + BUILT_IN

    /** An unknown name plays `default` (SPEC). */
    fun known(name: String?) = name?.takeIf { it in NAMES } ?: DEFAULT

    fun channel(priority: Int, sound: String) = known(sound).let { if (it == DEFAULT) "p$priority" else "p$priority-$it" }

    /** Ours: p1..p5 with or without a sound suffix. Never the sync notice's channel. */
    fun isOurs(channelId: String) = Regex("p[1-5](-[a-z]+)?").matches(channelId)

    /**
     * What the channel should play: null for silent. A built-in sound by name, not by resource number,
     * since a channel keeps its sound forever and resource numbers can change between builds.
     */
    fun uri(ctx: Context, sound: String): Uri? = when (val s = known(sound)) {
        DEFAULT -> Settings.System.DEFAULT_NOTIFICATION_URI
        NONE -> null
        else -> Uri.parse("android.resource://${ctx.packageName}/raw/$s")
    }

    /**
     * True when the phone's own settings play something other than the choice on a channel that alerts
     * (p3..p5): someone changed the sound or the importance in system settings. Pure, so it is tested on the JVM.
     * [actual] is (sound, importance) per priority, null for a channel that does not exist.
     */
    fun overridden(expected: Uri?, actual: Map<Int, Pair<Uri?, Int>?>): Boolean =
        (3..5).any { p -> actual[p]?.let { (sound, importance) -> sound != expected || importance < android.app.NotificationManager.IMPORTANCE_DEFAULT } ?: false }

    /**
     * Whether Settings says the Urgent channel lost its Do Not Disturb pass (#346). Only the person can set
     * that pass, so a new sound (a new channel) drops it. [replacedHadIt]: an Urgent channel this change
     * deleted let urgent ones through. It stays until the current Urgent channel has the pass. Pure, so it is tested on the JVM.
     */
    fun dndLost(was: Boolean, replacedHadIt: Boolean, nowHasIt: Boolean) = !nowHasIt && (was || replacedHadIt)
}
