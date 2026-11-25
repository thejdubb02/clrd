package me.itsjustin.clrd

import org.json.JSONObject

/**
 * This account's topic subscriptions as the server's settings message last sent them (docs/SPEC.md
 * section 2.3, #617). The server does the filtering; the app only shows and changes the set.
 * [filtered] false means a token with no user: it always sees every topic and may not change the set.
 */
internal data class Subs(val filtered: Boolean, val newTopics: Boolean, val topics: Map<String, Boolean>) {
    fun subscribed(topic: String) = topics[topic] ?: newTopics

    companion object {
        /** Null when the server sent no subscriptions object: an older server, so there is no Topics page. */
        fun of(o: JSONObject?): Subs? {
            o ?: return null
            val t = o.optJSONObject("topics") ?: JSONObject()
            return Subs(o.optBoolean("filtered", true), o.optBoolean("new_topics", true), t.keys().asSequence().associateWith { t.optBoolean(it, true) })
        }
    }
}

/** One topic of `GET /api/v1/subscriptions`; [lastAt] is null for a topic with no message yet. [approvals]: it carried an approval or question (#623). */
internal data class TopicSub(val topic: String, val subscribed: Boolean, val open: Int, val lastAt: String?, val approvals: Boolean)

/** The `GET` and `PUT /api/v1/subscriptions` reply, sorted by name as the server sends it. */
internal data class SubsList(val filtered: Boolean, val newTopics: Boolean, val topics: List<TopicSub>) {
    companion object {
        fun from(o: JSONObject): SubsList {
            val a = o.optJSONArray("topics")
            val rows = (0 until (a?.length() ?: 0)).mapNotNull { a!!.optJSONObject(it) }.map {
                val t = it.optString("topic")
                TopicSub(t, it.optBoolean("subscribed", true), it.optInt("open"), it.optString("last_at").takeIf { s -> s.isNotEmpty() && s != "null" }, it.optBoolean("approvals", t == APPROVAL_TOPIC))
            }
            return SubsList(o.optBoolean("filtered", true), o.optBoolean("new_topics", true), rows)
        }
    }
}

/** Unsubscribing from a topic that carries approvals hides them too (SPEC 2.3), so the app asks first. The server says which (#623); a server without the flag gets this name. */
internal const val APPROVAL_TOPIC = "agents"

/** Whether unsubscribing from [topic] asks first: the server's flag, else the name when the server is older or out of reach. */
internal fun asksFirst(topic: String, list: SubsList?) = list?.topics?.find { it.topic == topic }?.approvals ?: (topic == APPROVAL_TOPIC)

/** The `PUT /api/v1/subscriptions` body that changes one topic and nothing else. */
internal fun topicBody(topic: String, on: Boolean): JSONObject = JSONObject().put("topics", JSONObject().put(topic, on))
