package io.github.jeremiahm37.lectern

import java.net.URI

/** What a scanned QR code or pasted text asks the app to do. */
sealed class Link {
    /** Settings → Devices → Encrypted relay: everything is in the fragment. */
    data class Relay(val fragment: String) : Link()

    /** Settings → Devices → Pair a phone: a one-time code for this host. */
    data class DirectPair(val origin: String, val code: String) : Link()

    /** Just an address, for a phone Lectern already admits (Tailscale identity). */
    data class Direct(val origin: String) : Link()

    companion object {
        private val relayFragment = Regex("[#&]p=([A-Za-z0-9_-]+)")
        private val codeFragment = Regex("[#&]code=([A-Za-z0-9%-]+)")

        fun parse(text: String): Link? {
            val raw = text.trim()
            val uri = runCatching { URI(raw) }.getOrNull() ?: return null
            val scheme = uri.scheme?.lowercase()
            if ((scheme != "http" && scheme != "https") || uri.host.isNullOrEmpty()) return null
            val origin = scheme + "://" + uri.host + (if (uri.port == -1) "" else ":${uri.port}")
            val fragment = raw.substringAfter('#', "").let { if (it.isEmpty()) "" else "#$it" }
            val path = uri.path.orEmpty()
            if (path.endsWith("/relay-pair")) {
                return relayFragment.find(fragment)?.let { Relay(it.groupValues[1]) }
            }
            if (path.endsWith("/pair")) {
                return codeFragment.find(fragment)?.let { DirectPair(origin, java.net.URLDecoder.decode(it.groupValues[1], "UTF-8")) }
            }
            return Direct(origin)
        }
    }
}
