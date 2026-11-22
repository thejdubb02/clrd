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
    @ColumnInfo(defaultValue = "") val icon: String = "", // http(s) image URL, "" if none
    @ColumnInfo(defaultValue = "") val attachment: String = "", // raw JSON object, "" if none
    @ColumnInfo(defaultValue = "markdown") val format: String = "markdown", // or "text": shown exactly as sent
    @ColumnInfo(defaultValue = "0") val silenced: Boolean = false, // a server silence matched this event: show, never alert (#328)
    @ColumnInfo(defaultValue = "") val marks: String = "", // raw JSON array of {by, mark, note, link}, "" if none
    @ColumnInfo(defaultValue = "") val replyText: String = "", // what was typed for a text action (#153), shown as plain text
) {
    /** Who saw or actioned it, for the card: "stew actioned: filed relay#512". */
    val markLines: List<String> get() = runCatching {
        val a = JSONArray(marks)
        (0 until a.length()).map { a.getJSONObject(it) }.map { m ->
            "${m.optString("by")} ${m.optString("mark")}" + (m.optString("note").takeIf { it.isNotEmpty() }?.let { ": $it" } ?: "")
        }
    }.getOrDefault(emptyList())

    /** The mark from [byId] (this phone's, from `ready`), or null. Never guessed: no id, no mark (#451). */
    fun markBy(byId: String): JSONObject? = if (byId.isEmpty()) null else runCatching {
        val a = JSONArray(marks)
        (0 until a.length()).map { a.getJSONObject(it) }.firstOrNull { it.optString("by_id") == byId }
    }.getOrNull()

    val isFinal get() = state == DISMISSED || state == RESOLVED

    /** The one file this notification carries, or null (docs/SPEC.md section 2, "Attachments"). */
    val file: Attachment? get() = Attachment.parse(attachment)

    val isMarkdown get() = format != "text"

    /** Copy: the title, a blank line, the message, then click on its own line (docs/SPEC.md section 2). Empty parts are left out. */
    fun copyText() = listOf(title, message).filter { it.isNotEmpty() }.joinToString("\n\n") + (click?.takeIf { it.isNotEmpty() }?.let { "\n$it" } ?: "")

    /** Share: the subject carries the title, so the text is the message plus click. */
    fun shareText() = listOfNotNull(message.takeIf { it.isNotEmpty() }, click?.takeIf { it.isNotEmpty() }).joinToString("\n")

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
            replyText = o.str("reply_text") ?: "",
            icon = o.str("icon") ?: "", // events logged before the server had icons leave it out
            attachment = o.optJSONObject("attachment")?.toString() ?: "",
            format = o.str("format") ?: "markdown", // events logged before formats existed are Markdown
            silenced = o.optBoolean("silenced"), // missing on events logged before silences: not silenced
            marks = o.optJSONArray("marks")?.takeIf { it.length() > 0 }?.toString() ?: "",
        )

        private fun millis(s: String) = OffsetDateTime.parse(s).toInstant().toEpochMilli()

        /** optString turns JSON null into "null"; this returns a real null. */
        private fun JSONObject.str(k: String): String? = if (isNull(k)) null else optString(k)
    }
}

/** A file on a notification. A [url] starting with one "/" is on our server and needs the token; anything else is an outside link. */
data class Attachment(val name: String, val url: String, val type: String, val size: Long) {
    val isOurs get() = Images.isServerPath(url)
    val isImage get() = type in IMAGES

    companion object {
        /** The types the server shows inline (docs/SPEC.md section 2); everything else is a download. */
        private val IMAGES = setOf("image/png", "image/jpeg", "image/gif", "image/webp")

        fun parse(raw: String): Attachment? {
            val o = runCatching { JSONObject(raw) }.getOrNull() ?: return null
            val url = o.optString("url")
            if (!Images.isServerPath(url) && !url.startsWith("https://", true) && !url.startsWith("http://", true)) return null
            return Attachment(o.optString("name").ifEmpty { "attachment" }, url, o.optString("type"), o.optLong("size"))
        }
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
