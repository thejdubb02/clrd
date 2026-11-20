package me.itsjustin.clrd

import android.content.Context
import android.os.Build
import android.provider.Settings
import java.net.URI
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64

// Flow A (issue #74): a signed-in browser shows a QR code, this phone scans it, the browser approves the phone.

/**
 * A browser offering to link this phone. Unlike [PairLink], [server] is called (once the person says Yes),
 * so it is always rebuilt from the parsed scheme, host, port and path, never taken as typed.
 */
data class LinkCode(val server: String, val code: String)

/** What a scanned QR code, or an opened clrd:// link, asks for. */
sealed interface Scan {
    data class Link(val link: LinkCode) : Scan
    data class Pair(val link: PairLink) : Scan
    data object Bad : Scan
}

/**
 * Routes scanned text:
 * `https://<host>/<base>/link?c=<code>` and `clrd://link?server=..&code=..` link this phone,
 * `https://<host>/<base>/pair?c=<code>` and `clrd://pair?..` sign a browser in (Flow B), anything else is [Scan.Bad].
 */
fun parseScan(text: String): Scan {
    val s = text.trim()
    parseClrdLink(s, "link")?.let { p -> return serverBase(p.server)?.let { Scan.Link(LinkCode(it, p.code)) } ?: Scan.Bad }
    parsePairLink(s)?.let { return Scan.Pair(it) }
    val u = runCatching { URI(s) }.getOrNull() ?: return Scan.Bad
    if (u.scheme == null || u.scheme.lowercase() !in WEB) return Scan.Bad
    val segments = u.rawPath.orEmpty().split('/').filter { it.isNotEmpty() }
    val route = segments.lastOrNull()
    if (route != "link" && route != "pair") return Scan.Bad
    val code = queryParams(u)?.get("c")?.takeIf { CODE.matches(it) } ?: return Scan.Bad
    val base = base(u, segments.dropLast(1)) ?: return Scan.Bad
    return if (route == "link") Scan.Link(LinkCode(base, code)) else Scan.Pair(PairLink(base, code))
}

private val WEB = setOf("http", "https")

/** An http(s) server address in the form the app saves it, or null when it is not one. */
private fun serverBase(server: String): String? {
    val u = runCatching { URI(server) }.getOrNull() ?: return null
    if (u.scheme == null || u.scheme.lowercase() !in WEB) return null
    return base(u, u.rawPath.orEmpty().split('/').filter { it.isNotEmpty() })
}

/** Scheme, host, port and [mount] path only: user info, query and fragment are dropped. */
private fun base(u: URI, mount: List<String>): String? {
    val host = u.host?.lowercase() ?: return null
    val port = if (u.port == -1) "" else ":${u.port}"
    return "${u.scheme.lowercase()}://$host$port" + mount.joinToString("") { "/$it" }
}

/** The secret this phone proves the claim with on every poll. 32 random bytes, 43 characters. */
fun newLinkSecret(): String {
    val b = ByteArray(32)
    SecureRandom().nextBytes(b)
    return Base64.getUrlEncoder().withoutPadding().encodeToString(b)
}

/**
 * The 4-digit match number both screens show, so the browser approves this phone and not one that
 * photographed the code first. Same as the server: SHA-256 of the secret, first 4 bytes big-endian, mod 10000.
 */
fun linkCheck(secret: String): String {
    val h = MessageDigest.getInstance("SHA-256").digest(secret.toByteArray(Charsets.UTF_8))
    val n = ((h[0].toLong() and 0xff) shl 24) or ((h[1].toLong() and 0xff) shl 16) or
        ((h[2].toLong() and 0xff) shl 8) or (h[3].toLong() and 0xff)
    return "%04d".format(n % 10000)
}

/** Whether a failed claim poll is worth another try: the network, a proxy hiccup or a busy server, not a refusal. */
fun claimRetries(e: Exception): Boolean = e !is HttpError || e.code >= 500 || e.code == 408 || e.code == 429

/** The name the browser shows when asked to approve this phone. */
fun deviceName(ctx: Context): String =
    Settings.Global.getString(ctx.contentResolver, Settings.Global.DEVICE_NAME)?.takeIf { it.isNotBlank() } ?: Build.MODEL
