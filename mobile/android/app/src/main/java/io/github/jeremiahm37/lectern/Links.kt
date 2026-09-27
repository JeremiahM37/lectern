package io.github.jeremiahm37.lectern

import java.net.URI
import java.net.URLDecoder

/** What a scanned QR code, a pasted text or a tapped link asks the app to do. */
sealed class Link {
    /** Settings → Devices → Encrypted relay: everything is in the payload. */
    data class Relay(val fragment: String) : Link()

    /** Settings → Devices → Pair a phone: a one-time code for this host. */
    data class DirectPair(val origin: String, val code: String) : Link()

    /** Just an address, for a phone Lectern already admits (Tailscale identity). */
    data class Direct(val origin: String) : Link()

    /** Where this link connects, as shown before the person agrees. */
    fun describe(): String = when (this) {
        is Relay -> "a Lectern through its encrypted relay"
        is DirectPair -> origin
        is Direct -> origin
    }

    companion object {
        private val relayFragment = Regex("[#&]p=([A-Za-z0-9_-]+)")
        private val codeFragment = Regex("[#&]code=([A-Za-z0-9%-]+)")
        private val relayPayload = Regex("^[A-Za-z0-9_-]+$")
        private val code = Regex("^[A-Za-z0-9%-]+$")

        fun parse(text: String): Link? {
            val raw = text.trim()
            val uri = runCatching { URI(raw) }.getOrNull() ?: return null
            val scheme = uri.scheme?.lowercase()
            if (scheme == "lectern") return parseApp(uri)
            if ((scheme != "http" && scheme != "https") || uri.host.isNullOrEmpty()) return null
            val origin = originOf(uri) ?: return null
            val fragment = raw.substringAfter('#', "").let { if (it.isEmpty()) "" else "#$it" }
            val path = uri.path.orEmpty()
            if (path.endsWith("/relay-pair")) {
                return relayFragment.find(fragment)?.let { Relay(it.groupValues[1]) }
            }
            if (path.endsWith("/pair")) {
                return codeFragment.find(fragment)?.let { DirectPair(origin, URLDecoder.decode(it.groupValues[1], "UTF-8")) }
            }
            return Direct(origin)
        }

        /** lectern://pair?p=… or lectern://pair?origin=…&code=… (frontend/src/pairing/links.ts). */
        private fun parseApp(uri: URI): Link? {
            if (uri.host != "pair") return null
            val query = (uri.rawQuery ?: "").split('&').filter { it.isNotEmpty() }.associate {
                val (k, v) = (it.split('=', limit = 2) + "").take(2)
                k to URLDecoder.decode(v, "UTF-8")
            }
            query["p"]?.let { return if (relayPayload.matches(it)) Relay(it) else null }
            val origin = query["origin"]?.let { runCatching { URI(it) }.getOrNull() }?.let { originOf(it) } ?: return null
            val pairCode = query["code"] ?: return null
            return if (code.matches(pairCode)) DirectPair(origin, pairCode) else null
        }

        private fun originOf(uri: URI): String? {
            val scheme = uri.scheme?.lowercase()
            if ((scheme != "http" && scheme != "https") || uri.host.isNullOrEmpty()) return null
            return scheme + "://" + uri.host + (if (uri.port == -1) "" else ":${uri.port}")
        }
    }
}
