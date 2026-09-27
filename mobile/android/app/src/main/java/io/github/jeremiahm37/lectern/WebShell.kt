package io.github.jeremiahm37.lectern

import android.annotation.SuppressLint
import android.content.Intent
import android.graphics.Bitmap
import android.net.Uri
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient

/** Shared WebView setup for the visible app and the unseen action runner. */
object WebShell {
    const val BRIDGE = "LecternNative"

    /**
     * [origin] is the origin of the Lectern this view shows (it changes when
     * the visible app switches Lecterns). Its app files come from the APK;
     * a relay origin never reaches the network at all.
     */
    @SuppressLint("SetJavaScriptEnabled")
    fun configure(view: WebView, bridge: Bridge, origin: () -> String?, onOrigin: (String) -> Unit, onExternal: ((Uri) -> Unit)? = null) {
        val shell = Shell(view.context)
        view.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            mediaPlaybackRequiresUserGesture = false
            allowFileAccess = false
            allowContentAccess = false
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            userAgentString = "$userAgentString LecternAndroid/${BuildConfig.VERSION_NAME}"
        }
        view.addJavascriptInterface(bridge, BRIDGE)
        view.webViewClient = object : WebViewClient() {
            override fun shouldInterceptRequest(v: WebView, request: WebResourceRequest): WebResourceResponse? {
                val requested = Shell.originOf(request.url.toString())
                val app = origin()
                if (app != null && requested == app) return shell.respond(request, relayOrigin = Shell.isRelayOrigin(app))
                // Another pairing's private origin: never the network.
                if (Shell.isRelayOrigin(requested)) return Shell.notFound()
                return null
            }

            override fun onPageStarted(v: WebView, url: String, favicon: Bitmap?) {
                onOrigin(Shell.originOf(url))
            }

            override fun shouldOverrideUrlLoading(v: WebView, request: WebResourceRequest): Boolean {
                val requested = Shell.originOf(request.url.toString())
                if (requested == origin()) return false
                if (Shell.isRelayOrigin(requested)) return true
                // Anything else leaves the app: the bridge only ever serves
                // this Lectern's own pages.
                onExternal?.invoke(request.url) ?: runCatching {
                    v.context.startActivity(Intent(Intent.ACTION_VIEW, request.url).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                }
                return true
            }
        }
    }
}
