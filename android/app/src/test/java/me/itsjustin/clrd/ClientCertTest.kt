package me.itsjustin.clrd

import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.security.KeyStore
import java.security.PrivateKey
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import javax.net.ssl.KeyManagerFactory
import javax.net.ssl.SSLContext

/**
 * Client certificates (#152): the picked key reaches the clrd server and no other host. The fixtures in
 * resources/mtls are a throwaway test CA, server and client, made for this test and trusted nowhere else.
 */
class ClientCertTest {
    private val server = MockWebServer()
    private var alias: String? = null

    private fun p12(name: String) = KeyStore.getInstance("PKCS12").apply { load(ClientCertTest::class.java.getResourceAsStream("/mtls/$name.p12"), "test".toCharArray()) }
    private val client = p12("client")
    private val ca = KeyStore.getInstance("PKCS12").apply {
        load(null)
        setCertificateEntry("ca", CertificateFactory.getInstance("X.509").generateCertificate(ClientCertTest::class.java.getResourceAsStream("/mtls/ca.pem")))
    }

    private fun http(host: String): OkHttpClient {
        val keys = ClientCertKeys({ alias }, { host }) { a ->
            (client.getKey(a, "test".toCharArray()) as PrivateKey) to client.getCertificateChain(a).map { it as X509Certificate }.toTypedArray()
        }
        val (tls, trust) = keys.tls(ca)
        return OkHttpClient.Builder().sslSocketFactory(tls.socketFactory, trust)
            .dns(object : okhttp3.Dns { override fun lookup(hostname: String) = listOf(java.net.InetAddress.getByName("127.0.0.1")) }) // the server listens on IPv4 only
            .build()
    }

    /** The peer certificates the server saw on one request to [host]. */
    private fun sent(serverHost: String, host: String): Int {
        server.enqueue(MockResponse().setBody("ok"))
        http(serverHost).newCall(Request.Builder().url("https://$host:${server.port}/").build()).execute().use { assertEquals(200, it.code) }
        return server.takeRequest().handshake!!.peerCertificates.size
    }

    init {
        val kmf = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm()).apply { init(p12("server"), "test".toCharArray()) }
        server.useHttps(SSLContext.getInstance("TLS").apply { init(kmf.keyManagers, javax.net.ssl.TrustManagerFactory.getInstance("PKIX").apply { init(ca) }.trustManagers, null) }.socketFactory, false)
        server.requestClientAuth()
        server.start()
    }

    @After fun tearDown() = server.shutdown()

    @Test fun noPickSendsNoCertificate() = assertEquals(0, sent("localhost", "localhost"))

    @Test fun thePickReachesTheServer() {
        alias = "client"
        assertTrue(sent("localhost", "localhost") > 0)
    }

    @Test fun anotherHostNeverSeesIt() {
        alias = "client"
        assertEquals(0, sent("localhost", "127.0.0.1")) // same machine, but not the server's name
    }
}
