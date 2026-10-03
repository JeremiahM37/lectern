package io.github.jeremiahm37.lectern

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.webkit.CookieManager
import android.webkit.JavascriptInterface
import org.json.JSONArray
import org.json.JSONObject

/**
 * window.LecternNative (frontend/src/native/bridge.ts), for the page of one
 * paired Lectern ([hostId]; the visible app's changes when it switches).
 * Every method refuses to run unless the page calling it is on that
 * Lectern's origin; the WebView never navigates anywhere else, so this is
 * defense in depth.
 */
class Bridge(
    context: Context,
    private val owner: Owner,
    private val hostId: () -> String?,
) {
    /** What the bridge needs from whoever owns the WebView. */
    interface Owner {
        /** The origin of the page currently loaded, updated on navigation. */
        val pageOrigin: String?
        fun enablePush(vapidKey: String) {}
        /** The page's Lectern was forgotten. */
        fun disconnected() {}
        fun actionResult(json: JSONObject) {}
        fun switchHost(id: String) {}
        fun openHosts() {}
        fun haptic(kind: String) {}
        fun barColors(background: String, light: Boolean) {}
        fun installUpdate() {}
    }

    private val app = context.applicationContext
    private val hosts = Hosts(app)

    private fun host(): Host {
        val host = hosts.get(hostId()) ?: throw SecurityException("LecternNative has no Lectern")
        val origin = owner.pageOrigin
        if (origin == null || origin != host.origin) throw SecurityException("LecternNative is not available to $origin")
        return host
    }

    @JavascriptInterface
    fun version(): String {
        val host = host()
        return JSONObject().put("app", BuildConfig.VERSION_NAME).put("key", DeviceKey(app, host).storage).toString()
    }

    /** "relay" once a relay pairing is saved (the page then routes through
     * the tunnel), "direct", or "" while a relay pairing is in progress. */
    @JavascriptInterface
    fun mode(): String = host().let { if (it.mode == MODE_RELAY && !it.paired) "" else it.mode }

    @JavascriptInterface
    fun relayPublicKey(): String = DeviceKey.b64(DeviceKey(app, host()).publicKey())

    @JavascriptInterface
    fun relayDH(peer: String): String = DeviceKey.b64(DeviceKey(app, host()).dh(DeviceKey.unb64(peer)))

    @JavascriptInterface
    fun relayPairing(): String = hosts.secret(host().id, SecureStore.RELAY_PAIRING) ?: ""

    @JavascriptInterface
    fun saveRelayPairing(json: String) {
        val host = host()
        require(host.mode == MODE_RELAY) { "not a relay pairing" }
        val pairing = JSONObject(json)
        // The device key is in Keystore; a page must never hand one over.
        require(pairing.optString("deviceSecret").isEmpty()) { "the app keeps its own device key" }
        hosts.putSecret(host.id, SecureStore.RELAY_PAIRING, pairing.toString())
        hosts.update(host.id) { it.copy(paired = true, label = it.label.ifEmpty { Hosts.labelFor(MODE_RELAY, it.origin, pairing.toString()) }) }
    }

    @JavascriptInterface
    fun saveDeviceToken(token: String) {
        val host = host()
        require(host.mode == MODE_DIRECT) { "not connected directly" }
        hosts.putSecret(host.id, SecureStore.DEVICE_TOKEN, token)
        hosts.update(host.id) { it.copy(paired = true) }
        applyDeviceCookie(app, host)
    }

    @JavascriptInterface
    fun forgetPairing() {
        hosts.remove(host().id)
        owner.disconnected()
    }

    @JavascriptInterface
    fun enablePush(vapidKey: String) {
        host()
        owner.enablePush(vapidKey)
    }

    @JavascriptInterface
    fun disablePush() = Push.disable(app, host())

    @JavascriptInterface
    fun pushSubscription(): String = Push.subscription(app, host())?.toString() ?: ""

    @JavascriptInterface
    fun pushSubscribed(endpoint: String) = Push.confirmed(app, host(), endpoint)

    @JavascriptInterface
    fun actionResult(json: String) {
        host()
        owner.actionResult(JSONObject(json))
    }

    // ---- app 0.2.0 ----

    /** The paired Lecterns, for the page's own switcher (settings/PhonePanels.tsx). */
    @JavascriptInterface
    fun hosts(): String {
        val current = host()
        return JSONArray(hosts.all().filter { it.paired }.map {
            JSONObject().put("id", it.id).put("label", it.label).put("mode", it.mode)
                .put("origin", if (Shell.isRelayOrigin(it.origin)) "" else it.origin).put("active", it.id == current.id)
        }).toString()
    }

    @JavascriptInterface
    fun switchHost(id: String) {
        host()
        if (hosts.get(id)?.paired == true) owner.switchHost(id)
    }

    @JavascriptInterface
    fun openHosts() {
        host()
        owner.openHosts()
    }

    @JavascriptInterface
    fun haptic(kind: String) {
        host()
        owner.haptic(kind)
    }

    /** Opens a web address in the phone's browser; nothing but http(s). */
    @JavascriptInterface
    fun openUrl(url: String) {
        host()
        val uri = Uri.parse(url)
        if (uri.scheme != "http" && uri.scheme != "https") return
        app.startActivity(Intent(Intent.ACTION_VIEW, uri).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    // ---- app 2.8.0 ----

    /** Looks for a newer app; answered with a "lectern-native-update" event. */
    @JavascriptInterface
    fun checkUpdate() {
        host()
        Updates.check(Pages::update)
    }

    /** Downloads and installs the newest app, after Android asks the person. */
    @JavascriptInterface
    fun installUpdate() {
        host()
        owner.installUpdate()
    }

    /** The page's background, for the status and navigation bars around it. */
    @JavascriptInterface
    fun barColors(background: String, light: Boolean) {
        host()
        owner.barColors(background, light)
    }

    companion object {
        const val MODE_RELAY = "relay"
        const val MODE_DIRECT = "direct"
        private const val DEVICE_COOKIE = "lectern_device"

        /** Hands a direct-mode device token to the WebView as the same
         * cookie a browser gets from /api/pair/exchange. It is a session
         * cookie, re-applied from the sealed copy at every start, so the
         * WebView's cookie file never keeps it. Cookies are per origin, so
         * every paired Lectern keeps its own. */
        fun applyDeviceCookie(context: Context, host: Host) {
            if (host.mode != MODE_DIRECT) return
            val token = Hosts(context).secret(host.id, SecureStore.DEVICE_TOKEN) ?: return
            val secure = if (host.origin.startsWith("https:")) "; Secure" else ""
            CookieManager.getInstance().setCookie(host.origin, "$DEVICE_COOKIE=$token; Path=/; HttpOnly; SameSite=Strict$secure")
        }

        fun clearDeviceCookie(host: Host) {
            if (host.mode != MODE_DIRECT) return
            CookieManager.getInstance().setCookie(host.origin, "$DEVICE_COOKIE=; Path=/; Max-Age=0")
            CookieManager.getInstance().flush()
        }
    }
}
