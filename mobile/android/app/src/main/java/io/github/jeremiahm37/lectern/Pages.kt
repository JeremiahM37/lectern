package io.github.jeremiahm37.lectern

import android.os.Handler
import android.os.Looper
import android.webkit.WebView
import org.json.JSONObject
import java.lang.ref.WeakReference

/** The app's visible WebView, for telling its page about native events. */
object Pages {
    @Volatile private var current: WeakReference<WebView>? = null
    @Volatile private var showing: () -> String? = { null }
    private val main = Handler(Looper.getMainLooper())

    /** [host] says which Lectern the view shows right now. */
    fun attach(view: WebView, host: () -> String?) {
        current = WeakReference(view)
        showing = host
    }

    fun detach(view: WebView) { if (current?.get() === view) current = null }

    /** Fires "lectern-native-push" (frontend/src/native/push.ts) on the
     * visible page, if it is that Lectern's. Returns whether it was. */
    fun pushResult(hostId: String, ok: Boolean, error: String?): Boolean {
        val view = current?.get() ?: return false
        if (showing() != hostId) return false
        val detail = JSONObject().put("ok", ok).apply { if (error != null) put("error", error) }
        main.post {
            view.evaluateJavascript("window.dispatchEvent(new CustomEvent('lectern-native-push', {detail: $detail}))", null)
        }
        return true
    }
}
