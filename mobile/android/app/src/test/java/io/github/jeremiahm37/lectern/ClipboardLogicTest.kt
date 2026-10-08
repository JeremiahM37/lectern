package io.github.jeremiahm37.lectern

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ClipboardLogicTest {
    @Test
    fun requestsAreParsedStrictly() {
        assertEquals(ClipboardLogic.Request("a1-b", "list", "image/png"), ClipboardLogic.parseRequest("""{"id":"a1-b","op":"list","type":"image/png"}"""))
        assertEquals("text/plain", ClipboardLogic.parseRequest("""{"id":"x","op":"read","type":"text/plain"}""")!!.type)
        assertNull(ClipboardLogic.parseRequest("""{"id":"x","op":"read","type":"application/zip"}"""))
        assertNull(ClipboardLogic.parseRequest("""{"id":"../x","op":"list","type":"image/png"}"""))
        assertNull(ClipboardLogic.parseRequest("""{"id":"x","op":"write","type":"image/png"}"""))
        assertNull(ClipboardLogic.parseRequest("not json"))
    }

    @Test
    fun clientIdsAndPaths() {
        assertTrue(ClipboardLogic.validClient("android-0123456789abcdef"))
        assertFalse(ClipboardLogic.validClient("abc"))
        assertFalse(ClipboardLogic.validClient("bad id!"))
        assertFalse(ClipboardLogic.validClient(null))
        assertEquals("/api/clipboard/listen?client=abcd&kind=android&can_read=1", ClipboardLogic.listenPath("abcd", null))
        assertEquals("/api/clipboard/listen?client=abcd&kind=android&can_read=1&session=7", ClipboardLogic.listenPath("abcd", "7"))
        assertEquals("/api/clipboard/listen?client=abcd&kind=android&can_read=1", ClipboardLogic.listenPath("abcd", "7&x=1"))
    }

    @Test
    fun clipboardTypesAndEligibility() {
        assertEquals(listOf("image/png", "text/plain"), ClipboardLogic.advertisedTypes(listOf("image/jpeg", "text/html")))
        assertEquals(listOf("text/plain"), ClipboardLogic.advertisedTypes(listOf("text/plain")))
        assertEquals(emptyList<String>(), ClipboardLogic.advertisedTypes(listOf("application/pdf")))
        assertTrue(ClipboardLogic.imageEligible("image/png", 1))
        assertTrue(ClipboardLogic.imageEligible("image/webp", ClipboardLogic.MAX_BYTES))
        assertFalse(ClipboardLogic.imageEligible("image/png", ClipboardLogic.MAX_BYTES + 1))
        assertFalse(ClipboardLogic.imageEligible("image/png", 0))
        assertFalse(ClipboardLogic.imageEligible("text/plain", 10))
        assertFalse(ClipboardLogic.imageEligible(null, 10))
        assertTrue(ClipboardLogic.mirrorable("image/JPEG"))
        assertFalse(ClipboardLogic.mirrorable("image/heic"))
    }

    @Test
    fun repliesCarryTheRightHeaders() {
        val list = ClipboardLogic.headers(ClipboardLogic.listReply(listOf("image/png", "text/plain")))
        assertEquals("image/png,text/plain", list["X-Clipboard-Types"])
        assertTrue(ClipboardLogic.listReply(emptyList()).body.isNotEmpty())
        val un = ClipboardLogic.headers(ClipboardLogic.unavailable())
        assertEquals("unavailable", un["X-Clipboard-Status"])
        assertFalse(un.containsKey("X-Clipboard-Types"))
        val read = ClipboardLogic.readReply("image/png", byteArrayOf(1, 2))
        assertFalse(read.unavailable)
        assertEquals("image/png", ClipboardLogic.headers(read)["Content-Type"])
        assertTrue(ClipboardLogic.readReply("image/png", null).unavailable)
        assertTrue(ClipboardLogic.readReply("image/png", ByteArray(0)).unavailable)
    }

    @Test
    fun serverSentEventsAreFramed() {
        val got = mutableListOf<Pair<String, String>>()
        val sse = ClipboardLogic.Sse { e, d -> got += e to d }
        for (l in listOf(": keepalive", "", "event: request\r", "data: {\"id\":", "data: 1}", "", "data: plain", "")) sse.line(l)
        assertEquals(listOf("request" to "{\"id\":\n1}", "message" to "plain"), got)
    }

    @Test
    fun pathsAreQuotedAndUploadsFramed() {
        assertEquals("/tmp/a/b.png ", ClipboardLogic.quotePath("/tmp/a/b.png"))
        assertEquals("'/tmp/my shot.png' ", ClipboardLogic.quotePath("/tmp/my shot.png"))
        assertEquals("'/tmp/it'\\''s.png' ", ClipboardLogic.quotePath("/tmp/it's.png"))
        val body = ClipboardLogic.multipart("B", "a b\".png", "image/png", byteArrayOf(9))
        val text = body.decodeToString()
        assertTrue(text.startsWith("--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a_b_.png\""))
        assertTrue(text.endsWith("\r\n--B--\r\n"))
    }

    @Test
    fun activeCallsAreThrottled() {
        assertTrue(ClipboardLogic.activeDue(5000, 0))
        assertFalse(ClipboardLogic.activeDue(5000, 4000))
        assertTrue(ClipboardLogic.activeDue(6000, 4000))
    }
}
