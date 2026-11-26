package me.itsjustin.clrd

import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.security.KeyStore
import java.security.PrivateKey
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import javax.net.ssl.KeyManagerFactory
import javax.net.ssl.SSLContext

/**
 * Client certificates (#152): the picked key reaches the clrd server and no other host. The fixtures in
 * resources/mtls are a throwaway test CA, server and client, made for this test and trusted nowhere else.
 * The server's certificate names localhost and 127.0.0.1.
 */
class ClientCertTest {
    private val server = MockWebServer()
    private var alias: String? = null
    private var origin: Pair<String, Int>? = null
    private var lookups = 0

    private fun p12(name: String) = KeyStore.getInstance("PKCS12").apply { load(ClientCertTest::class.java.getResourceAsStream("/mtls/$name.p12"), "test".toCharArray()) }
    private val client = p12("client")
    private val ca = KeyStore.getInstance("PKCS12").apply {
        load(null)
        setCertificateEntry("ca", CertificateFactory.getInstance("X.509").generateCertificate(ClientCertTest::class.java.getResourceAsStream("/mtls/ca.pem")))
    }

    private val keys = ClientCertKeys({ alias?.let { a -> origin?.let { a to it } } }, ca) { a ->
        lookups++
        (client.getKey(a, "test".toCharArray()) as PrivateKey) to client.getCertificateChain(a).map { it as X509Certificate }.toTypedArray()
    }
    private val http = OkHttpClient.Builder().sslSocketFactory(keys.socketFactory, keys.trustManager)
        .dns(object : okhttp3.Dns { override fun lookup(hostname: String) = listOf(java.net.InetAddress.getByName("127.0.0.1")) }) // every name is this machine; the server listens on IPv4 only
        .build()

    private fun call(host: String) = http.newCall(Request.Builder().url("https://$host:${server.port}/").build()).execute().use { assertEquals(200, it.code) }

    /** The peer certificates the server saw on one request to [host]. */
    private fun sent(host: String): Int {
        server.enqueue(MockResponse().setBody("ok"))
        call(host)
        return server.takeRequest().handshake!!.peerCertificates.size
    }

    init {
        val kmf = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm()).apply { init(p12("server"), "test".toCharArray()) }
        server.useHttps(SSLContext.getInstance("TLS").apply { init(kmf.keyManagers, javax.net.ssl.TrustManagerFactory.getInstance("PKIX").apply { init(ca) }.trustManagers, null) }.socketFactory, false)
        server.requestClientAuth()
        server.start()
    }

    @After fun tearDown() = server.shutdown()

    private fun pick(host: String = "localhost") {
        alias = "client"
        origin = host to server.port
    }

    @Test fun noPickSendsNoCertificate() = assertEquals(0, sent("localhost"))

    @Test fun thePickReachesTheServer() {
        pick()
        assertTrue(sent("localhost") > 0)
    }

    @Test fun anotherHostNeverSeesIt() {
        pick()
        assertEquals(0, sent("127.0.0.1")) // same machine, but not the host it was picked for
    }

    @Test fun anotherPortNeverSeesIt() {
        alias = "client"
        origin = "localhost" to server.port + 1
        assertEquals(0, sent("localhost"))
    }

    @Test fun aServerNotNamedInItsCertificateNeverSeesIt() {
        pick("clrd.test") // trusted CA, but the certificate does not name this host
        server.enqueue(MockResponse().setBody("ok"))
        assertThrows(IOException::class.java) { call("clrd.test") }
        assertEquals(0, lookups) // refused before the key was ever used
    }

    @Test fun removingItStopsItAtOnce() {
        pick()
        assertTrue(sent("localhost") > 0)
        alias = null
        keys.renew()
        http.connectionPool.evictAll()
        assertEquals(0, sent("localhost")) // a resumed session would still carry the old certificate
    }
}
