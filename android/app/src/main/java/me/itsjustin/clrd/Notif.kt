package me.itsjustin.clrd

import androidx.room.ColumnInfo
import androidx.room.Entity
import androidx.room.PrimaryKey
import org.json.JSONArray
import org.json.JSONObject
import java.time.OffsetDateTime

/** One clrd notification, as the server sends it (docs/SPEC.md section 2). */
@Entity(tableName = "notif")
data class Notif(
    @PrimaryKey val id: String,
    val topic: String,
    val title: String,
    val message: String,
    val priority: Int,
    val tags: String, // comma separated
    val click: String?,
    val actions: String, // raw JSON array of {label, url} or {label, reply}
    val state: String,
    val stateBy: String?,
    val snoozeUntil: Long?, // epoch ms
    val createdAt: Long, // epoch ms
    val updatedAt: Long, // epoch ms
    val seq: Long,
    @ColumnInfo(defaultValue = "") val reply: String = "", // the reply action tapped, "" until someone answers
) {
    val isFinal get() = state == DISMISSED || state == RESOLVED

    companion object {
        const val ACTIVE = "active"
        const val ACKNOWLEDGED = "acknowledged"
        const val SNOOZED = "snoozed"
        const val DISMISSED = "dismissed"
        const val RESOLVED = "resolved"

        fun fromJson(o: JSONObject) = Notif(
            id = o.getString("id"),
            topic = o.optString("topic"),
            title = o.str("title") ?: "",
            message = o.str("message") ?: "",
            priority = o.optInt("priority", 3).coerceIn(1, 5),
            tags = o.optJSONArray("tags")?.let { a -> (0 until a.length()).joinToString(",") { a.getString(it) } } ?: "",
            click = o.str("click"),
            actions = (o.optJSONArray("actions") ?: JSONArray()).toString(),
            state = o.optString("state", ACTIVE),
            stateBy = o.str("state_by"),
            snoozeUntil = o.str("snooze_until")?.let(::millis),
            createdAt = o.str("created_at")?.let(::millis) ?: 0,
            updatedAt = o.str("updated_at")?.let(::millis) ?: 0,
            seq = o.optLong("seq"),
            reply = o.str("reply") ?: "",
        )

        private fun millis(s: String) = OffsetDateTime.parse(s).toInstant().toEpochMilli()

        /** optString turns JSON null into "null"; this returns a real null. */
        private fun JSONObject.str(k: String): String? = if (isNull(k)) null else optString(k)
    }
}

/** A user action waiting to reach the server. Saved before any network call so a swipe made offline is not lost. */
@Entity(tableName = "pending")
data class Pending(
    @PrimaryKey(autoGenerate = true) val key: Long = 0,
    val notifId: String,
    val action: String, // ack | snooze | dismiss | reopen | reply
    val body: String = "{}",
)

/** One-row table: the highest event seq applied. Kept in the same database so it commits with the rows it covers. */
@Entity(tableName = "meta")
data class Meta(@PrimaryKey val k: String, val v: Long)
