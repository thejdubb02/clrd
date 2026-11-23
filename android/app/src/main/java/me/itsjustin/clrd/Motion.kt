package me.itsjustin.clrd

import android.content.Context
import android.os.SystemClock
import android.provider.Settings
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

// Card motion, with the values in docs/SPEC.md section 10.1 (#598).

/** A row changed live while the app was in front, at [at] (uptime ms). [arrive]: a new message, a repeat or a snooze ending. */
data class Moved(val at: Long, val arrive: Boolean, val state: String, val by: String?)

/** How long a mark may wait for the list to show its change. Older marks move nothing. */
const val FRESH_MS = 2000L

internal val Entry.notifs get() = when (this) { is One -> listOf(n); is Stack -> rows }
internal val Entry.ids get() = notifs.map { it.id }

/** A card that left the list at [born] (uptime ms), drawn at its old place while it says why and slides away. [toward] is +1 for inline end. */
internal data class Ghost(val entry: Entry, val index: Int, val wait: Int, val moved: Moved, val toward: Int, val born: Long) {
    val life get() = wait + HOLD_MS + SLIDE_MS + GAP_MS
}

internal class Moves(val gone: List<Ghost>, val arrive: List<String>)

/**
 * What moves between two lists of one tab. Only rows marked in the last [FRESH_MS] move, and a mark is set only
 * for a live change while the app is in front, so a first load, a catch-up, a reset or a change made while away
 * moves nothing. A card leaves once none of its rows are left; Clear all goes top to bottom, 40 ms apart, at
 * most 8 steps. [Moves.arrive] keeps the list's order, newest first, and only its first wiggles.
 */
internal fun motion(prev: List<Entry>, next: List<Entry>, now: Long, moved: (String) -> Moved?, toward: (String) -> Int): Moves {
    fun fresh(id: String) = moved(id)?.takeIf { now - it.at in 0..FRESH_MS }
    // only a mark that changed the row moves it out, not an older one such as its arrival
    fun left(n: Notif) = fresh(n.id)?.takeIf { it.state != n.state }
    val nextKeys = next.mapTo(HashSet()) { it.key }
    val nextIds = next.flatMapTo(HashSet()) { it.ids }
    var rank = 0
    val gone = prev.withIndex().mapNotNull { (i, e) ->
        if (e.key in nextKeys || e.ids.any { it in nextIds }) return@mapNotNull null
        val marks = e.notifs.map(::left)
        if (marks.any { it == null }) null else Ghost(e, i, minOf(rank++, 8) * 40, marks[0]!!, toward(e.ids[0]), now)
    }
    val prevKeys = prev.mapTo(HashSet()) { it.key }
    val prevIds = prev.flatMapTo(HashSet()) { it.ids }
    val arrive = next.filter { e -> e.key !in prevKeys && e.ids.any { it !in prevIds && fresh(it)?.arrive == true } }.map { it.key }
    return Moves(gone, arrive)
}

/** One tab's motion: the list last shown, the cards leaving and the ones that just came. Plain fields, stepped from composition. */
internal class MotionState {
    private var prev: List<Entry>? = null // null: the first list, which never moves
    val ghosts = mutableListOf<Ghost>()
    val arrived = HashMap<String, Long>() // key to when it came
    var wiggle: String? = null

    fun step(next: List<Entry>, now: Long, moved: (String) -> Moved?, toward: (String) -> Int) {
        ghosts.removeAll { now - it.born > it.life + 1000 } // one that left off screen never ran
        val p = prev
        if (p == next) return
        prev = next
        if (p == null) return
        val keys = next.mapTo(HashSet()) { it.key }
        ghosts.removeAll { it.entry.key in keys } // an Undo during the hold brings the card back at once
        val m = motion(p, next, now, moved, toward)
        // count the cards still leaving above, so two leaving at once keep their order
        m.gone.forEach { g -> ghosts += g.copy(index = g.index + ghosts.count { it.index <= g.index }) }
        arrived.values.removeIf { now - it > FRESH_MS }
        m.arrive.forEach { arrived[it] = now }
        m.arrive.firstOrNull()?.let { wiggle = it }
    }

    /** The list with each leaving card back at its old place. */
    fun shown(next: List<Entry>): List<Any> {
        val out = next.toMutableList<Any>()
        ghosts.sortedBy { it.index }.forEach { out.add(minOf(it.index, out.size), it) }
        return out
    }
}

internal const val HOLD_MS = 1400L
internal const val SLIDE_MS = 200
internal const val GAP_MS = 200
internal val EaseOut = CubicBezierEasing(.2f, .7f, .2f, 1f)
internal val EaseInOut = CubicBezierEasing(.4f, 0f, .2f, 1f)

