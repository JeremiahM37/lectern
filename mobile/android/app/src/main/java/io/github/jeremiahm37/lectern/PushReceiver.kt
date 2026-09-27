package io.github.jeremiahm37.lectern

import android.content.Context
import android.content.Intent
import android.util.Log
import org.json.JSONObject
import org.unifiedpush.android.connector.FailedReason
import org.unifiedpush.android.connector.MessagingReceiver
import org.unifiedpush.android.connector.data.PushEndpoint
import org.unifiedpush.android.connector.data.PushMessage
import kotlin.concurrent.thread

/**
 * Receives UnifiedPush events from the distributor. Declared with a priority
 * above the connector's built-in receiver, which then stands aside; work runs
 * off the main thread, and any failure is logged rather than lost.
 */
class PushReceiver : MessagingReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val pending = goAsync()
        thread(name = "lectern-push") {
            try {
                super.onReceive(context, intent)
            } catch (t: Throwable) {
                Log.e(TAG, "push event failed", t)
            } finally {
                pending.finish()
            }
        }
    }

    override fun onNewEndpoint(context: Context, endpoint: PushEndpoint, instance: String) {
        val host = Hosts(context).byPushInstance(instance) ?: return
        if (!Push.saveEndpoint(context, host, endpoint)) {
            Pages.pushResult(host.id, false, "The push distributor does not support encrypted (Web Push) messages.")
            return
        }
        Log.i(TAG, "new push endpoint for ${host.id}")
        // An open page of that Lectern registers it straight away; otherwise
        // a background call does (the endpoint can change at any time).
        if (!Pages.pushResult(host.id, true, null)) Actions.enqueueSubscribe(context, host)
    }

    override fun onMessage(context: Context, message: PushMessage, instance: String) {
        if (!message.decrypted) {
            Log.w(TAG, "dropped a push message that did not decrypt")
            return
        }
        val host = Hosts(context).byPushInstance(instance) ?: return
        val data = runCatching { JSONObject(String(message.content)) }.getOrNull() ?: return
        Notifications.show(context, host, data)
    }

    override fun onRegistrationFailed(context: Context, reason: FailedReason, instance: String) {
        val host = Hosts(context).byPushInstance(instance) ?: return
        Pages.pushResult(host.id, false, "Push registration failed ($reason).")
    }

    override fun onUnregistered(context: Context, instance: String) {
        val host = Hosts(context).byPushInstance(instance) ?: return
        Push.forget(context, host)
    }

    companion object {
        private const val TAG = "LecternPush"
    }
}
