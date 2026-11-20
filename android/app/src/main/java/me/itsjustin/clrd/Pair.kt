package me.itsjustin.clrd

import java.net.URI
import java.net.URLDecoder

// Flow B (issue #74): a browser shows a QR code, its page opens clrd://pair?server=...&code=..., this phone approves.

/** A browser asking this phone to sign it in. [server] is only compared, never called: requests go to the configured server. */
data class PairLink(val server: String, val code: String)

private val CODE = Regex("[A-Za-z0-9]{16,64}")

/** The pairing link from `clrd://pair?server=<url-encoded>&code=<code>`, or null when it is anything else. */
fun parsePairLink(uri: String): PairLink? {
    val u = runCatching { URI(uri) }.getOrNull() ?: return null
    if (!u.scheme.equals("clrd", ignoreCase = true) || !u.host.equals("pair", ignoreCase = true)) return null
    val params = mutableMapOf<String, String>()
    for (part in u.rawQuery.orEmpty().split('&')) {
        val i = part.indexOf('=')
        if (i < 0) continue
        val value = runCatching { URLDecoder.decode(part.substring(i + 1), "UTF-8") }.getOrNull() ?: return null
        if (params.put(part.substring(0, i), value) != null) return null // a repeated key is a crafted link
    }
    val server = params["server"] ?: return null
    val code = params["code"] ?: return null
    if (!CODE.matches(code)) return null
    if (!server.startsWith("https://", ignoreCase = true) && !server.startsWith("http://", ignoreCase = true)) return null
    return PairLink(server, code)
}

enum class PairGate { NOT_SET_UP, OTHER_SERVER, OK }

/**
 * Whether a link may be acted on with this app's settings. Anything but OK means no request at all,
 * because the request carries this phone's token.
 */
fun pairGate(link: PairLink, serverUrl: String, token: String): PairGate = when {
    serverUrl.isBlank() || token.isBlank() -> PairGate.NOT_SET_UP
    serverAddress(link.server) != serverAddress(serverUrl) -> PairGate.OTHER_SERVER
    else -> PairGate.OK
}
