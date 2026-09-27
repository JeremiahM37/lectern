package io.github.jeremiahm37.lectern

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject

/**
 * One Lectern this app is paired with. The app can hold several (a work
 * machine and a home server, say) and shows one at a time.
 *
 * [origin] is what the WebView loads: the Lectern's own address when the
 * phone reaches it directly, or a private origin that never touches the
 * network when it goes through the encrypted relay. Each relay pairing gets
 * its own private origin, so two Lecterns never share the page's storage
 * (settings, the offline cache) even though neither has a real address.
 */
data class Host(
    val id: String,
    val label: String,
    val mode: String,
    val origin: String,
    /** False while a pairing that created this entry has not finished. */
    val paired: Boolean,
    /** Android Keystore alias of this pairing's relay device key. */
    val keyAlias: String,
    /** UnifiedPush instance name: one registration per Lectern. */
    val pushInstance: String,
) {
    fun json(): JSONObject = JSONObject().put("id", id).put("label", label).put("mode", mode).put("origin", origin)
        .put("paired", paired).put("keyAlias", keyAlias).put("pushInstance", pushInstance)

    companion object {
        fun from(o: JSONObject) = Host(
            o.getString("id"), o.optString("label"), o.getString("mode"), o.getString("origin"),
            o.optBoolean("paired", true), o.optString("keyAlias", DeviceKey.LEGACY_ALIAS), o.optString("pushInstance", o.getString("id")),
        )
    }
}

/**
 * The paired Lecterns and which one is open. Plain settings live in the
 * app's preferences; secrets (relay route token, device token) are sealed
 * per host through [SecureStore].
 */
class Hosts(context: Context) {
    private val app = context.applicationContext
    private val store = SecureStore(app)

    init {
        migrate()
    }

    fun all(): List<Host> {
        val raw = store.getPlain(KEY_HOSTS) ?: return emptyList()
        return runCatching {
            val arr = JSONArray(raw)
            (0 until arr.length()).map { Host.from(arr.getJSONObject(it)) }
        }.getOrDefault(emptyList())
    }

    fun get(id: String?): Host? = id?.let { wanted -> all().firstOrNull { it.id == wanted } }

    var activeId: String?
        get() = store.getPlain(KEY_ACTIVE)
        set(value) = store.putPlain(KEY_ACTIVE, value)

    fun active(): Host? = get(activeId)

    fun byPushInstance(instance: String): Host? = all().firstOrNull { it.pushInstance == instance }

    private fun save(list: List<Host>) {
        store.putPlain(KEY_HOSTS, JSONArray(list.map { it.json() }).toString())
    }

    /** A new, not yet paired entry; the pairing page completes it. */
    fun add(mode: String, origin: String, label: String): Host {
        val id = nextId()
        val host = Host(
            id = id,
            label = label,
            mode = mode,
            origin = if (mode == Bridge.MODE_RELAY) Shell.relayOrigin(id) else origin,
            paired = false,
            keyAlias = DeviceKey.LEGACY_ALIAS + "-" + id,
            pushInstance = id,
        )
        save(all() + host)
        return host
    }

    fun update(id: String, change: (Host) -> Host): Host? {
        var updated: Host? = null
        save(all().map { if (it.id == id) change(it).also { h -> updated = h } else it })
        return updated
    }

    /** Forgets one Lectern: its secrets, key, cookie and push registration. */
    fun remove(id: String) {
        val host = get(id) ?: return
        Push.disable(app, host)
        Bridge.clearDeviceCookie(host)
        for (name in listOf(SecureStore.RELAY_PAIRING, SecureStore.DEVICE_TOKEN)) store.putSecret(secretName(id, name), null)
        DeviceKey(app, host).delete()
        save(all().filter { it.id != id })
        if (activeId == id) activeId = all().firstOrNull { it.paired }?.id
    }

    /** Drops entries left behind by a pairing that was abandoned. */
    fun pruneUnpaired(keep: String? = null) {
        for (h in all()) if (!h.paired && h.id != keep) remove(h.id)
    }

    fun secret(id: String, name: String): String? = store.getSecret(secretName(id, name))

    fun putSecret(id: String, name: String, value: String?) = store.putSecret(secretName(id, name), value)

    fun plain(id: String, name: String): String? = store.getPlain(secretName(id, name))

    fun putPlain(id: String, name: String, value: String?) = store.putPlain(secretName(id, name), value)

    /** A direct Lectern already in the list, by address. */
    fun byOrigin(origin: String): Host? = all().firstOrNull { it.mode == Bridge.MODE_DIRECT && it.origin == origin }

    private fun nextId(): String {
        val used = all().map { it.id }.toSet()
        var n = (store.getPlain(KEY_COUNTER)?.toIntOrNull() ?: 1) + 1
        while ("h$n" in used) n++
        store.putPlain(KEY_COUNTER, n.toString())
        return "h$n"
    }

    /** App 0.1.0 kept exactly one connection in flat settings; it becomes
     * host h1, keeping its origin, key alias and push registration, so an
     * update changes nothing for a phone that is already paired. */
    private fun migrate() {
        if (store.getPlain(KEY_HOSTS) != null) return
        val mode = store.legacyMode
        if (mode.isEmpty()) {
            save(emptyList())
            return
        }
        val origin = store.legacyOrigin.ifEmpty { Shell.APP_ORIGIN }
        val id = "h1"
        for (name in listOf(SecureStore.RELAY_PAIRING, SecureStore.DEVICE_TOKEN, SecureStore.PUSH_SUBSCRIPTION, SecureStore.PUSH_CONFIRMED)) {
            store.rename(name, secretName(id, name))
        }
        val host = Host(id, labelFor(mode, origin, secret(id, SecureStore.RELAY_PAIRING)), mode, origin, true, DeviceKey.LEGACY_ALIAS, LEGACY_PUSH_INSTANCE)
        save(listOf(host))
        activeId = id
        store.putPlain(KEY_COUNTER, "1")
        store.clearLegacy()
    }

    companion object {
        private const val KEY_HOSTS = "hosts"
        private const val KEY_ACTIVE = "active_host"
        private const val KEY_COUNTER = "host_counter"
        /** The UnifiedPush instance app 0.1.0 registered. */
        const val LEGACY_PUSH_INSTANCE = "default"

        fun secretName(id: String, name: String) = "$name:$id"

        /** What to call a Lectern until the person renames it. */
        fun labelFor(mode: String, origin: String, relayPairing: String?): String {
            if (mode == Bridge.MODE_RELAY) {
                val relay = runCatching { JSONObject(relayPairing ?: "").optString("relay") }.getOrNull().orEmpty()
                val host = runCatching { java.net.URI(relay).host }.getOrNull()
                return if (host.isNullOrEmpty()) "Lectern (relay)" else "Lectern via $host"
            }
            return runCatching { java.net.URI(origin).host }.getOrNull()?.takeIf { it.isNotEmpty() } ?: origin
        }
    }
}
