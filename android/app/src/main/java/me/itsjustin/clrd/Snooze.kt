package me.itsjustin.clrd

import org.json.JSONArray
import org.json.JSONObject

/**
 * The snooze times every device shares (docs/SPEC.md, display settings), as Go durations.
 * A plain tap on Snooze uses [default]; a long press offers [times].
 */
data class Snooze(val default: String = "1h", val times: List<String> = listOf("1h", "4h")) {
    fun json(): String = JSONObject().put("snooze_default", default).put("snooze_intervals", JSONArray(times)).toString()

    companion object {
        /** The snooze part of a settings object, or null when it has none that makes sense. */
        fun from(o: JSONObject): Snooze? {
            val a = o.optJSONArray("snooze_intervals") ?: return null
            val times = List(a.length()) { a.optString(it) }.filter { it.isNotBlank() }
            val def = o.optString("snooze_default")
            return if (def in times) Snooze(def, times) else null
        }

        /** The action body that snoozes for [v]. */
        fun body(v: String): String = JSONObject().put("for", v).toString()

        /** Times typed in Settings, comma separated. "2d" is sent as "48h", the form the server reads (same as the web). */
        fun parse(text: String): List<String> = text.split(',').map { it.trim() }.filter { it.isNotEmpty() }
            .map { v -> Regex("""(\d{1,4})d""").matchEntire(v)?.let { "${it.groupValues[1].toInt() * 24}h" } ?: v }
    }
}

/** Seconds in a Go duration of whole h, m and s ("1h30m"), or null for any other form. */
fun goSeconds(v: String): Long? {
    val m = Regex("""(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?""").matchEntire(v) ?: return null
    if (v.isEmpty()) return null
    val (h, mi, s) = m.destructured
    return (h.toLongOrNull() ?: 0) * 3600 + (mi.toLongOrNull() ?: 0) * 60 + (s.toLongOrNull() ?: 0)
}

/** Milliseconds in any duration the server's snooze reads ("1.5h", "500ms", "2d"), or null for any other form (#508). */
fun goMillis(v: String): Long? {
    Regex("""(\d{1,5})d""").matchEntire(v)?.let { return it.groupValues[1].toLong().takeIf { d -> d <= 36500 }?.times(86_400_000) }
    val part = Regex("""(\d+\.?\d*|\.\d+)(ns|us|µs|μs|ms|s|m|h)""")
    if (v.isEmpty() || part.replace(v, "").isNotEmpty()) return null
    val unit = mapOf("ns" to 1e-6, "us" to 1e-3, "µs" to 1e-3, "μs" to 1e-3, "ms" to 1.0, "s" to 1e3, "m" to 6e4, "h" to 3.6e6)
    return part.findAll(v).sumOf { it.groupValues[1].toDouble() * unit.getValue(it.groupValues[2]) }.toLong()
}

/** Phone-clock end of a queued snooze, kept in its body and never sent (#508). */
const val ENDS = "ends_local"

/**
 * A queued snooze as sent: "for" is what is left of it now (#508), so one sent late ends when the phone
 * showed. Relative, so a phone clock that is off cannot make the server refuse it. One that ran out
 * while offline still goes as 1s, so it stops a repeating alert as the tap did.
 */
fun snoozeWire(body: String, now: Long = System.currentTimeMillis()): String {
    val o = runCatching { JSONObject(body) }.getOrNull() ?: return body
    if (!o.has(ENDS)) return body
    val left = (o.optLong(ENDS) - now).coerceAtLeast(1000)
    o.remove(ENDS)
    return o.put("for", "${left}ms").toString()
}

/** When a snooze or reopen action body ends, in epoch ms, or null when it has no future time this reads (#473). */
fun snoozeEnd(body: JSONObject, now: Long = System.currentTimeMillis()): Long? {
    val t = body.optString("until").takeIf { it.isNotEmpty() }?.let { runCatching { java.time.Instant.parse(it).toEpochMilli() }.getOrNull() }
        ?: goMillis(body.optString("for"))?.let { now + it }
    return t?.takeIf { it > now }
}

/** A Go duration as people say it: "1 hour", "2 days", "90 min". Anything else is shown as typed, like the web. */
fun snoozeLabel(v: String): String {
    val secs = goSeconds(v) ?: return v
    if (secs == 0L || secs % 60 != 0L) return v
    val mins = secs / 60
    val (n, unit) = when {
        mins % 1440 == 0L -> mins / 1440 to "day"
        mins % 60 == 0L -> mins / 60 to "hour"
        else -> return "$mins min"
    }
    return "$n $unit" + if (n == 1L) "" else "s"
}
