package io.github.jeremiahm37.lectern

import android.os.Handler
import android.os.Looper
import android.webkit.WebView
import org.json.JSONObject
import java.lang.ref.WeakReference

/** The app's visible WebView, for telling its page about native events. */
object Pages {
    @Volatile private var current: WeakReference<WebView>? = null
    private val main = Handler(Looper.getMainLooper())

    fun attach(view: WebView) { current = WeakReference(view) }

    fun detach(view: WebView) { if (current?.get() === view) current = null }

    /** Fires "lectern-native-push" (frontend/src/native/push.ts). Returns
     * whether a page was there to hear it. */
    fun pushResult(ok: Boolean, error: String?): Boolean {
        val view = current?.get() ?: return false
        val detail = JSONObject().put("ok", ok).apply { if (error != null) put("error", error) }
        main.post {
            view.evaluateJavascript("window.dispatchEvent(new CustomEvent('lectern-native-push', {detail: $detail}))", null)
        }
        return true
    }
}
