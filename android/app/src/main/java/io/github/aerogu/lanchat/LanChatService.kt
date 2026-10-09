package io.github.aerogu.lanchat

import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Environment
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log
import androidx.core.app.ServiceCompat
import io.github.aerogu.lanchat.mobile.Mobile
import java.io.File
import java.util.concurrent.Executors

/**
 * Servicio en primer plano que mantiene el núcleo en Go funcionando aunque se
 * cierre la ventana: Android no lo detiene mientras muestre su aviso fijo.
 */
class LanChatService : Service() {
    private var multicastLock: WifiManager.MulticastLock? = null
    private var networks: LocalNetworks? = null
    private var destroyed = false

    override fun onCreate() {
        super.onCreate()
        Notifications.createChannels(this)
        ServiceCompat.startForeground(
            this,
            Notifications.SERVICE_ID,
            Notifications.service(this, 0),
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
            } else {
                0
            },
        )
        // Sin este candado, el Wi-Fi descarta los broadcasts del descubrimiento.
        multicastLock = applicationContext.getSystemService(WifiManager::class.java)
            .createMulticastLock("lanchat")
            .apply {
                setReferenceCounted(false)
                acquire()
            }
        // Antes de Mobile.start, para que el primer saludo ya use las redes.
        networks = LocalNetworks(applicationContext).also { it.start() }

        result = null
        val host = LanChatHost(applicationContext)
        val dataDir = filesDir.absolutePath
        val name = deviceName()
        val downloads = downloadDir()
        core.execute {
            val r = runCatching { Mobile.start(dataDir, name, downloads, host) }
            r.onFailure { Log.e(TAG, "no se pudo iniciar", it) }
            main.post { if (!destroyed) publish(r) }
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int) = START_STICKY

    override fun onBind(intent: Intent?) = null

    override fun onDestroy() {
        destroyed = true
        result = null
        networks?.stop()
        multicastLock?.release()
        // Stop espera a que el núcleo se despida de la red: fuera del hilo principal.
        core.execute { runCatching { Mobile.stop() }.onFailure { Log.w(TAG, "al detener", it) } }
        super.onDestroy()
    }

    /** El nombre que verán los demás: el que el usuario dio al teléfono, o el modelo. */
    private fun deviceName(): String {
        val name = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N_MR1) {
            Settings.Global.getString(contentResolver, Settings.Global.DEVICE_NAME)
        } else {
            null
        }
        return name?.takeIf { it.isNotBlank() } ?: Build.MODEL
    }

    /** Carpeta de la app en el almacenamiento compartido: no pide permisos. */
    private fun downloadDir(): String =
        (getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS) ?: File(filesDir, "Descargas")).absolutePath

    companion object {
        private const val TAG = "LanChat"

        /** Arranca y detiene el núcleo en orden, sin bloquear el hilo principal. */
        private val core = Executors.newSingleThreadExecutor()
        private val main = Handler(Looper.getMainLooper())

        // Solo se tocan en el hilo principal.
        private var result: Result<String>? = null
        private val waiting = mutableListOf<(Result<String>) -> Unit>()

        /**
         * Llama a [callback] en el hilo principal con la dirección de la interfaz
         * (o el error de arranque): ahora mismo o cuando el núcleo termine de arrancar.
         */
        fun whenStarted(callback: (Result<String>) -> Unit) {
            val r = result
            if (r != null) callback(r) else waiting += callback
        }

        fun cancel(callback: (Result<String>) -> Unit) {
            waiting -= callback
        }

        private fun publish(r: Result<String>) {
            result = r
            val callbacks = waiting.toList()
            waiting.clear()
            callbacks.forEach { it(r) }
        }
    }
}
