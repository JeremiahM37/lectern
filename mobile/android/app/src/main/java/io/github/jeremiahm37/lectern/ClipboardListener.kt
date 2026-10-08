package io.github.jeremiahm37.lectern

import android.content.Context
import android.util.Log
import java.io.BufferedReader
import java.io.InputStreamReader
import java.net.HttpURLConnection
import java.util.UUID
import java.util.concurrent.Executors

/**
 * While the app is visible (MainActivity.onStart..onStop) this holds the
 * server's /api/clipboard/listen stream open and answers its requests from
 * the real Android clipboard. Android only releases the clipboard to the app
 * that has focus, so [focused] gates every answer; otherwise "unavailable".
 */
class ClipboardListener(
    context: Context,
    private val host: () -> Host?,
    private val focused: () -> Boolean,
) {
    private val app = context.applicationContext
    private val answers = Executors.newCachedThreadPool()
    private val hosts = Hosts(app)
    @Volatile private var running = false
    @Volatile private var thread: Thread? = null
    @Volatile private var conn: HttpURLConnection? = null
    @Volatile private var session: String? = null
    @Volatile private var lastActive = 0L

    private val client: String by lazy {
        val prefs = app.getSharedPreferences("clipboard", Context.MODE_PRIVATE)
        prefs.getString("client", null)?.takeIf { ClipboardLogic.validClient(it) }
            ?: ("android-" + UUID.randomUUID().toString().replace("-", "").take(16)).also { prefs.edit().putString("client", it).apply() }
    }

    @Synchronized fun start() {
        if (running) return
        running = true
        thread = Thread({ loop() }, "lectern-clipboard").also { it.isDaemon = true; it.start() }
    }

    @Synchronized fun stop() {
        running = false
        conn?.disconnect()
        thread?.interrupt()
        thread = null
    }

    /** Reconnects (another Lectern, or a session terminal opened or closed). */
    fun restart() { if (running) { conn?.disconnect() } }

    fun setSession(id: String?) {
        val clean = id?.takeIf { it.isNotEmpty() }
        if (clean != session) { session = clean; restart() }
    }

    /** The person touched or typed: tell Lectern which device they are on (at most every 2 s). */
    fun active() {
        val now = System.currentTimeMillis()
        if (!running || !ClipboardLogic.activeDue(now, lastActive)) return
        lastActive = now
        val h = host()?.takeIf { ClipboardNet.direct(it) } ?: return
        answers.execute { ClipboardNet.call(app, h, "POST", "/api/clipboard/active?client=$client") }
    }

    private fun loop() {
        var backoff = 1000L
        while (running) {
            val h = host()?.takeIf { ClipboardNet.direct(it) }
            if (h == null) { sleep(5000); continue }
            try {
                val c = ClipboardNet.open(app, h, "GET", ClipboardLogic.listenPath(client, session), mapOf("Accept" to "text/event-stream"), readMs = 120000)
                conn = c
                if (c.responseCode != 200) { c.disconnect(); throw java.io.IOException("HTTP ${c.responseCode}") }
                backoff = 1000L
                val sse = ClipboardLogic.Sse { event, data -> if (event == "request") handle(h, data) }
                BufferedReader(InputStreamReader(c.inputStream)).use { r ->
                    while (running) sse.line(r.readLine() ?: break)
                }
            } catch (e: Exception) {
                if (running) Log.d("LecternClipboard", "stream: ${e.message}")
            } finally {
                conn?.disconnect()
                conn = null
            }
            if (running) { sleep(backoff); backoff = minOf(backoff * 2, 15000L) }
        }
    }

    private fun sleep(ms: Long) { try { Thread.sleep(ms) } catch (_: InterruptedException) {} }

    private fun handle(h: Host, data: String) {
        val req = ClipboardLogic.parseRequest(data) ?: return
        answers.execute {
            val reply = runCatching { answer(req) }.getOrElse { ClipboardLogic.unavailable() }
            val bytes = reply.body
            ClipboardNet.call(app, h, "POST", "/api/clipboard/respond/${req.id}?client=$client", ClipboardLogic.headers(reply), bytes)
        }
    }

    private fun answer(req: ClipboardLogic.Request): ClipboardLogic.Reply {
        if (!focused()) return ClipboardLogic.unavailable()
        return when (req.op) {
            "list" -> ClipboardLogic.listReply(ClipboardLogic.advertisedTypes(ClipboardImage.types(app)))
            else -> if (req.type == "image/png") {
                val png = ClipboardImage.read(app)?.let { ClipboardImage.asPng(it) }
                ClipboardLogic.readReply("image/png", png?.bytes)
            } else {
                ClipboardLogic.readReply("text/plain", ClipboardImage.text(app)?.toByteArray())
            }
        }
    }
}
