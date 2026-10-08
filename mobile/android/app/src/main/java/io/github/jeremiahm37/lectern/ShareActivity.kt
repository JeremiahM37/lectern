package io.github.jeremiahm37.lectern

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.widget.Toast
import org.json.JSONArray
import org.json.JSONObject

/**
 * The share sheet's way into a session: pick the session, upload each image
 * to /api/sessions/{id}/attachments, put the quoted paths in the session as a
 * message, and mirror the first image into the clipboard of the machine the
 * session runs on (PUT /api/clipboard/mirror). Direct pairings only.
 */
class ShareActivity : Activity() {
    private data class Row(val id: Long, val label: String)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val host = Hosts(this).active()
        val uris = uris(intent)
        when {
            host == null || !host.paired -> done(R.string.share_not_paired)
            !ClipboardNet.direct(host) -> done(R.string.share_relay)
            uris.isEmpty() -> finish()
            else -> Thread { choose(host, uris) }.start()
        }
    }

    private fun uris(i: Intent): List<Uri> {
        @Suppress("DEPRECATION")
        val list: List<Uri> = when (i.action) {
            Intent.ACTION_SEND -> listOfNotNull(i.getParcelableExtra<Uri>(Intent.EXTRA_STREAM))
            Intent.ACTION_SEND_MULTIPLE -> i.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM) ?: emptyList()
            else -> emptyList()
        }
        return list.take(10)
    }

    private fun choose(host: Host, uris: List<Uri>) {
        val (status, body) = ClipboardNet.call(this, host, "GET", "/api/sessions", mapOf("Accept" to "application/json"))
        val rows = if (status == 200) parseSessions(body) else emptyList()
        runOnUiThread {
            if (rows.isEmpty()) return@runOnUiThread done(R.string.share_no_sessions)
            AlertDialog.Builder(this)
                .setTitle(R.string.share_title)
                .setItems(rows.map { it.label }.toTypedArray()) { _, which -> Thread { send(host, rows[which].id, uris) }.start() }
                .setOnCancelListener { finish() }
                .show()
        }
    }

    private fun send(host: Host, session: Long, uris: List<Uri>) {
        try {
            val paths = StringBuilder()
            var first: ClipboardImage.Image? = null
            for ((n, uri) in uris.withIndex()) {
                val mime = contentResolver.getType(uri) ?: "image/png"
                val bytes = contentResolver.openInputStream(uri)?.use { ClipboardImage.readCapped(it) }
                if (bytes == null || !ClipboardLogic.imageEligible(mime, bytes.size.toLong())) throw IllegalStateException("image is empty or over 20 MB")
                val image = ClipboardImage.acceptable(ClipboardImage.Image(mime, bytes)) ?: throw IllegalStateException("not a readable image")
                val boundary = "lectern" + System.nanoTime()
                val ext = image.mime.substringAfter('/').substringBefore('+')
                val (code, text) = ClipboardNet.call(
                    this, host, "POST", "/api/sessions/$session/attachments",
                    mapOf("Content-Type" to "multipart/form-data; boundary=$boundary", "Accept" to "application/json"),
                    ClipboardLogic.multipart(boundary, "shared-${n + 1}.$ext", image.mime, image.bytes),
                )
                if (code !in 200..299) throw IllegalStateException(Actions.detail(text).ifEmpty { "HTTP $code" })
                paths.append(ClipboardLogic.quotePath(JSONObject(text).getString("path")))
                if (first == null) first = image
            }
            first?.let { ClipboardNet.call(this, host, "PUT", "/api/clipboard/mirror?session=$session", mapOf("Content-Type" to it.mime), it.bytes) }
            val (code, text) = ClipboardNet.call(
                this, host, "POST", "/api/sessions/$session/send",
                mapOf("Content-Type" to "application/json"), JSONObject().put("text", paths.toString().trim()).toString().toByteArray(),
            )
            if (code !in 200..299) throw IllegalStateException(Actions.detail(text).ifEmpty { "HTTP $code" })
            runOnUiThread { done(R.string.share_sent) }
        } catch (e: Exception) {
            runOnUiThread { Toast.makeText(this, getString(R.string.share_failed, e.message ?: e.toString()), Toast.LENGTH_LONG).show(); finish() }
        }
    }

    private fun done(message: Int) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
        finish()
    }

    companion object {
        /** Live sessions, newest first, as picker rows; ended and archived ones are left out. */
        private fun parseSessions(body: String): List<Row> = runCatching {
            val arr = JSONArray(body)
            (0 until arr.length()).map { arr.getJSONObject(it) }
                .filter { it.optString("state") != "ended" && it.isNull("ended_at") }
                .sortedByDescending { it.optLong("id") }
                .map { Row(it.getLong("id"), "#${it.getLong("id")}  ${it.optString("name").ifEmpty { it.optString("agent") }}") }
        }.getOrDefault(emptyList())
    }
}
