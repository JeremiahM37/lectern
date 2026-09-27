package io.github.jeremiahm37.lectern

import android.app.Application

class LecternApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Notifications.createChannels(this)
    }
}
