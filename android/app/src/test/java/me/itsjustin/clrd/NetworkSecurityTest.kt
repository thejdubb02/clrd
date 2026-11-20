package me.itsjustin.clrd

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.w3c.dom.Element
import java.io.File
import javax.xml.parsers.DocumentBuilderFactory

/**
 * Servers behind a home-lab certificate authority need the app to trust CAs the
 * person installed in Android settings, and plain http servers on a home network
 * still need cleartext. Both live in the network security config.
 */
class NetworkSecurityTest {
    private fun xml(path: String) =
        DocumentBuilderFactory.newInstance().apply { isNamespaceAware = true }
            .newDocumentBuilder().parse(File(path)).documentElement

    @Test fun manifestUsesTheConfig() {
        val app = xml("src/main/AndroidManifest.xml").getElementsByTagName("application").item(0) as Element
        assertEquals(
            "@xml/network_security_config",
            app.getAttributeNS("http://schemas.android.com/apk/res/android", "networkSecurityConfig"),
        )
    }

    @Test fun trustsUserCasAndAllowsCleartext() {
        val base = xml("src/main/res/xml/network_security_config.xml")
            .getElementsByTagName("base-config").item(0) as Element
        assertEquals("true", base.getAttribute("cleartextTrafficPermitted"))
        val certs = base.getElementsByTagName("certificates")
        val srcs = (0 until certs.length).map { (certs.item(it) as Element).getAttribute("src") }
        assertTrue("system CAs: $srcs", "system" in srcs)
        assertTrue("user CAs: $srcs", "user" in srcs)
    }
}
