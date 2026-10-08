package io.github.jeremiahm37.lectern

import org.json.JSONObject

/**
 * The pure rules of the clipboard bridge (docs/android.md, "Clipboard and
 * screenshots"): what the server may ask for, what a clipboard counts as
 * holding, and the replies. Nothing here touches Android, so LogicTest runs it.
 */
object ClipboardLogic {
    /** The server's cap on one image (mirror upload and attachment alike). */
    const val MAX_BYTES = 20L * 1024 * 1024

    /** Image types the server accepts for /api/clipboard/mirror. */
    val MIRROR_TYPES = setOf("image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp")

    private val clientId = Regex("^[A-Za-z0-9_.-]{4,64}$")

    fun validClient(id: String?): Boolean = id != null && clientId.matches(id)

    /** One `event: request` from the server. */
    data class Request(val id: String, val op: String, val type: String)

    /** Null for anything that is not a well-formed request (ignored, never answered). */
    fun parseRequest(json: String): Request? {
        val o = runCatching { JSONObject(json) }.getOrNull() ?: return null
        val id = o.optString("id")
        val op = o.optString("op")
        val type = o.optString("type")
        // The id goes into a URL path: only the characters a server id has.
        if (!Regex("^[A-Za-z0-9_.-]{1,128}$").matches(id)) return null
        if (op != "list" && op != "read") return null
        if (op == "read" && type != "image/png" && type != "text/plain") return null
        return Request(id, op, type)
    }

    /** The clipboard's own MIME types -> what to advertise (images are always offered as PNG). */
    fun advertisedTypes(clipboard: List<String>): List<String> {
        val out = mutableListOf<String>()
        if (clipboard.any { it.startsWith("image/") }) out += "image/png"
        if (clipboard.any { it == "text/plain" || it == "text/html" || it == "text/*" }) out += "text/plain"
        return out
    }

    /** Whether [size] bytes of [mime] may be uploaded as an image. */
    fun imageEligible(mime: String?, size: Long): Boolean =
        mime != null && mime.startsWith("image/") && size in 1..MAX_BYTES

    /** True when the bytes can go to the mirror as they are (else convert to PNG first). */
    fun mirrorable(mime: String?): Boolean = mime?.lowercase() in MIRROR_TYPES

    /** The answer to a request: POST /api/clipboard/respond/<id>?client=<client>. */
    data class Reply(val unavailable: Boolean, val types: List<String>, val contentType: String, val body: ByteArray)

    fun unavailable() = Reply(true, emptyList(), "application/octet-stream", ByteArray(0))

    fun listReply(types: List<String>) = Reply(false, types, "text/plain", (types.joinToString("\n").ifEmpty { "\n" }).toByteArray())

    fun readReply(type: String, bytes: ByteArray?) =
        if (bytes == null || bytes.isEmpty() || bytes.size > MAX_BYTES) unavailable() else Reply(false, emptyList(), type, bytes)

    fun headers(reply: Reply): Map<String, String> = buildMap {
        put("Content-Type", reply.contentType)
        if (reply.unavailable) put("X-Clipboard-Status", "unavailable")
        else if (reply.types.isNotEmpty()) put("X-Clipboard-Types", reply.types.joinToString(","))
        else put("X-Clipboard-Types", "")
    }

    fun listenPath(client: String, session: String?): String =
        "/api/clipboard/listen?client=$client&kind=android&can_read=1" +
            (session?.takeIf { Regex("^[0-9]{1,18}$").matches(it) }?.let { "&session=$it" } ?: "")

    /** `'path'` quoting for a shell/agent prompt, plain when nothing needs it; always plus a trailing space. */
    fun quotePath(path: String): String {
        val plain = Regex("^[A-Za-z0-9_./:+@%,=-]+$").matches(path)
        return (if (plain) path else "'" + path.replace("'", "'\\''") + "'") + " "
    }

    /** Server-Sent Events, one line at a time. */
    class Sse(private val onEvent: (event: String, data: String) -> Unit) {
        private var event = "message"
        private val data = StringBuilder()
        private var has = false

        fun line(raw: String) {
            val line = raw.trimEnd('\r')
            when {
                line.isEmpty() -> {
                    if (has) onEvent(event, data.toString())
                    event = "message"; data.setLength(0); has = false
                }
                line.startsWith(":") -> {}
                else -> {
                    val i = line.indexOf(':')
                    val field = if (i < 0) line else line.substring(0, i)
                    val value = if (i < 0) "" else line.substring(i + 1).removePrefix(" ")
                    when (field) {
                        "event" -> event = value
                        "data" -> { if (has) data.append('\n'); data.append(value); has = true }
                    }
                }
            }
        }
    }

    /** A multipart/form-data body with a single "file" part. */
    fun multipart(boundary: String, filename: String, mime: String, bytes: ByteArray): ByteArray {
        val safe = filename.replace(Regex("[^A-Za-z0-9_.-]"), "_").ifEmpty { "image" }
        val head = "--$boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"$safe\"\r\nContent-Type: $mime\r\n\r\n"
        return head.toByteArray() + bytes + "\r\n--$boundary--\r\n".toByteArray()
    }

    /** Minimum gap between POST /api/clipboard/active calls. */
    const val ACTIVE_GAP_MS = 2000L

    fun activeDue(now: Long, last: Long): Boolean = now - last >= ACTIVE_GAP_MS
}
