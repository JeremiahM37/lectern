package io.github.jeremiahm37.lectern

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.webkit.WebView
import androidx.core.app.NotificationCompat
import androidx.core.app.RemoteInput
import androidx.work.CoroutineWorker
import androidx.work.Data
import androidx.work.ExistingWorkPolicy
import androidx.work.ForegroundInfo
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.OutOfQuotaPolicy
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.util.UUID
import java.util.concurrent.atomic.AtomicReference

/**
 * Notification buttons, handled with no Lectern screen open.
 *
 * The receiver answers the tap at once ("Approving…") and hands the call to
 * an expedited WorkManager job. The job makes the same authenticated API call
 * the web app would: directly with this device's paired credential, or
 * through the encrypted relay by loading the app's own /native-action page in
 * an unseen WebView (the Noise and tunnel code stay the one implementation in
 * frontend/src/relay). Lectern decides whether the caller may decide the
 * approval, exactly as for the web app: a paired device is its owner's device.
 */
object Actions {
    const val APPROVE = "approve"
    const val DENY = "deny"
    const val REPLY_APPROVAL = "reply-approval"
    const val REPLY_SESSION = "reply-session"
    const val SUBSCRIBE = "subscribe"

    private const val EXTRA_ID = "notification_id"
    private const val EXTRA_TAG = "tag"
    private const val EXTRA_ACTION = "action"
    private const val EXTRA_TARGET = "target"
    private const val EXTRA_ABOUT = "about"
    private const val EXTRA_HOST = "host"

    fun intent(context: Context, id: Int, host: Host, tag: String, action: String, target: Long, about: String, mutable: Boolean = false): PendingIntent {
        val intent = Intent(context, ActionReceiver::class.java)
            .setAction("$action:$tag")
            .putExtra(EXTRA_ID, id)
            .putExtra(EXTRA_HOST, host.id)
            .putExtra(EXTRA_TAG, tag)
            .putExtra(EXTRA_ACTION, action)
            .putExtra(EXTRA_TARGET, target)
            .putExtra(EXTRA_ABOUT, about)
        val flags = PendingIntent.FLAG_UPDATE_CURRENT or (if (mutable) PendingIntent.FLAG_MUTABLE else PendingIntent.FLAG_IMMUTABLE)
        return PendingIntent.getBroadcast(context, (action + tag).hashCode(), intent, flags)
    }

    /** What an action does: the request, and how to describe it. */
    data class Plan(val method: String, val path: String, val body: JSONObject?, val doing: String, val done: String)

    fun plan(action: String, target: Long, reply: String?): Plan? = when (action) {
        APPROVE -> Plan("POST", "/api/approvals/$target/decision", JSONObject().put("decision", "approved"), "Approving…", "Approved")
        DENY -> Plan("POST", "/api/approvals/$target/decision",
            JSONObject().put("decision", "denied").put("note", "denied from the Lectern app"), "Denying…", "Denied")
        REPLY_APPROVAL -> reply?.takeIf { it.isNotBlank() }?.let {
            Plan("POST", "/api/approvals/$target/decision", JSONObject().put("decision", "denied").put("note", it), "Sending…", "Denied with your note")
        }
        REPLY_SESSION -> reply?.takeIf { it.isNotBlank() }?.let {
            Plan("POST", "/api/sessions/$target/send", JSONObject().put("text", it), "Sending…", "Reply sent")
        }
        else -> null
    }

    fun handle(context: Context, intent: Intent) {
        val id = intent.getIntExtra(EXTRA_ID, 0)
        val action = intent.getStringExtra(EXTRA_ACTION) ?: return
        val host = Hosts(context).get(intent.getStringExtra(EXTRA_HOST)) ?: return
        val target = intent.getLongExtra(EXTRA_TARGET, 0)
        val reply = RemoteInput.getResultsFromIntent(intent)?.getCharSequence(Notifications.REPLY_KEY)?.toString()
        val plan = plan(action, target, reply) ?: return
        val about = intent.getStringExtra(EXTRA_ABOUT).orEmpty()
        Notifications.result(context, id, host, plan.doing, about, ongoing = true)
        enqueue(context, "action-$id", Data.Builder()
            .putInt(EXTRA_ID, id)
            .putString(EXTRA_HOST, host.id)
            .putString(EXTRA_ABOUT, about)
            .putString("method", plan.method)
            .putString("path", plan.path)
            .putString("body", plan.body?.toString())
            .putString("done", plan.done)
            .build())
    }

    /** Registers a new push endpoint with a Lectern while no page of it is open. */
    fun enqueueSubscribe(context: Context, host: Host) {
        val sub = Push.subscription(context, host) ?: return
        val body = JSONObject().put("endpoint", sub.getString("endpoint")).put("keys", sub.getJSONObject("keys"))
        enqueue(context, "push-subscribe-${host.id}", Data.Builder()
            .putString(EXTRA_HOST, host.id)
            .putString("method", "POST")
            .putString("path", "/api/push/subscribe")
            .putString("body", body.toString())
            .putString("subscribe", sub.getString("endpoint"))
            .build())
    }

