package io.github.jeremiahm37.lectern

import android.content.Context
import android.net.Uri
import android.webkit.MimeTypeMap
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import java.io.ByteArrayInputStream

/**
 * Serves the Lectern web app from the APK's own assets (the build copies
 * web/index.html and web/static, the same files the Go binary embeds).
 *
 * The WebView's origin is either the Lectern host itself (direct mode: API
 * calls, SSE and terminals go to the network as usual, but every app file
 * comes from here) or [APP_ORIGIN] (relay mode: nothing on that origin ever
 * reaches the network; the page talks to Lectern only through the encrypted
 * tunnel; see [relayOrigin]). Either way the code that runs is the code this APK was signed
 * with, never code served by a relay or an install origin.
 */
class Shell(context: Context) {
    private val assets = context.applicationContext.assets
    private val files: Set<String> by lazy { list("shell").map { it.removePrefix("shell") }.toSet() }

    private fun list(dir: String): List<String> {
        val children = assets.list(dir) ?: return emptyList()
        if (children.isEmpty()) return listOf(dir)
        return children.flatMap { list("$dir/$it") }
    }

    /** The app file (or client-side route) a request asks for, if any. */
    fun resolve(path: String, method: String): String? {
        if (method != "GET" && method != "HEAD") return null
        if (path == "/" || path == "/index.html") return "/index.html"
        val file = "/static$path"
        if (file in files) return file
        return if (isAppRoute(path)) "/index.html" else null
    }

    fun respond(request: WebResourceRequest, relayOrigin: Boolean): WebResourceResponse? {
        val url = request.url
        val file = resolve(url.path ?: "/", request.method)
        if (file == null) {
            // The synthetic relay origin must never touch the network.
            return if (relayOrigin) notFound() else null
        }
        val mime = mimeOf(file)
        val body = assets.open("shell$file")
        return WebResourceResponse(mime, if (mime.startsWith("text/") || mime.endsWith("javascript") || mime.endsWith("json")) "utf-8" else null,
            200, "OK", mapOf("Cache-Control" to "no-cache"), body)
    }

    companion object {
        /** The relay-mode origin of app 0.1.0's one pairing. It exists only
         * inside this WebView; later relay pairings each get a subdomain of
         * it ([relayOrigin]), so their pages keep separate storage. */
        const val APP_ORIGIN = "https://app.lectern.invalid"

        fun relayOrigin(hostId: String) = "https://$hostId.app.lectern.invalid"

        /** An origin that must never reach the network. */
        fun isRelayOrigin(origin: String): Boolean {
            val u = runCatching { java.net.URI(origin) }.getOrNull() ?: return false
            val host = u.host ?: return false
            return u.scheme == "https" && (host == "app.lectern.invalid" || host.endsWith(".app.lectern.invalid"))
        }

        /** Mirrors internal/api/server.go appRoute: extensionless paths that
         * are not API-shaped are client-side routes of the one-page app. */
        fun isAppRoute(path: String): Boolean {
            val p = path.removePrefix("/")
            if (p.isEmpty() || p.substringAfterLast('/').contains('.')) return false
            for (prefix in listOf("api/", "term/", "a2a/", ".well-known/", "static/")) {
                if ("$p/".startsWith(prefix)) return false
            }
            return true
        }

        fun mimeOf(file: String): String = when (file.substringAfterLast('.', "")) {
            "html" -> "text/html"
            "js", "mjs" -> "text/javascript"
            "css" -> "text/css"
            "svg" -> "image/svg+xml"
            "json", "webmanifest" -> "application/json"
            "woff2" -> "font/woff2"
            "woff" -> "font/woff"
            "wasm" -> "application/wasm"
            else -> MimeTypeMap.getSingleton().getMimeTypeFromExtension(file.substringAfterLast('.', "")) ?: "application/octet-stream"
        }

        fun originOf(url: String): String {
            val u = Uri.parse(url)
            val port = if (u.port == -1) "" else ":${u.port}"
            return "${u.scheme}://${u.host}$port"
        }

        fun notFound() = WebResourceResponse("text/plain", "utf-8", 404, "Not Found", emptyMap(), ByteArrayInputStream(ByteArray(0)))
    }
}
