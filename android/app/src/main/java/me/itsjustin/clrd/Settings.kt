package me.itsjustin.clrd

/**
 * The Settings page layout from docs/SPEC.md section 9.1: sections in order, then each setting's
 * id, label, one-line description and search keywords. Android rows only; the web has its own.
 */
internal class Setting(val section: Int, val id: String, val label: Int, val desc: Int?, val keywords: List<String>)

internal val SETTINGS = listOf(
    Setting(R.string.section_notifications, "sound", R.string.sound, R.string.sound_explain, listOf("sound", "chime", "ring", "audio", "volume")),
    Setting(R.string.section_notifications, "subscriptions", R.string.subs_title, R.string.subs_explain, listOf("subscribe", "unsubscribe", "topic", "follow", "new topics")),
    Setting(R.string.section_notifications, "muted-topics", R.string.topics, R.string.topics_explain, listOf("mute", "topic", "pop up", "quiet")),
    Setting(R.string.section_notifications, "shade-clears", R.string.shade_clears, R.string.shade_clears_explain, listOf("swipe", "shade", "tray", "clear everywhere", "dismiss")),
    Setting(R.string.section_notifications, "battery", R.string.battery, R.string.battery_explain, listOf("battery", "background", "delayed", "killed", "optimization")),
    Setting(R.string.section_inbox, "stack", R.string.stack, R.string.stack_explain, listOf("group", "fold", "bundle", "collapse")),
    Setting(R.string.section_inbox, "swipe", R.string.swipe_settings, R.string.swipe_explain, listOf("swipe", "gesture", "slide", "buttons")),
    Setting(R.string.section_inbox, "full-swipe", R.string.fast_swipe, R.string.fast_swipe_explain, listOf("swipe", "fast", "gesture", "one move")),
    Setting(R.string.section_inbox, "snooze", R.string.snooze_settings, R.string.snooze_explain, listOf("later", "remind", "postpone", "default time")),
    Setting(R.string.section_inbox, "drop-cleared", R.string.drop_cleared, R.string.drop_cleared_explain, listOf("hide", "cleared tab", "remove")),
    Setting(R.string.section_display, "formatting", R.string.formatting_settings, R.string.formatting_explain, listOf("markdown", "bold", "links", "site icon", "favicon", "preview")),
    Setting(R.string.section_display, "appearance", R.string.appearance, R.string.appearance_explain, listOf("theme", "dark mode", "light mode", "colors", "system")),
    Setting(R.string.section_devices, "server", R.string.server, R.string.server_explain, listOf("server", "address", "url", "token", "sign in", "log in")),
    Setting(R.string.section_devices, "servers", R.string.servers, R.string.servers_explain, listOf("server", "add server", "another server", "switch", "remove")),
    Setting(R.string.section_devices, "devices", R.string.devices_title, R.string.devices_explain, listOf("phone", "browser", "unlink", "sign out", "approve", "may approve", "activity")),
    Setting(R.string.section_devices, "link-phone", R.string.link_phone, R.string.link_phone_explain, listOf("qr", "scan", "pair", "code", "add phone")),
    Setting(R.string.section_devices, "client-cert", R.string.client_cert, R.string.client_cert_explain, listOf("certificate", "mtls", "mutual tls", "tls", "key")),
    Setting(R.string.section_integrations, "senders", R.string.senders_title, R.string.senders_explain, listOf("sender", "rules", "bot", "agent", "allow")),
)

/** SPEC 9.1: the typed text, trimmed and ignoring case, appears in the label, the description or a keyword. */
internal fun settingMatches(query: String, label: String, desc: String?, keywords: List<String>): Boolean {
    val q = query.trim()
    return q.isEmpty() || label.contains(q, true) || desc?.contains(q, true) == true || keywords.any { it.contains(q, true) }
}
