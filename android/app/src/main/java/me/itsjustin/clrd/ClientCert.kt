package me.itsjustin.clrd

import java.net.Socket
import java.security.KeyStore
import java.security.Principal
import java.security.PrivateKey
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLEngine
import javax.net.ssl.SSLSocket
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509ExtendedKeyManager
import javax.net.ssl.X509TrustManager

/**
 * Client certificate for mutual TLS (#152). Offers the key picked from Android's key store, and only to
 * the clrd server's own host, so a site an image or action link points at never sees it. The key stays
 * in KeyChain; [keys] looks it up on the connecting thread, never the main one.
 */
class ClientCertKeys(
    private val alias: () -> String?,
    private val host: () -> String?,
    private val keys: (String) -> Pair<PrivateKey, Array<X509Certificate>>?,
) : X509ExtendedKeyManager() {
    override fun chooseClientAlias(keyType: Array<String>?, issuers: Array<Principal>?, socket: Socket?): String? {
        val a = alias() ?: return null
        val peer = (socket as? SSLSocket)?.handshakeSession?.peerHost ?: return null
        return a.takeIf { peer.equals(host(), ignoreCase = true) }
    }
    override fun chooseEngineClientAlias(keyType: Array<String>?, issuers: Array<Principal>?, engine: SSLEngine?): String? = null
    override fun getPrivateKey(alias: String): PrivateKey? = keys(alias)?.first
    override fun getCertificateChain(alias: String): Array<X509Certificate>? = keys(alias)?.second
    override fun getClientAliases(keyType: String?, issuers: Array<Principal>?): Array<String>? = null
    override fun getServerAliases(keyType: String?, issuers: Array<Principal>?): Array<String>? = null
    override fun chooseServerAlias(keyType: String?, issuers: Array<Principal>?, socket: Socket?): String? = null

    /** TLS for OkHttp with these keys and the platform's default trust, which on Android is the network security config (user CAs included). */
    fun tls(trust: KeyStore? = null): Pair<SSLContext, X509TrustManager> {
        val tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm()).apply { init(trust) }
        val tm = tmf.trustManagers.filterIsInstance<X509TrustManager>().first()
        return SSLContext.getInstance("TLS").apply { init(arrayOf(this@ClientCertKeys), arrayOf(tm), null) } to tm
    }
}
