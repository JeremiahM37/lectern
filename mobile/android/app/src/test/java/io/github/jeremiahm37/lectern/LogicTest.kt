package io.github.jeremiahm37.lectern

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class LogicTest {
    @Test
    fun pairingLinksAreRecognised() {
        assertEquals(Link.Relay("eyJ2IjoxfQ"), Link.parse("https://lectern.example:8443/relay-pair#p=eyJ2IjoxfQ"))
        assertEquals(Link.DirectPair("http://10.0.2.2:19210", "ABCD-1234"), Link.parse(" http://10.0.2.2:19210/pair#code=ABCD-1234 "))
        assertEquals(Link.Direct("https://aiserver.example.ts.net:8443"), Link.parse("https://aiserver.example.ts.net:8443/#board"))
        assertNull(Link.parse("javascript:alert(1)"))
        assertNull(Link.parse("file:///sdcard/x"))
        assertNull(Link.parse("not a link"))
        assertNull(Link.parse("https://x.example/relay-pair#nothing"))
    }

    @Test
    fun appRoutesMirrorTheServer() {
        for (p in listOf("/session/7", "/pair", "/relay-pair", "/native-action", "/board")) assertTrue(p, Shell.isAppRoute(p))
        for (p in listOf("/", "/api/tasks", "/api", "/term/1/ws", "/a2a/v1", "/static/x", "/react/assets/app.js", "/.well-known/x")) {
            assertFalse(p, Shell.isAppRoute(p))
        }
    }

    @Test
    fun notificationButtonsMakeTheRightCalls() {
        val approve = Actions.plan(Actions.APPROVE, 7, null)!!
        assertEquals("/api/approvals/7/decision", approve.path)
        assertEquals("approved", approve.body!!.getString("decision"))
        assertEquals("denied", Actions.plan(Actions.DENY, 7, null)!!.body!!.getString("decision"))
        val reply = Actions.plan(Actions.REPLY_APPROVAL, 7, "try staging")!!
        assertEquals("denied", reply.body!!.getString("decision"))
        assertEquals("try staging", reply.body!!.getString("note"))
        assertEquals("/api/sessions/3/send", Actions.plan(Actions.REPLY_SESSION, 3, "go on")!!.path)
        assertNull(Actions.plan(Actions.REPLY_SESSION, 3, "  "))
        assertNull(Actions.plan("unknown", 3, null))
    }

    @Test
    fun deepLinksStayOnThisLectern() {
        assertEquals("/session/7", MainActivity.safePath("/session/7"))
        assertEquals("/", MainActivity.safePath("//evil.example/x"))
        assertEquals("/", MainActivity.safePath("https://evil.example/"))
        assertEquals("/", MainActivity.safePath(null))
    }
}
