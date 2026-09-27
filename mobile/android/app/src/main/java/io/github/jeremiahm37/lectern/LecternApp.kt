package io.github.jeremiahm37.lectern

import android.app.Application
import android.content.Intent
import androidx.core.content.pm.ShortcutInfoCompat
import androidx.core.content.pm.ShortcutManagerCompat
import androidx.core.graphics.drawable.IconCompat

class LecternApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Notifications.createChannels(this)
        // Long-press the launcher icon → Switch Lectern (the list of paired
        // Lecterns). Dynamic, so the debug build's package works as well.
        runCatching {
            ShortcutManagerCompat.pushDynamicShortcut(this, ShortcutInfoCompat.Builder(this, "hosts")
                .setShortLabel(getString(R.string.shortcut_hosts))
                .setIcon(IconCompat.createWithResource(this, R.mipmap.ic_launcher))
                .setIntent(Intent(this, HostsActivity::class.java).setAction(Intent.ACTION_VIEW))
                .build())
        }
    }
}
