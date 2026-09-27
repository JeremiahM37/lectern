package io.github.jeremiahm37.lectern

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
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

/** The Lectern web app, from this APK, in a WebView. */
class MainActivity : ComponentActivity(), Bridge.Host {
    private lateinit var web: WebView
    private lateinit var store: SecureStore
    @Volatile override var pageOrigin: String? = null
    private var pendingVapid: String? = null
    private var fileCallback: ValueCallback<Array<Uri>>? = null

    private val notificationPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val key = pendingVapid ?: return@registerForActivityResult
        pendingVapid = null
        if (granted) startPush(key) else Pages.pushResult(false, "Notifications are turned off for Lectern in Android settings.")
    }

    private val filePicker = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        fileCallback?.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(result.resultCode, result.data))
        fileCallback = null
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        store = SecureStore(this)
        val start = intent.getStringExtra(EXTRA_START)
        if (store.mode.isEmpty() && start == null) {
            startActivity(Intent(this, ConnectActivity::class.java))
            finish()
            return
        }
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        web = WebView(this)
        ViewCompat.setOnApplyWindowInsetsListener(web) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.ime())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            WindowInsetsCompat.CONSUMED
        }
        setContentView(web)
        WebShell.configure(web, Bridge(this, this), onOrigin = { pageOrigin = it })
        web.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                fileCallback?.onReceiveValue(null)
                fileCallback = callback
                return runCatching { filePicker.launch(params.createIntent()) }.isSuccess
            }
        }
        if (store.mode == Bridge.MODE_DIRECT) Bridge.applyDeviceCookie(this)
        Pages.attach(web)
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (web.canGoBack()) web.goBack() else finish()
            }
        })
        web.loadUrl(start ?: (store.origin + safePath(intent.getStringExtra(EXTRA_PATH))))
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        val path = intent.getStringExtra(EXTRA_PATH) ?: return
        if (::web.isInitialized && store.origin.isNotEmpty()) web.loadUrl(store.origin + safePath(path))
    }

    override fun onDestroy() {
        if (::web.isInitialized) {
            Pages.detach(web)
            web.destroy()
        }
        super.onDestroy()
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

    private fun startPush(vapidKey: String) = Push.enable(this, vapidKey) { error -> Pages.pushResult(false, error) }

    override fun disconnected() = runOnUiThread {
        startActivity(Intent(this, ConnectActivity::class.java))
        finish()
    }

    override fun actionResult(json: JSONObject) {}

    companion object {
        /** A full URL to open instead of the app's home (pairing pages). */
        const val EXTRA_START = "start"
        /** A path on the connected Lectern, e.g. from a notification. */
        const val EXTRA_PATH = "path"

        /** Only a path on this Lectern, never another origin. */
        fun safePath(path: String?): String = path?.takeIf { it.startsWith("/") && !it.startsWith("//") } ?: "/"
    }
}
