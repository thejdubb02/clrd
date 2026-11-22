package me.itsjustin.clrd

import org.json.JSONArray
import org.json.JSONObject

/**
 * The buttons a swipe on a card reveals (#161, docs/SPEC.md display settings), shared by every device.
 * [start] shows on a swipe toward the start of the line, [end] on the other swipe; the first sits nearest the edge.
 * Names are kept as the server sent them: one this build does not know is skipped on a card, never dropped from the setting.
 */
data class Swipes(val start: List<String> = listOf("clear", "snooze"), val end: List<String> = listOf("ack")) {
    fun json(): String = JSONObject().put("swipe_start", JSONArray(start)).put("swipe_end", JSONArray(end)).toString()

    /**
     * [name] tapped in Settings for one side: taken off that side when it is there, else added last on it and taken
     * off the other side (a name shows once across both). Null when that side already holds [MAX].
     */
    fun toggle(name: String, toStart: Boolean): Swipes? {
        val mine = if (toStart) start else end
        val next = when {
            name in mine -> mine - name
            mine.size < MAX -> mine + name
            else -> return null
        }
        val other = (if (toStart) end else start) - name
        return if (toStart) Swipes(next, other) else Swipes(other, next)
    }

    companion object {
        const val MAX = 3
        /** Every name the server takes, in the order Settings offers them. */
        val NAMES = listOf("clear", "snooze", "ack", "resolve")

        /** The swipe part of a settings object; a side it does not carry (or carries as null) keeps [old]'s. */
        fun from(o: JSONObject, old: Swipes): Swipes = Swipes(side(o, "swipe_start") ?: old.start, side(o, "swipe_end") ?: old.end)

        private fun side(o: JSONObject, key: String): List<String>? = o.optJSONArray(key)?.let { a -> List(a.length()) { a.optString(it) } }

        /** What [json] saved, or the defaults. */
        fun parse(saved: String?): Swipes = saved?.let { runCatching { from(JSONObject(it), Swipes()) }.getOrNull() } ?: Swipes()

        /**
         * The names a card shows, in order: Ack only on an active card, Snooze only in the Inbox, and never one this
         * app has no button for (Resolve, or a name from a newer server).
         */
        fun shown(names: List<String>, active: Boolean, inbox: Boolean): List<String> = names.distinct().filter {
            when (it) {
                "clear" -> true
                "snooze" -> inbox
                "ack" -> active
                else -> false
            }
        }
    }
}
