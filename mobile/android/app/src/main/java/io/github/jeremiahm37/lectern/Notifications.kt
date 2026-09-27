package io.github.jeremiahm37.lectern

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.RemoteInput
import org.json.JSONObject

/**
 * Turns a Lectern push (internal/sinks pushMessage: title, body, url, kind,
 * approval_id, session_id) into an Android notification. Approvals get
 * Approve / Deny / Reply; a session waiting on you gets Reply. The buttons
 * work with no Lectern screen open (ActionReceiver → ActionWorker).
 *
 * With several Lecterns paired, each notification names the one it came
 * from and its buttons act on that one. A "dismiss" push (an approval
 * decided anywhere else) removes the notification.
 */
object Notifications {
    const val CHANNEL_APPROVALS = "approvals"
    const val CHANNEL_ACTIVITY = "activity"
    const val REPLY_KEY = "reply"

    fun createChannels(context: Context) {
        val nm = context.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_APPROVALS, context.getString(R.string.channel_approvals), NotificationManager.IMPORTANCE_HIGH),
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ACTIVITY, context.getString(R.string.channel_activity), NotificationManager.IMPORTANCE_DEFAULT),
        )
    }

    /** Same grouping as the web app (frontend/src/sw-actions.ts groupTag): a
     * newer state for the same approval or session replaces the old entry. */
    fun tagOf(data: JSONObject): String {
        val kind = data.optString("kind")
        return when {
            kind == "dismiss" -> data.optString("tag")
            kind == "approval" && data.has("approval_id") -> "approval-${data.getLong("approval_id")}"
            data.has("limit_id") -> "limit-${data.getLong("limit_id")}"
            data.has("session_id") -> "session-${data.getLong("session_id")}"
            else -> "kind-${kind.ifEmpty { "general" }}"
        }
    }

    /** One Lectern's tag space: approval 7 on two Lecterns is two entries. */
    fun scopedTag(host: Host, tag: String) = "${host.id}/$tag"

    fun idOf(tag: String): Int = tag.hashCode() and 0x7fffffff

    private val safeTag = Regex("^[a-z]+-[A-Za-z0-9_-]{1,80}$")

    fun show(context: Context, host: Host, data: JSONObject) {
        val kind = data.optString("kind")
        if (kind == "dismiss") {
            val tag = data.optString("tag")
            if (safeTag.matches(tag)) dismiss(context, host, tag)
            return
        }
        val tag = scopedTag(host, tagOf(data))
        val id = idOf(tag)
        val approval = kind == "approval" && data.has("approval_id")
        val builder = NotificationCompat.Builder(context, if (approval) CHANNEL_APPROVALS else CHANNEL_ACTIVITY)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(data.optString("title", "Lectern"))
            .setContentText(data.optString("body"))
            .setSubText(label(context, host))
            .setStyle(NotificationCompat.BigTextStyle().bigText(data.optString("body")))
            .setAutoCancel(true)
            .setContentIntent(open(context, id, host, data.optString("url", "/")))
            .setPriority(if (approval) NotificationCompat.PRIORITY_HIGH else NotificationCompat.PRIORITY_DEFAULT)
            .setCategory(if (approval) NotificationCompat.CATEGORY_MESSAGE else NotificationCompat.CATEGORY_STATUS)
        if (approval) {
            val approvalId = data.getLong("approval_id")
            val about = data.optString("body")
            builder.addAction(0, "Approve", Actions.intent(context, id, host, tag, Actions.APPROVE, approvalId, about))
            builder.addAction(0, "Deny", Actions.intent(context, id, host, tag, Actions.DENY, approvalId, about))
            builder.addAction(replyAction(context, id, host, tag, Actions.REPLY_APPROVAL, approvalId, "Reply (denies with your note)", about))
        } else if ((kind == "waiting_input" || kind == "waiting_permission") && data.has("session_id")) {
            builder.addAction(replyAction(context, id, host, tag, Actions.REPLY_SESSION, data.getLong("session_id"), "Reply", data.optString("body")))
        }
        notify(context, id, builder)
    }

    /** Removes a notification that no longer needs anyone. */
    fun dismiss(context: Context, host: Host, tag: String) {
        NotificationManagerCompat.from(context).cancel(idOf(scopedTag(host, tag)))
    }

    /** The Lectern's name on its notifications, once there is more than one. */
    private fun label(context: Context, host: Host): String? = if (Hosts(context).all().count { it.paired } > 1) host.label else null

    /** Replaces a notification with the outcome of pressing one of its buttons. */
    fun result(context: Context, id: Int, host: Host?, title: String, text: String, ongoing: Boolean = false) {
        val builder = NotificationCompat.Builder(context, CHANNEL_APPROVALS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setSilent(true)
            .setOngoing(ongoing)
            .setAutoCancel(!ongoing)
        if (host != null) builder.setSubText(label(context, host)).setContentIntent(open(context, id, host, "/#approvals"))
        notify(context, id, builder)
    }

    private fun replyAction(context: Context, id: Int, host: Host, tag: String, action: String, target: Long, label: String, about: String): NotificationCompat.Action {
        val input = RemoteInput.Builder(REPLY_KEY).setLabel(label).build()
        return NotificationCompat.Action.Builder(0, "Reply", Actions.intent(context, id, host, tag, action, target, about, mutable = true))
            .addRemoteInput(input)
            .setAllowGeneratedReplies(false)
            .build()
    }

    private fun open(context: Context, id: Int, host: Host, url: String): PendingIntent {
        val intent = Intent(context, MainActivity::class.java)
            .putExtra(MainActivity.EXTRA_PATH, url)
            .putExtra(MainActivity.EXTRA_HOST, host.id)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        return PendingIntent.getActivity(context, id, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
    }

    private fun notify(context: Context, id: Int, builder: NotificationCompat.Builder) {
        try {
            NotificationManagerCompat.from(context).notify(id, builder.build())
        } catch (_: SecurityException) {
            // Notification permission not granted; nothing to show.
        }
    }
}
