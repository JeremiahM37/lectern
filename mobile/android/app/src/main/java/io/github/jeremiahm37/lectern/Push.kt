package io.github.jeremiahm37.lectern

import android.app.Activity
import android.content.Context
import org.json.JSONObject
import org.unifiedpush.android.connector.UnifiedPush
import org.unifiedpush.android.connector.data.PushEndpoint

/**
 * UnifiedPush registration. The distributor (ntfy, or any other UnifiedPush
 * app) gives this app a Web Push endpoint and RFC 8291 keys; the page sends
 * them to Lectern's ordinary /api/push/subscribe, and Lectern encrypts every
 * notification to those keys, so the push server (ntfy) cannot read them.
 */
object Push {
    private const val INSTANCE = "default"

    /** Asks the distributor for an endpoint; the answer arrives in [PushReceiver]. */
    fun enable(activity: Activity, vapidKey: String, failed: (String) -> Unit) {
        UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { ok ->
            if (!ok) {
                failed("No UnifiedPush distributor on this phone. Install ntfy (or another UnifiedPush app) and try again.")
                return@tryUseCurrentOrDefaultDistributor
            }
            try {
                UnifiedPush.register(activity, INSTANCE, "Lectern", vapidKey)
            } catch (e: Exception) {
                failed("Push registration failed: ${e.message}")
            }
        }
    }

    fun disable(context: Context) {
        runCatching { UnifiedPush.unregister(context, INSTANCE) }
        val store = SecureStore(context)
        store.putPlain(SecureStore.PUSH_SUBSCRIPTION, null)
        store.putPlain(SecureStore.PUSH_CONFIRMED, null)
    }

    fun saveEndpoint(context: Context, endpoint: PushEndpoint): Boolean {
        val keys = endpoint.pubKeySet ?: return false
        val sub = JSONObject()
            .put("endpoint", endpoint.url)
            .put("keys", JSONObject().put("p256dh", keys.pubKey).put("auth", keys.auth))
        SecureStore(context).putPlain(SecureStore.PUSH_SUBSCRIPTION, sub.toString())
        return true
    }

    fun subscription(context: Context): JSONObject? {
        val store = SecureStore(context)
        val sub = store.getPlain(SecureStore.PUSH_SUBSCRIPTION)?.let { JSONObject(it) } ?: return null
        return sub.put("confirmed", sub.optString("endpoint") == store.getPlain(SecureStore.PUSH_CONFIRMED))
    }

    fun confirmed(context: Context, endpoint: String) {
        SecureStore(context).putPlain(SecureStore.PUSH_CONFIRMED, endpoint)
    }
}
