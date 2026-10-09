package io.github.aerogu.lanchat

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.PowerManager
import android.os.SystemClock
import androidx.core.content.ContextCompat

/**
 * Desde cuándo está apagada la pantalla: en el teléfono es la inactividad del
 * ausente automático (en Windows, el tiempo sin usar teclado ni ratón).
 */
class ScreenIdle(private val context: Context) : BroadcastReceiver() {
    // elapsedRealtime al apagarse (sigue contando con el teléfono dormido); 0 = encendida.
    @Volatile
    private var offSince = 0L

    fun start() {
        if (!context.getSystemService(PowerManager::class.java).isInteractive) {
            offSince = SystemClock.elapsedRealtime()
        }
        val filter = IntentFilter().apply {
            addAction(Intent.ACTION_SCREEN_OFF)
            addAction(Intent.ACTION_SCREEN_ON)
        }
        ContextCompat.registerReceiver(context, this, filter, ContextCompat.RECEIVER_NOT_EXPORTED)
    }

    fun stop() {
        context.unregisterReceiver(this)
    }

    override fun onReceive(context: Context, intent: Intent) {
        offSince = if (intent.action == Intent.ACTION_SCREEN_OFF) SystemClock.elapsedRealtime() else 0L
    }

    /** Segundos con la pantalla apagada; 0 si está encendida. */
    fun idleSeconds(): Long {
        val since = offSince
        return if (since == 0L) 0 else (SystemClock.elapsedRealtime() - since) / 1000
    }
}