    private fun enqueue(context: Context, name: String, data: Data) {
        val work = OneTimeWorkRequestBuilder<ActionWorker>()
            .setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
            .setInputData(data)
            .build()
        WorkManager.getInstance(context).enqueueUniqueWork(name, ExistingWorkPolicy.REPLACE, work)
    }

    /** One API call to a paired Lectern, as this device. Returns (HTTP
     * status, body); status 0 is a failure to reach Lectern at all. */
    suspend fun call(context: Context, host: Host?, method: String, path: String, body: String?): Pair<Int, String> {
        if (host == null || !host.paired) return 0 to "This app is no longer paired with that Lectern."
        return when (host.mode) {
            Bridge.MODE_DIRECT -> withContext(Dispatchers.IO) { direct(context, host, method, path, body) }
            Bridge.MODE_RELAY -> relay(context, host, method, path, body)
            else -> 0 to "This app is not connected to a Lectern."
        }
    }

    private fun direct(context: Context, host: Host, method: String, path: String, body: String?): Pair<Int, String> = try {
        val conn = URL(host.origin + path).openConnection() as HttpURLConnection
        conn.requestMethod = method
        conn.connectTimeout = 15000
        conn.readTimeout = 20000
        conn.setRequestProperty("Accept", "application/json")
        // A paired device's bearer token; without one (a phone Lectern admits
        // by its Tailscale identity) the request carries no credential at all.
        Hosts(context).secret(host.id, SecureStore.DEVICE_TOKEN)?.let { conn.setRequestProperty("Authorization", "Bearer $it") }
        if (body != null) {
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.outputStream.use { it.write(body.toByteArray()) }
        }
        val status = conn.responseCode
        val text = (if (status >= 400) conn.errorStream else conn.inputStream)?.use { it.readBytes().decodeToString() } ?: ""
        status to text
    } catch (e: Exception) {
        0 to (e.message ?: e.toString())
    }

    /** Runs the call through the encrypted relay in an unseen WebView. */
    private suspend fun relay(context: Context, target: Host, method: String, path: String, body: String?): Pair<Int, String> {
        val requestId = UUID.randomUUID().toString()
        val spec = JSONObject().put("id", requestId).put("method", method).put("path", path)
        if (body != null) spec.put("body", JSONObject(body))
        val result = CompletableDeferred<JSONObject>()
        val origin = AtomicReference<String?>(null)
        val owner = object : Bridge.Owner {
            override val pageOrigin: String? get() = origin.get()
            override fun actionResult(json: JSONObject) {
                if (json.optString("id") == requestId) result.complete(json)
            }
        }
        val view = withContext(Dispatchers.Main) {
            WebView(context.applicationContext).also { v ->
                WebShell.configure(v, Bridge(context, owner) { target.id }, origin = { target.origin }, onOrigin = { origin.set(it) }, onExternal = {})
                v.loadUrl(target.origin + "/native-action#a=" + DeviceKey.b64(spec.toString().toByteArray()))
            }
        }
        return try {
            val json = withTimeout(60000) { result.await() }
            json.optInt("status") to json.optString("body")
        } catch (e: Exception) {
            0 to "Lectern did not answer through the relay in time."
        } finally {
            Handler(Looper.getMainLooper()).post { view.destroy() }
        }
    }

    /** "detail" from Lectern's {"detail": ...} errors, else the raw text. */
    fun detail(body: String): String = runCatching { JSONObject(body).optString("detail") }.getOrNull()?.takeIf { it.isNotEmpty() } ?: body.take(200)
}

class ActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) = Actions.handle(context, intent)
}

class ActionWorker(context: Context, params: WorkerParameters) : CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        val id = inputData.getInt("notification_id", 0)
        val host = Hosts(applicationContext).get(inputData.getString("host"))
        val (status, body) = Actions.call(applicationContext, host, inputData.getString("method")!!, inputData.getString("path")!!, inputData.getString("body"))
        Log.i("LecternAction", "${host?.id} ${inputData.getString("path")} -> $status")
        inputData.getString("subscribe")?.let { endpoint ->
            if (host == null) return Result.success()
            if (status in 200..299) Push.confirmed(applicationContext, host, endpoint)
            return if (status in 200..299) Result.success() else Result.retry()
        }
        if (status in 200..299) {
            Notifications.result(applicationContext, id, host, inputData.getString("done") ?: "Done", inputData.getString("about").orEmpty())
        } else {
            val why = if (status == 0) body else "Lectern answered $status: ${Actions.detail(body)}"
            Notifications.result(applicationContext, id, host, "Could not complete that", why)
        }
        return Result.success()
    }

    override suspend fun getForegroundInfo(): ForegroundInfo {
        val n = NotificationCompat.Builder(applicationContext, Notifications.CHANNEL_ACTIVITY)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle("Talking to Lectern…")
            .build()
        return ForegroundInfo(0x4c45, n)
    }
}
