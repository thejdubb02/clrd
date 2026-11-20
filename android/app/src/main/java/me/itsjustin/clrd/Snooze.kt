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
            .map { v -> Regex("""(\d+)d""").matchEntire(v)?.let { "${it.groupValues[1].toLong() * 24}h" } ?: v }
    }
}

/** A Go duration as people say it: "1 hour", "2 days", "90 min". Anything else is shown as typed, like the web. */
fun snoozeLabel(v: String): String {
    val m = Regex("""(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?""").matchEntire(v) ?: return v
    if (v.isEmpty()) return v
    val (h, mi, s) = m.destructured
    val secs = (h.toLongOrNull() ?: 0) * 3600 + (mi.toLongOrNull() ?: 0) * 60 + (s.toLongOrNull() ?: 0)
    if (secs == 0L || secs % 60 != 0L) return v
    val mins = secs / 60
    val (n, unit) = when {
        mins % 1440 == 0L -> mins / 1440 to "day"
        mins % 60 == 0L -> mins / 60 to "hour"
        else -> return "$mins min"
    }
    return "$n $unit" + if (n == 1L) "" else "s"
}