/** Remove animations is on: nothing slides, a cleared card still shows why for the hold. */
internal fun stillMotion(ctx: Context) = Settings.Global.getFloat(ctx.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f

@Composable
private fun reason(m: Moved): String {
    val what = stringResource(
        when (m.state) {
            Notif.DISMISSED -> R.string.tab_cleared
            Notif.RESOLVED -> R.string.gone_resolved
            Notif.SNOOZED -> R.string.tab_snoozed
            Notif.ACKNOWLEDGED -> R.string.gone_acknowledged
            Notif.ACTIVE -> R.string.gone_back
            else -> R.string.gone_moved
        },
    )
    val by = m.by?.takeIf { it.isNotEmpty() } ?: return what
    return stringResource(if (m.state == Notif.RESOLVED) R.string.gone_by else R.string.gone_on, what, by)
}

/**
 * A leaving card: a green wash grows from the inline start (420 ms), the reason fades in 180 ms in (260 ms),
 * the card holds until 1400 ms, then slides out of the row and fades (200 ms) and [done] removes it. [card]
 * draws the card as it was, with the reason row it is given in place of its buttons.
 */
@Composable
internal fun Gone(g: Ghost, still: Boolean, done: () -> Unit, card: @Composable (@Composable () -> Unit) -> Unit) {
    val b = LocalBrand.current
    val wash = remember { Animatable(if (still) 1f else 0f) }
    val tick = remember { Animatable(if (still) 1f else 0f) }
    val away = remember { Animatable(0f) }
    val shut = remember { Animatable(0f) }
    val finish by rememberUpdatedState(done)
    LaunchedEffect(Unit) {
        // scrolled to after it was done, or back after its hold was cut short: it is simply gone
        if (SystemClock.uptimeMillis() - g.born > g.wait + HOLD_MS) return@LaunchedEffect finish()
        delay(g.wait.toLong())
        if (!still) {
            launch { wash.animateTo(1f, tween(420, easing = EaseOut)) }
            launch { delay(180); tick.animateTo(1f, tween(260, easing = EaseOut)) }
        }
        delay(HOLD_MS)
        if (!still) {
            away.animateTo(1f, tween(SLIDE_MS, easing = EaseInOut))
            shut.animateTo(1f, tween(GAP_MS, easing = EaseInOut)) // the cards below close the gap
        }
        finish()
    }
    val dir = if (LocalLayoutDirection.current == LayoutDirection.Rtl) -g.toward else g.toward
    val shape = RoundedCornerShape(16.dp)
    Box(
        Modifier.fillMaxWidth().testTag("gone")
            .layout { m, c -> val p = m.measure(c); layout(p.width, (p.height * (1f - shut.value)).toInt()) { p.place(0, 0) } }
            .graphicsLayer { translationX = size.width * away.value * dir; alpha = 1f - away.value },
    ) {
        card {
            Row(Modifier.alpha(tick.value), verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Filled.Check, null, tint = b.clear, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(4.dp))
                Text(reason(g.moved), color = b.clear, fontSize = 14.sp, fontWeight = FontWeight.SemiBold)
            }
        }
        Box(Modifier.matchParentSize().clip(shape).border(1.dp, b.clear.copy(alpha = wash.value), shape)) {
            Box(Modifier.fillMaxHeight().fillMaxWidth(wash.value).background(b.clear.copy(alpha = .08f)))
        }
    }
}

/** A card that just came: it drops in 8 dp and fades in (180 ms), then the newest wiggles 1 degree each way (320 ms). */
@Composable
internal fun Arrive(arrive: Boolean, wiggle: () -> Boolean, content: @Composable () -> Unit) {
    val drop = remember { Animatable(if (arrive) 0f else 1f) }
    val turn = remember { Animatable(0f) }
    val px = with(LocalDensity.current) { 8.dp.toPx() }
    LaunchedEffect(Unit) {
        if (!arrive) return@LaunchedEffect
        drop.animateTo(1f, tween(180, easing = EaseOut))
        if (wiggle()) { // asked once it has dropped in, so only the newest of a burst wiggles
            turn.animateTo(1f, tween(80, easing = EaseInOut))
            turn.animateTo(-1f, tween(160, easing = EaseInOut))
            turn.animateTo(0f, tween(80, easing = EaseInOut))
        }
    }
    Box(Modifier.graphicsLayer { translationY = -px * (1f - drop.value); alpha = drop.value; rotationZ = turn.value }) { content() }
}
