package io.github.jeremiahm37.lectern

import android.content.Context
import java.net.HttpURLConnection
import java.net.URL

/** Binary HTTP to a directly paired Lectern, authenticated as Actions.direct is.
 * A relay pairing has no direct address, so the clipboard bridge does not run there. */
object ClipboardNet {
    fun open(context: Context, host: Host, method: String, path: String, headers: Map<String, String> = emptyMap(), body: ByteArray? = null,
             connectMs: Int = 5000, readMs: Int = 15000): HttpURLConnection {
        val conn = URL(host.origin + path).openConnection() as HttpURLConnection
        conn.requestMethod = method
        conn.connectTimeout = connectMs
        conn.readTimeout = readMs
        Hosts(context).secret(host.id, SecureStore.DEVICE_TOKEN)?.let { conn.setRequestProperty("Authorization", "Bearer $it") }
        for ((k, v) in headers) conn.setRequestProperty(k, v)
        if (body != null) {
            conn.doOutput = true
            conn.setFixedLengthStreamingMode(body.size)
            conn.outputStream.use { it.write(body) }
        }
        return conn
    }

    /** (status, body); status 0 when Lectern could not be reached. */
    fun call(context: Context, host: Host, method: String, path: String, headers: Map<String, String> = emptyMap(), body: ByteArray? = null): Pair<Int, String> = try {
        val conn = open(context, host, method, path, headers, body, readMs = 60000)
        val status = conn.responseCode
        val text = (if (status >= 400) conn.errorStream else conn.inputStream)?.use { it.readBytes().decodeToString() } ?: ""
        conn.disconnect()
        status to text
    } catch (e: Exception) {
        0 to (e.message ?: e.toString())
    }

    fun direct(host: Host?): Boolean = host != null && host.paired && host.mode == Bridge.MODE_DIRECT
}
