package io.github.jeremiahm37.lectern

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyInfo
import android.security.keystore.KeyProperties
import android.util.Base64
import android.util.Log
import com.google.crypto.tink.subtle.X25519
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.X509EncodedKeySpec
import javax.crypto.KeyAgreement

/**
 * This phone's relay identity: the X25519 static key Noise IK uses
 * (docs/relay.md). JavaScript gets the public half and a DH function, never
 * the private half.
 *
 * On Android 13+ the key is generated inside Android Keystore and is not
 * extractable at all. Where Keystore has no X25519 (older Android, or a
 * Keystore that refuses it) the key is software X25519 (Tink) sealed with the
 * app's Keystore AES key ([SecureStore]); that is the documented fallback.
 */
class DeviceKey(context: Context, host: Host) {
    private val store = SecureStore(context)
    // One key per paired Lectern, so two Lecterns cannot tell they share a
    // phone. App 0.1.0's pairing keeps its original alias and sealed name.
    private val alias = host.keyAlias
    private val fallbackName = if (alias == LEGACY_ALIAS) SecureStore.FALLBACK_DEVICE_KEY else Hosts.secretName(host.id, SecureStore.FALLBACK_DEVICE_KEY)

    /** "keystore" (non-extractable), "keystore-tee"/"keystore-strongbox" when
     * the key is in secure hardware, or "sealed" (software key, encrypted). */
    val storage: String
        get() = keystorePrivate()?.let { hardware(it) } ?: if (store.getSecret(fallbackName) != null) "sealed" else "none"

    fun publicKey(): ByteArray {
        keystorePrivate()?.let { return keystorePublic() }
        sealedPrivate()?.let { return X25519.publicFromPrivate(it) }
        if (Build.VERSION.SDK_INT >= 33) {
            val made = runCatching { generateInKeystore() }
            if (made.isSuccess) return keystorePublic()
            Log.w(TAG, "Keystore has no usable X25519; using a sealed software key", made.exceptionOrNull())
        }
        val secret = X25519.generatePrivateKey()
        store.putSecret(fallbackName, b64(secret))
        return X25519.publicFromPrivate(secret)
    }

    fun dh(peer: ByteArray): ByteArray {
        require(peer.size == 32) { "peer key must be 32 bytes" }
        keystorePrivate()?.let { priv ->
            val agreement = KeyAgreement.getInstance("XDH", "AndroidKeyStore")
            agreement.init(priv)
            agreement.doPhase(KeyFactory.getInstance("XDH").generatePublic(X509EncodedKeySpec(SPKI + peer)), true)
            return agreement.generateSecret()
        }
        val secret = sealedPrivate() ?: error("no device key")
        return X25519.computeSharedSecret(secret, peer)
    }

    fun delete() {
        runCatching { keystore().deleteEntry(alias) }
        store.putSecret(fallbackName, null)
    }

    private fun keystore() = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    private fun keystorePrivate(): PrivateKey? = runCatching { keystore().getKey(alias, null) as? PrivateKey }.getOrNull()

    private fun keystorePublic(): ByteArray {
        val encoded = keystore().getCertificate(alias).publicKey.encoded
        return encoded.copyOfRange(encoded.size - 32, encoded.size)
    }

    private fun sealedPrivate(): ByteArray? = store.getSecret(fallbackName)?.let { unb64(it) }

    private fun generateInKeystore() {
        val gen = KeyPairGenerator.getInstance("XDH", "AndroidKeyStore")
        gen.initialize(
            KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_AGREE_KEY)
                .setAlgorithmParameterSpec(ECGenParameterSpec("x25519"))
                .build(),
        )
        gen.generateKeyPair()
        // Prove the key works before relying on it; a Keystore that
        // generates but cannot agree gets the sealed fallback instead.
        try {
            dh(X25519.publicFromPrivate(X25519.generatePrivateKey()))
        } catch (e: Exception) {
            keystore().deleteEntry(alias)
            throw e
        }
    }

    private fun hardware(key: PrivateKey): String = runCatching {
        val info = KeyFactory.getInstance(key.algorithm, "AndroidKeyStore").getKeySpec(key, KeyInfo::class.java)
        when {
            Build.VERSION.SDK_INT >= 31 && info.securityLevel == KeyProperties.SECURITY_LEVEL_STRONGBOX -> "keystore-strongbox"
            Build.VERSION.SDK_INT >= 31 && info.securityLevel == KeyProperties.SECURITY_LEVEL_TRUSTED_ENVIRONMENT -> "keystore-tee"
            else -> "keystore"
        }
    }.getOrDefault("keystore")

    companion object {
        private const val TAG = "LecternKey"
        /** App 0.1.0's alias; new pairings append their host id. */
        const val LEGACY_ALIAS = "lectern-relay-x25519"
        // SubjectPublicKeyInfo header for an X25519 key (RFC 8410).
        private val SPKI = byteArrayOf(0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x6e, 0x03, 0x21, 0x00)

        fun b64(bytes: ByteArray): String = Base64.encodeToString(bytes, Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)
        fun unb64(text: String): ByteArray = Base64.decode(text, Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)
    }
}
