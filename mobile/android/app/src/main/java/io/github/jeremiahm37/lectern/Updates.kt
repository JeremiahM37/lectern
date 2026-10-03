package io.github.jeremiahm37.lectern

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.os.Build
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import java.util.concurrent.Executors

/**
 * Updating the app from inside it (docs/android.md, "Updates").
 *
 * Every Lectern release carries `lectern-android.json` beside the APK:
 * `{"version": "2.9.0", "versionCode": 20900, "apk": "<url>", "sha256": "…",
 * "size": 1234}`. GitHub's `releases/latest/download/` link always names the
 * newest release's copy, so the app needs no API and no rate limit. The APK
 * must come from this repository's release downloads, must match the
 * manifest's SHA-256, and Android itself refuses it unless it is signed with
 * the same key as the installed app, so a tampered manifest can at most
 * offer a version that will not install.
 */
object Updates {
    private val MANIFEST_URL = BuildConfig.UPDATE_MANIFEST
    private val APK_PREFIX = BuildConfig.UPDATE_APK_PREFIX
    private const val ACTION_STATUS = "io.github.jeremiahm37.lectern.UPDATE_STATUS"
    private val worker = Executors.newSingleThreadExecutor()
    @Volatile private var busy = false

    data class Release(val version: String, val versionCode: Long, val apk: String, val sha256: String, val size: Long)

    /** Parses and checks a manifest; null when it is not one this app accepts. */
    fun parse(json: String): Release? = runCatching {
        val o = JSONObject(json)
        val r = Release(o.getString("version"), o.getLong("versionCode"), o.getString("apk"), o.getString("sha256").lowercase(), o.optLong("size"))
        r.takeIf {
            it.apk.startsWith(APK_PREFIX) && !it.apk.contains("..") && it.apk.endsWith(".apk") &&
                it.sha256.matches(Regex("^[0-9a-f]{64}$")) && it.versionCode > 0
        }
    }.getOrNull()

    /** The answer the page gets (frontend/src/native/update.ts). */
    private fun status(state: String, release: Release? = null, error: String? = null, progress: Int? = null) =
        JSONObject().put("state", state).put("current", BuildConfig.VERSION_NAME).apply {
            if (release != null) put("latest", release.version).put("size", release.size)
            if (error != null) put("error", error)
            if (progress != null) put("progress", progress)
        }

    /** The person did not allow installs from this app. */
    fun blocked(message: String) = status("error", error = message)

    private fun get(url: String): HttpURLConnection = (URL(url).openConnection() as HttpURLConnection).apply {
        connectTimeout = 15000
        readTimeout = 30000
        instanceFollowRedirects = true
        setRequestProperty("User-Agent", "Lectern-Android/${BuildConfig.VERSION_NAME}")
    }

    private fun latest(): Release {
        val conn = get(MANIFEST_URL)
        try {
            if (conn.responseCode == 404) throw IllegalStateException("The latest release has no Android app.")
            if (conn.responseCode != 200) throw IllegalStateException("GitHub answered ${conn.responseCode}.")
            val text = conn.inputStream.bufferedReader().use { it.readText() }
            return parse(text) ?: throw IllegalStateException("The release's app manifest is not valid.")
        } finally {
            conn.disconnect()
        }
    }

    /** Looks for a newer release; [report] gets "current", "available" or "error". */
    fun check(report: (JSONObject) -> Unit) = worker.execute {
        val result = runCatching { latest() }
        report(result.fold(
            { r -> if (r.versionCode > BuildConfig.VERSION_CODE) status("available", r) else status("current", r) },
            { e -> status("error", error = e.message ?: e.toString()) },
        ))
    }

    /** Downloads, verifies and hands the newest APK to Android's installer,
     * which asks the person to confirm. [report] follows each step. */
    fun install(context: Context, report: (JSONObject) -> Unit) {
        if (busy) return
        busy = true
        val app = context.applicationContext
        worker.execute {
            var release: Release? = null
            try {
                val r = latest().also { release = it }
                if (r.versionCode <= BuildConfig.VERSION_CODE) {
                    report(status("current", r))
                    return@execute
                }
                val dir = File(app.cacheDir, "updates").apply { deleteRecursively(); mkdirs() }
                val file = File(dir, "lectern-${r.version}.apk")
                val digest = MessageDigest.getInstance("SHA-256")
                val conn = get(r.apk)
                try {
                    if (conn.responseCode != 200) throw IllegalStateException("The download answered ${conn.responseCode}.")
                    val total = conn.contentLengthLong.takeIf { it > 0 } ?: r.size
                    var done = 0L
                    var shown = -1
                    conn.inputStream.use { input ->
                        file.outputStream().use { out ->
                            val buf = ByteArray(64 * 1024)
                            while (true) {
                                val n = input.read(buf)
                                if (n < 0) break
                                out.write(buf, 0, n)
                                digest.update(buf, 0, n)
                                done += n
                                val pct = if (total > 0) (done * 100 / total).toInt().coerceIn(0, 100) else 0
                                if (pct / 5 != shown / 5) {
                                    shown = pct
                                    report(status("downloading", r, progress = pct))
                                }
                            }
                        }
                    }
                } finally {
                    conn.disconnect()
                }
                val sum = digest.digest().joinToString("") { "%02x".format(it) }
                if (sum != r.sha256) {
                    file.delete()
                    throw IllegalStateException("The download did not match the release's checksum; nothing was installed.")
                }
                report(status("installing", r))
                commit(app, file)
            } catch (e: Exception) {
                report(status("error", release, e.message ?: e.toString()))
            } finally {
                busy = false
            }
        }
    }

    private fun commit(app: Context, file: File) {
        val installer = app.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(app.packageName)
            setSize(file.length())
            if (Build.VERSION.SDK_INT >= 31) setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
        }
        val id = installer.createSession(params)
        installer.openSession(id).use { session ->
            session.openWrite("lectern.apk", 0, file.length()).use { out ->
                file.inputStream().use { it.copyTo(out) }
                session.fsync(out)
            }
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or (if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else 0)
            val intent = Intent(app, UpdateReceiver::class.java).setAction(ACTION_STATUS)
            session.commit(PendingIntent.getBroadcast(app, id, intent, flags).intentSender)
        }
    }

    /** Android's answers about the install session. */
    class UpdateReceiver : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            if (intent.action != ACTION_STATUS) return
            when (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
                PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                    @Suppress("DEPRECATION")
                    val confirm = (if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                        else intent.getParcelableExtra(Intent.EXTRA_INTENT)) ?: return
                    context.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                }
                PackageInstaller.STATUS_SUCCESS -> Unit // The app restarts as the new version.
                else -> {
                    val message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: "Android did not install the update."
                    Pages.update(status("error", error = message))
                }
            }
        }
    }
}
