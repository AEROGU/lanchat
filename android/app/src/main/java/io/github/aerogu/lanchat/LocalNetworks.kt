package io.github.aerogu.lanchat

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.util.Log
import io.github.aerogu.lanchat.mobile.Mobile
import java.net.Inet4Address
import java.util.concurrent.ConcurrentHashMap

/**
 * Informa al núcleo las redes locales del teléfono (Wi-Fi y Ethernet) cada vez
 * que cambian. Go no puede leerlas en Android (las apps no tienen permiso de
 * netlink), y sin ellas no reconoce sus propias IP ni envía el broadcast
 * dirigido a cada red.
 */
class LocalNetworks(context: Context) : ConnectivityManager.NetworkCallback() {
    private val cm = context.getSystemService(ConnectivityManager::class.java)
    private val nets = ConcurrentHashMap<Network, List<String>>()

    fun start() {
        val request = NetworkRequest.Builder()
            .apply { TRANSPORTS.forEach(::addTransportType) }
            // La red de la oficina puede no tener salida a internet.
            .removeCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        cm.registerNetworkCallback(request, this)
        // La red actual de una vez: las llamadas llegan en otro hilo, un poco
        // después, y el núcleo ya debe conocer sus IP en el primer saludo.
        cm.activeNetwork?.let { n ->
            val caps = cm.getNetworkCapabilities(n)
            if (caps != null && TRANSPORTS.any(caps::hasTransport)) {
                cm.getLinkProperties(n)?.let { update(n, it) }
            }
        }
    }

    fun stop() {
        cm.unregisterNetworkCallback(this)
    }

    // Antes de Android 8 no siempre llega onLinkPropertiesChanged al registrarse.
    override fun onAvailable(network: Network) {
        cm.getLinkProperties(network)?.let { update(network, it) }
    }

    override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) = update(network, lp)

    override fun onLost(network: Network) {
        if (nets.remove(network) != null) publish()
    }

    private fun update(network: Network, lp: LinkProperties) {
        val cidrs = lp.linkAddresses
            .filter { it.address is Inet4Address }
            .map { "${it.address.hostAddress}/${it.prefixLength}" }
        if (nets.put(network, cidrs) != cidrs) publish()
    }

    private fun publish() {
        val all = nets.values.flatten().distinct().joinToString(" ")
        runCatching { Mobile.setNetworks(all) }.onFailure { Log.w("LanChat", "redes locales: $all", it) }
    }

    private companion object {
        /** Redes locales; los datos móviles no llevan a otros equipos de la oficina. */
        val TRANSPORTS = listOf(NetworkCapabilities.TRANSPORT_WIFI, NetworkCapabilities.TRANSPORT_ETHERNET)
    }
}
