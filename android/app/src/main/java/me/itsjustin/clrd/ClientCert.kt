package me.itsjustin.clrd

import okhttp3.internal.tls.OkHostnameVerifier
import java.net.InetAddress
import java.net.Socket
import java.security.KeyStore
import java.security.Principal
import java.security.PrivateKey
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLEngine
import javax.net.ssl.SSLSession
import javax.net.ssl.SSLSocket
import javax.net.ssl.SSLSocketFactory
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509ExtendedKeyManager
import javax.net.ssl.X509ExtendedTrustManager
import javax.net.ssl.X509TrustManager

/**
 * Client certificate for mutual TLS (#152). Offers the key picked from Android's key store, and only to
 * the host and port it was picked for ([pick], null when the server has moved since), so a site an
 * image or action link points at never sees it. The server's certificate must name that host before the
 * key is offered: the trust check below verifies the hostname during the handshake, where OkHttp would
 * only do it after the key had been sent. The key stays in KeyChain; [keys] looks it up on the
 * connecting thread, never the main one.
 */
class ClientCertKeys(
    private val pick: () -> Pair<String, Pair<String, Int>>?, // the alias and its host and port, read once per handshake
    private val trust: KeyStore? = null,
    private val keys: (String) -> Pair<PrivateKey, Array<X509Certificate>>?,
): X509ExtendedKeyManager() {
    override fun chooseClientAlias(keyType: Array<String>?, issuers: Array<Principal>?, socket: Socket?): String? {
        val (a, at) = pick() ?: return null
        val (host, port) = at
        val s = (socket as? SSLSocket)?.handshakeSession ?: return null
        return a.takeIf { s.peerHost.equals(host, ignoreCase = true) && s.peerPort == port }
    }
    override fun chooseEngineClientAlias(keyType: Array<String>?, issuers: Array<Principal>?, engine: SSLEngine?): String? = null
    override fun getPrivateKey(alias: String): PrivateKey? = keys(alias)?.first
    override fun getCertificateChain(alias: String): Array<X509Certificate>? = keys(alias)?.second
    override fun getClientAliases(keyType: String?, issuers: Array<Principal>?): Array<String>? = null
    override fun getServerAliases(keyType: String?, issuers: Array<Principal>?): Array<String>? = null
    override fun chooseServerAlias(keyType: String?, issuers: Array<Principal>?, socket: Socket?): String? = null

    /** The platform's default trust, which on Android is the network security config (user CAs included). For OkHttp's own use. */
    val trustManager: X509TrustManager = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm()).apply { init(trust) }
        .trustManagers.filterIsInstance<X509TrustManager>().first()

    @Volatile private var tls = fresh()
    private fun fresh() = SSLContext.getInstance("TLS").apply { init(arrayOf(this@ClientCertKeys), arrayOf(HostChecked(trustManager)), null) }

    /** A new TLS context, so no session saved with the old certificate (or without one) is resumed. Pair with evicting the connection pool. */
    fun renew() { tls = fresh() }

    /** Sockets from whichever context is current, so every client built from this one picks up [renew]. */
    val socketFactory: SSLSocketFactory = object : SSLSocketFactory() {
        private val f get() = tls.socketFactory
        override fun getDefaultCipherSuites(): Array<String> = f.defaultCipherSuites
        override fun getSupportedCipherSuites(): Array<String> = f.supportedCipherSuites
        override fun createSocket(s: Socket?, host: String?, port: Int, autoClose: Boolean): Socket = f.createSocket(s, host, port, autoClose)
        override fun createSocket(): Socket = f.createSocket()
        override fun createSocket(host: String?, port: Int): Socket = f.createSocket(host, port)
        override fun createSocket(host: String?, port: Int, local: InetAddress?, localPort: Int): Socket = f.createSocket(host, port, local, localPort)
        override fun createSocket(host: InetAddress?, port: Int): Socket = f.createSocket(host, port)
        override fun createSocket(address: InetAddress?, port: Int, local: InetAddress?, localPort: Int): Socket = f.createSocket(address, port, local, localPort)
    }
}

/** Trust as [tm] decides, plus the certificate must name the host connected to. */
private class HostChecked(private val tm: X509TrustManager) : X509ExtendedTrustManager() {
    private fun named(chain: Array<X509Certificate>, s: SSLSession?) {
        val host = s?.peerHost
        if (host == null || !OkHostnameVerifier.verify(host, chain[0])) throw CertificateException("certificate is not for $host")
    }
    override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String, socket: Socket?) {
        if (tm is X509ExtendedTrustManager) tm.checkServerTrusted(chain, authType, socket) else tm.checkServerTrusted(chain, authType)
        named(chain, (socket as? SSLSocket)?.handshakeSession)
    }
    override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String, engine: SSLEngine?) {
        if (tm is X509ExtendedTrustManager) tm.checkServerTrusted(chain, authType, engine) else tm.checkServerTrusted(chain, authType)
        named(chain, engine?.handshakeSession)
    }
    override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String) = throw CertificateException("no host to check")
    override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String, socket: Socket?) = throw CertificateException("client only")
    override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String, engine: SSLEngine?) = throw CertificateException("client only")
    override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String) = throw CertificateException("client only")
    override fun getAcceptedIssuers(): Array<X509Certificate> = tm.acceptedIssuers
}
