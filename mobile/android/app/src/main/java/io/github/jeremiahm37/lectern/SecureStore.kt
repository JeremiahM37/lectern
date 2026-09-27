package io.github.jeremiahm37.lectern

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * The app's settings. Secrets (relay route tokens, direct-mode device
 * tokens, fallback device keys; one set per paired Lectern, see Hosts) are
 * sealed with an AES-256-GCM key that lives in Android Keystore and never
 * leaves it, so the preference file on its own reveals nothing.
 */
class SecureStore(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences("lectern", Context.MODE_PRIVATE)

    /** App 0.1.0's single connection, read once to migrate it (Hosts). */
    val legacyMode: String get() = prefs.getString(KEY_MODE, "") ?: ""
    val legacyOrigin: String get() = prefs.getString(KEY_ORIGIN, "") ?: ""

    fun clearLegacy() {
        prefs.edit().remove(KEY_MODE).remove(KEY_ORIGIN).commit()
    }

    /** Moves a stored value (sealed or not) to a new name, unchanged. */
    fun rename(from: String, to: String) {
        val value = prefs.getString(from, null) ?: return
        prefs.edit().putString(to, value).remove(from).commit()
    }

    fun putSecret(name: String, value: String?) {
        if (value == null) {
            prefs.edit().remove(name).apply()
            return
        }
        prefs.edit().putString(name, seal(value.toByteArray())).commit()
    }

    fun getSecret(name: String): String? = prefs.getString(name, null)?.let { runCatching { String(open(it)) }.getOrNull() }

    fun putPlain(name: String, value: String?) {
        if (value == null) prefs.edit().remove(name).commit() else prefs.edit().putString(name, value).commit()
    }

    fun getPlain(name: String): String? = prefs.getString(name, null)

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return gen.generateKey()
    }

    private fun seal(plain: ByteArray): String {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val out = cipher.iv + cipher.doFinal(plain)
        return Base64.encodeToString(out, Base64.NO_WRAP)
    }

    private fun open(sealed: String): ByteArray {
        val raw = Base64.decode(sealed, Base64.NO_WRAP)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, raw, 0, 12))
        return cipher.doFinal(raw, 12, raw.size - 12)
    }

    companion object {
        private const val ALIAS = "lectern-store"
        private const val KEY_MODE = "mode"
        private const val KEY_ORIGIN = "origin"
        const val RELAY_PAIRING = "relay_pairing"
        const val DEVICE_TOKEN = "device_token"
        const val PUSH_SUBSCRIPTION = "push_subscription"
        const val PUSH_CONFIRMED = "push_confirmed"
        const val FALLBACK_DEVICE_KEY = "relay_device_key"
    }
}
