package io.github.aerogu.lanchat

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Environment
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log
import androidx.core.app.NotificationManagerCompat
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
    private var screen: ScreenIdle? = null
    private var destroyed = false

    override fun onCreate() {
        super.onCreate()
        running = true
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

        val screen = ScreenIdle(applicationContext).also { it.start() }
        this.screen = screen

        result = null
        val host = LanChatHost(applicationContext, screen)
        val dataDir = filesDir.absolutePath
        val name = deviceName()
        val downloads = downloadDir()
        core.execute {
            val r = runCatching { Mobile.start(dataDir, name, downloads, host) }
            r.onFailure { Log.e(TAG, "no se pudo iniciar", it) }
            main.post { if (!destroyed) publish(r) }
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) { // "Detener" en el aviso fijo
            stopSelf()
            return START_NOT_STICKY
        }
        return START_STICKY
    }

    override fun onBind(intent: Intent?) = null

    override fun onDestroy() {
        running = false
        destroyed = true
        result = null
        networks?.stop()
        screen?.stop()
        multicastLock?.release()
        val context = applicationContext
        // Stop espera a que el núcleo se despida de la red: fuera del hilo principal.
        core.execute {
            runCatching { Mobile.stop() }.onFailure { Log.w(TAG, "al detener", it) }
            // Por si un aviso de no leídos volvió a poner el aviso fijo mientras se detenía.
            if (!running) NotificationManagerCompat.from(context).cancel(Notifications.SERVICE_ID)
        }
        stopped.toList().forEach { it() }
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

    /**
     * Descargas/LanChat, donde los ven el explorador de archivos y la galería:
     * desde Android 11 una app puede crear archivos ahí sin pedir permisos (y
     * siguen ahí si se desinstala). Antes, o si no se puede, la carpeta de la
     * app en el almacenamiento compartido.
     */
    private fun downloadDir(): String {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            @Suppress("DEPRECATION") // solo la ruta; los archivos los escribe Go
            val public = File(Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS), "LanChat")
            if (public.isDirectory || public.mkdirs()) return public.absolutePath
            Log.w(TAG, "no se pudo crear $public; se usa la carpeta de la app")
        }
        return (getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS) ?: File(filesDir, "Descargas")).absolutePath
    }

    companion object {
        private const val TAG = "LanChat"
        const val ACTION_STOP = "io.github.aerogu.lanchat.STOP"

        /** El servicio está en marcha (entre onCreate y onDestroy). */
        @Volatile
        var running = false
            private set

        /**
         * Detiene LanChat: se despide de la red y deja de recibir mensajes hasta
         * que se vuelva a abrir la app.
         */
        fun stop(context: Context) {
            context.stopService(Intent(context, LanChatService::class.java))
        }

        // Se llaman en el hilo principal cuando el servicio se detiene.
        private val stopped = mutableListOf<() -> Unit>()

        fun whenStopped(callback: () -> Unit) {
            stopped += callback
        }

        fun cancelStopped(callback: () -> Unit) {
            stopped -= callback
        }

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
