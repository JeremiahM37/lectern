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
        for (p in listOf("/terminal/session/7", "/terminal/attempt-shell/3", "/terminal/project/12/")) assertTrue(p, Shell.isTerminalPage(p))
        for (p in listOf("/terminal/session/x", "/terminal/other/1", "/terminal/session/0", "/terminal/session/1/../x")) assertFalse(p, Shell.isTerminalPage(p))
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

    @Test
    fun appPairingLinksAreRecognised() {
        // frontend/src/pairing/links.ts makes these from the https links.
        assertEquals(Link.Relay("eyJ2IjoxfQ"), Link.parse("lectern://pair?p=eyJ2IjoxfQ"))
        assertEquals(
            Link.DirectPair("http://10.0.2.2:19210", "ABCD1234"),
            Link.parse("lectern://pair?origin=http%3A%2F%2F10.0.2.2%3A19210&code=ABCD1234"),
        )
        assertNull(Link.parse("lectern://pair?origin=javascript%3Aalert(1)&code=X"))
        assertNull(Link.parse("lectern://pair?origin=https%3A%2F%2Fh.example&code=A%20B"))
        assertNull(Link.parse("lectern://pair?p=../../x"))
        assertNull(Link.parse("lectern://evil?p=abc"))
        assertNull(Link.parse("lectern://pair"))
    }

    @Test
    fun relayPairingsEachHaveAPrivateOrigin() {
        assertTrue(Shell.isRelayOrigin(Shell.APP_ORIGIN))
        assertTrue(Shell.isRelayOrigin(Shell.relayOrigin("h2")))
        assertFalse(Shell.isRelayOrigin("https://app.lectern.invalid.evil.example"))
        assertFalse(Shell.isRelayOrigin("http://h2.app.lectern.invalid"))
        assertFalse(Shell.isRelayOrigin("https://lectern.example"))
    }

    @Test
    fun hostsAreNamedFromWhereTheyAre() {
        assertEquals("aiserver.example.ts.net", Hosts.labelFor(Bridge.MODE_DIRECT, "https://aiserver.example.ts.net:8443", null))
        assertEquals("Lectern via relay.example.com", Hosts.labelFor(Bridge.MODE_RELAY, Shell.relayOrigin("h2"), """{"relay":"wss://relay.example.com"}"""))
        assertEquals("Lectern (relay)", Hosts.labelFor(Bridge.MODE_RELAY, Shell.APP_ORIGIN, null))
    }

    @Test
    fun aDismissalNamesTheNotificationItRemoves() {
        val shown = org.json.JSONObject().put("kind", "approval").put("approval_id", 7)
        val gone = org.json.JSONObject().put("kind", "dismiss").put("tag", "approval-7")
        assertEquals(Notifications.tagOf(shown), Notifications.tagOf(gone))
        val host = Host("h2", "Work", Bridge.MODE_DIRECT, "https://w.example", true, "k", "h2")
        val other = host.copy(id = "h3")
        // The same approval number on two Lecterns is two notifications.
        assertFalse(Notifications.idOf(Notifications.scopedTag(host, "approval-7")) == Notifications.idOf(Notifications.scopedTag(other, "approval-7")))
    }
}
