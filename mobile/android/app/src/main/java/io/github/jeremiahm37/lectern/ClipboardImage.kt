package io.github.jeremiahm37.lectern

import android.content.ClipboardManager
import android.content.ContentResolver
import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.Uri
import java.io.ByteArrayOutputStream

/** Reading the Android clipboard. Android only lets the app with input focus do it. */
object ClipboardImage {
    class Image(val mime: String, val bytes: ByteArray)

    private fun manager(context: Context) = context.applicationContext.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager

    /** The MIME types the primary clip holds (empty when none, or when not allowed to look). */
    fun types(context: Context): List<String> = runCatching {
        val d = manager(context).primaryClipDescription ?: return emptyList()
        (0 until d.mimeTypeCount).map { d.getMimeType(it) }
    }.getOrDefault(emptyList())

    fun text(context: Context): String? = runCatching {
        manager(context).primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(context)?.toString()?.takeIf { it.isNotEmpty() }
    }.getOrNull()

    /** The clipboard's image as it is, or null. Never more than [ClipboardLogic.MAX_BYTES]. */
    fun read(context: Context): Image? = runCatching {
        val clip = manager(context).primaryClip ?: return null
        for (i in 0 until clip.itemCount) {
            val uri: Uri = clip.getItemAt(i).uri ?: continue
            val resolver = context.contentResolver
            val mime = (if (uri.scheme == ContentResolver.SCHEME_CONTENT) resolver.getType(uri) else null) ?: continue
            if (!mime.startsWith("image/")) continue
            val bytes = resolver.openInputStream(uri)?.use { readCapped(it) } ?: continue
            if (ClipboardLogic.imageEligible(mime, bytes.size.toLong())) return Image(mime, bytes)
        }
        null
    }.getOrNull()

    /** [Image] as PNG: unchanged when it already is one, else decoded and re-encoded. */
    fun asPng(image: Image): Image? {
        if (image.mime.equals("image/png", true)) return image
        val bmp = BitmapFactory.decodeByteArray(image.bytes, 0, image.bytes.size) ?: return null
        val out = ByteArrayOutputStream()
        bmp.compress(Bitmap.CompressFormat.PNG, 100, out)
        bmp.recycle()
        return out.toByteArray().takeIf { ClipboardLogic.imageEligible("image/png", it.size.toLong()) }?.let { Image("image/png", it) }
    }

    /** What the mirror and attachments take: the original when the server accepts it, else PNG. */
    fun acceptable(image: Image): Image? = if (ClipboardLogic.mirrorable(image.mime)) image else asPng(image)

    fun readCapped(input: java.io.InputStream): ByteArray? {
        val out = ByteArrayOutputStream()
        val buf = ByteArray(64 * 1024)
        while (true) {
            val n = input.read(buf)
            if (n < 0) break
            out.write(buf, 0, n)
            if (out.size() > ClipboardLogic.MAX_BYTES) return null
        }
        return out.toByteArray()
    }
}
