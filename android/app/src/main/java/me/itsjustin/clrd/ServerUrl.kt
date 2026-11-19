package me.itsjustin.clrd

import java.net.URI

// The Server URL field starts as "https://", so a pasted link lands after that prefill.

/** While typing: a second scheme means a full URL was pasted in, so keep only that URL. */
fun pastedUrl(text: String): String {
    val i = maxOf(text.lastIndexOf("https://", ignoreCase = true), text.lastIndexOf("http://", ignoreCase = true))
    return if (i > 0) text.substring(i).trim() else text
}

// First path segments that belong to the server's own pages, never to where it is mounted.
private val ROUTES = setOf("setup", "app", "api", "docs", "download", "healthz")

/** On save: the server address from whatever was typed or pasted, such as a setup link or an inbox page. */
fun serverAddress(input: String): String {
    var s = pastedUrl(input.trim())
    if (!s.contains("://")) s = "https://$s"
    val u = runCatching { URI(s) }.getOrNull()
    if (u?.host == null) return s.trimEnd('/')
    val port = if (u.port == -1) "" else ":${u.port}"
    val mount = u.rawPath.orEmpty().split('/').filter { it.isNotEmpty() }.takeWhile { it !in ROUTES }
    return "${u.scheme.lowercase()}://${u.host.lowercase()}$port" + mount.joinToString("") { "/$it" }
}
