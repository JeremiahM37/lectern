package io.github.jeremiahm37.lectern

import android.app.Activity
import android.content.Context
import org.json.JSONObject
import org.unifiedpush.android.connector.UnifiedPush
import org.unifiedpush.android.connector.data.PushEndpoint

/**
 * UnifiedPush registration, one per paired Lectern (its own instance name,
 * so each Lectern gets its own endpoint). The distributor (ntfy, or any
 * other UnifiedPush app) gives this app a Web Push endpoint and RFC 8291
 * keys; the page sends them to that Lectern's ordinary /api/push/subscribe,
 * and Lectern encrypts every notification to those keys, so the push server
 * (ntfy) cannot read them.
 */
object Push {
    /** Asks the distributor for an endpoint; the answer arrives in [PushReceiver]. */
    fun enable(activity: Activity, host: Host, vapidKey: String, failed: (String) -> Unit) {
        UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { ok ->
            if (!ok) {
                failed("No UnifiedPush distributor on this phone. Install ntfy (or another UnifiedPush app) and try again.")
                return@tryUseCurrentOrDefaultDistributor
            }
            try {
                UnifiedPush.register(activity, host.pushInstance, "Lectern: ${host.label}", vapidKey)
            } catch (e: Exception) {
                failed("Push registration failed: ${e.message}")
            }
        }
    }

    fun disable(context: Context, host: Host) {
        runCatching { UnifiedPush.unregister(context, host.pushInstance) }
        forget(context, host)
    }

    fun forget(context: Context, host: Host) {
        val hosts = Hosts(context)
        hosts.putPlain(host.id, SecureStore.PUSH_SUBSCRIPTION, null)
        hosts.putPlain(host.id, SecureStore.PUSH_CONFIRMED, null)
    }

    fun saveEndpoint(context: Context, host: Host, endpoint: PushEndpoint): Boolean {
        val keys = endpoint.pubKeySet ?: return false
        val sub = JSONObject()
            .put("endpoint", endpoint.url)
            .put("keys", JSONObject().put("p256dh", keys.pubKey).put("auth", keys.auth))
        Hosts(context).putPlain(host.id, SecureStore.PUSH_SUBSCRIPTION, sub.toString())
        return true
    }

    fun subscription(context: Context, host: Host): JSONObject? {
        val hosts = Hosts(context)
        val sub = hosts.plain(host.id, SecureStore.PUSH_SUBSCRIPTION)?.let { JSONObject(it) } ?: return null
        return sub.put("confirmed", sub.optString("endpoint") == hosts.plain(host.id, SecureStore.PUSH_CONFIRMED))
    }

    fun confirmed(context: Context, host: Host, endpoint: String) {
        Hosts(context).putPlain(host.id, SecureStore.PUSH_CONFIRMED, endpoint)
    }
}
