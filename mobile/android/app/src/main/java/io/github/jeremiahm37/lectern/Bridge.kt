package io.github.jeremiahm37.lectern

import android.content.Context
import android.webkit.CookieManager
import android.webkit.JavascriptInterface
import org.json.JSONObject

/**
 * window.LecternNative (frontend/src/native/bridge.ts). Every method refuses
 * to run unless the page calling it is on the origin this app connected to
 * ([allowedOrigin]); the WebView never navigates anywhere else, so this is
 * defense in depth.
 */
class Bridge(
    context: Context,
    private val host: Host,
) {
    /** What the bridge needs from whoever owns the WebView. */
    interface Host {
        /** The origin of the page currently loaded, updated on navigation. */
        val pageOrigin: String?
        fun enablePush(vapidKey: String) {}
        fun disconnected() {}
        fun actionResult(json: JSONObject) {}
    }

    private val app = context.applicationContext
    private val store = SecureStore(app)
    private val key = DeviceKey(app)

    private fun check() {
        val origin = host.pageOrigin
        val allowed = store.origin.ifEmpty { Shell.APP_ORIGIN }
        if (origin == null || (origin != allowed && origin != Shell.APP_ORIGIN)) {
            throw SecurityException("LecternNative is not available to $origin")
        }
    }

    @JavascriptInterface
    fun version(): String {
        check()
        return JSONObject().put("app", BuildConfig.VERSION_NAME).put("key", key.storage).toString()
    }

    @JavascriptInterface
    fun mode(): String {
        check()
        return store.mode
    }

    @JavascriptInterface
    fun relayPublicKey(): String {
        check()
        return DeviceKey.b64(key.publicKey())
    }

    @JavascriptInterface
    fun relayDH(peer: String): String {
        check()
        return DeviceKey.b64(key.dh(DeviceKey.unb64(peer)))
    }

    @JavascriptInterface
    fun relayPairing(): String {
        check()
        return store.getSecret(SecureStore.RELAY_PAIRING) ?: ""
    }

    @JavascriptInterface
    fun saveRelayPairing(json: String) {
        check()
        val pairing = JSONObject(json)
        // The device key is in Keystore; a page must never hand one over.
        require(pairing.optString("deviceSecret").isEmpty()) { "the app keeps its own device key" }
        store.putSecret(SecureStore.RELAY_PAIRING, pairing.toString())
        store.origin = Shell.APP_ORIGIN
        store.mode = MODE_RELAY
    }

    @JavascriptInterface
    fun saveDeviceToken(token: String) {
        check()
        require(store.mode == MODE_DIRECT) { "not connected directly" }
        store.putSecret(SecureStore.DEVICE_TOKEN, token)
        applyDeviceCookie(app)
    }

    @JavascriptInterface
    fun forgetPairing() {
        check()
        forget(app)
        host.disconnected()
    }

    @JavascriptInterface
    fun enablePush(vapidKey: String) {
        check()
        host.enablePush(vapidKey)
    }

    @JavascriptInterface
    fun disablePush() {
        check()
        Push.disable(app)
    }

    @JavascriptInterface
    fun pushSubscription(): String {
        check()
        return Push.subscription(app)?.toString() ?: ""
    }

    @JavascriptInterface
    fun pushSubscribed(endpoint: String) {
        check()
        Push.confirmed(app, endpoint)
    }

    @JavascriptInterface
    fun actionResult(json: String) {
        check()
        host.actionResult(JSONObject(json))
    }

    companion object {
        const val MODE_RELAY = "relay"
        const val MODE_DIRECT = "direct"
        private const val DEVICE_COOKIE = "lectern_device"

        /** Hands the direct-mode device token to the WebView as the same
         * cookie a browser gets from /api/pair/exchange. It is a session
         * cookie, re-applied from the sealed copy at every start, so the
         * WebView's cookie file never keeps it. */
        fun applyDeviceCookie(context: Context) {
            val store = SecureStore(context)
            val token = store.getSecret(SecureStore.DEVICE_TOKEN) ?: return
            val origin = store.origin
            val secure = if (origin.startsWith("https:")) "; Secure" else ""
            CookieManager.getInstance().setCookie(origin, "$DEVICE_COOKIE=$token; Path=/; HttpOnly; SameSite=Strict$secure")
        }

        fun forget(context: Context) {
            val store = SecureStore(context)
            Push.disable(context)
            store.origin.takeIf { it.isNotEmpty() }?.let {
                CookieManager.getInstance().setCookie(it, "$DEVICE_COOKIE=; Path=/; Max-Age=0")
            }
            CookieManager.getInstance().flush()
            store.clearConnection()
            DeviceKey(context).delete()
        }
    }
}
