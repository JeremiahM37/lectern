package io.github.jeremiahm37.lectern

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.view.HapticFeedbackConstants
import android.webkit.PermissionRequest
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebView
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import org.json.JSONObject

/** The Lectern web app, from this APK, in a WebView, for one paired Lectern
 * at a time; switching reloads the view on another. */
class MainActivity : ComponentActivity(), Bridge.Owner {
    private lateinit var web: WebView
    private lateinit var hosts: Hosts
    @Volatile override var pageOrigin: String? = null
    /** The Lectern the view shows. */
    @Volatile private var hostId: String? = null
    private var pendingVapid: String? = null
    private var pendingMic: PermissionRequest? = null
    private var fileCallback: ValueCallback<Array<Uri>>? = null

    private val notificationPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val key = pendingVapid ?: return@registerForActivityResult
        pendingVapid = null
        if (granted) startPush(key) else Pages.pushResult(hostId ?: "", false, "Notifications are turned off for Lectern in Android settings.")
    }

    // Dictation transcribed on the host records in the page (voice-host.ts);
    // the WebView asks here, and Android asks the person once.
    private val micPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val request = pendingMic ?: return@registerForActivityResult
        pendingMic = null
        if (granted) request.grant(arrayOf(PermissionRequest.RESOURCE_AUDIO_CAPTURE)) else request.deny()
    }

    private val filePicker = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        fileCallback?.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(result.resultCode, result.data))
        fileCallback = null
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        hosts = Hosts(this)
        val start = intent.getStringExtra(EXTRA_START)
        intent.getStringExtra(EXTRA_HOST)?.let { id -> if (hosts.get(id) != null) hosts.activeId = id }
        // An abandoned pairing leaves an unpaired entry; only the pairing
        // page being opened right now may use one.
        if (start == null) hosts.pruneUnpaired()
        val host = hosts.active()?.takeIf { it.paired || start != null }
        if (host == null) {
            startActivity(Intent(this, ConnectActivity::class.java))
            finish()
            return
        }
        hostId = host.id
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        web = WebView(this)
        ViewCompat.setOnApplyWindowInsetsListener(web) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.ime())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            WindowInsetsCompat.CONSUMED
        }
        setContentView(web)
        WebShell.configure(web, Bridge(this, this) { hostId }, origin = { hosts.get(hostId)?.origin }, onOrigin = { pageOrigin = it })
        web.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                fileCallback?.onReceiveValue(null)
                fileCallback = callback
                return runCatching { filePicker.launch(params.createIntent()) }.isSuccess
            }

            override fun onPermissionRequest(request: PermissionRequest) = runOnUiThread {
                val own = Shell.originOf(request.origin.toString()) == hosts.get(hostId)?.origin
                if (!own || !request.resources.contains(PermissionRequest.RESOURCE_AUDIO_CAPTURE)) {
                    request.deny()
                } else if (ContextCompat.checkSelfPermission(this@MainActivity, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
                    request.grant(arrayOf(PermissionRequest.RESOURCE_AUDIO_CAPTURE))
                } else {
                    pendingMic?.deny()
                    pendingMic = request
                    micPermission.launch(Manifest.permission.RECORD_AUDIO)
                }
            }
        }
        for (h in hosts.all()) Bridge.applyDeviceCookie(this, h)
        Pages.attach(web) { hostId }
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (web.canGoBack()) web.goBack() else finish()
            }
        })
        web.loadUrl(start ?: (host.origin + safePath(intent.getStringExtra(EXTRA_PATH))))
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        if (!::web.isInitialized) return
        val target = intent.getStringExtra(EXTRA_HOST)?.let { hosts.get(it) }?.takeIf { it.paired }
        val path = intent.getStringExtra(EXTRA_PATH)
        if (target != null && target.id != hostId) show(target, path)
        else if (path != null) hosts.get(hostId)?.let { web.loadUrl(it.origin + safePath(path)) }
    }

    override fun onDestroy() {
        if (::web.isInitialized) {
            Pages.detach(web)
            web.destroy()
        }
        super.onDestroy()
    }

    /** Shows another paired Lectern in the same view. */
    private fun show(host: Host, path: String? = null) {
        hosts.activeId = host.id
        hostId = host.id
        Bridge.applyDeviceCookie(this, host)
        web.loadUrl(host.origin + safePath(path))
        // Back must not return to the previous Lectern's pages.
        web.postDelayed({ web.clearHistory() }, 800)
    }

    override fun switchHost(id: String) = runOnUiThread {
        hosts.get(id)?.takeIf { it.paired }?.let { show(it) }
    }

    override fun openHosts() = runOnUiThread { startActivity(Intent(this, HostsActivity::class.java)) }

    override fun haptic(kind: String) = runOnUiThread {
        val effect = when (kind) {
            "confirm" -> if (Build.VERSION.SDK_INT >= 30) HapticFeedbackConstants.CONFIRM else HapticFeedbackConstants.VIRTUAL_KEY
            "warn" -> if (Build.VERSION.SDK_INT >= 30) HapticFeedbackConstants.REJECT else HapticFeedbackConstants.LONG_PRESS
            else -> HapticFeedbackConstants.CLOCK_TICK
        }
        web.performHapticFeedback(effect)
    }

    override fun enablePush(vapidKey: String) = runOnUiThread {
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            pendingVapid = vapidKey
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        } else {
            startPush(vapidKey)
        }
    }

    private fun startPush(vapidKey: String) {
        val host = hosts.get(hostId) ?: return
        Push.enable(this, host, vapidKey) { error -> Pages.pushResult(host.id, false, error) }
    }

    /** The shown Lectern was forgotten: show another, or pair one. */
    override fun disconnected() = runOnUiThread {
        val next = hosts.all().firstOrNull { it.paired }
        if (next != null) show(next)
        else {
            startActivity(Intent(this, ConnectActivity::class.java))
            finish()
        }
    }

    override fun actionResult(json: JSONObject) {}

    companion object {
        /** A full URL to open instead of the app's home (pairing pages). */
        const val EXTRA_START = "start"
        /** A path on the connected Lectern, e.g. from a notification. */
        const val EXTRA_PATH = "path"
        /** Which paired Lectern to show (a notification names its own). */
        const val EXTRA_HOST = "host"

        /** Only a path on this Lectern, never another origin. */
        fun safePath(path: String?): String = path?.takeIf { it.startsWith("/") && !it.startsWith("//") } ?: "/"
    }
}
