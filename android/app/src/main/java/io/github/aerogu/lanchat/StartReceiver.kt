package io.github.aerogu.lanchat

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import androidx.core.content.ContextCompat
import androidx.core.content.edit

/**
 * Arranca LanChat al encender el teléfono y después de actualizar la app (al
 * instalar una versión nueva, Android detiene el servicio), si la opción está
 * activada: si no, no recibiría mensajes hasta que alguien lo abriera.
 */
class StartReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action !in ACTIONS || !enabled(context)) return
        ContextCompat.startForegroundService(context, Intent(context, LanChatService::class.java))
    }

    companion object {
        private val ACTIONS = setOf(Intent.ACTION_BOOT_COMPLETED, Intent.ACTION_MY_PACKAGE_REPLACED)
        private const val PREF = "autostart"

        /** Activada de forma predeterminada: un mensajero de oficina debe estar disponible. */
        fun enabled(context: Context) = AppPrefs.of(context).getBoolean(PREF, true)

        fun setEnabled(context: Context, on: Boolean) {
            AppPrefs.of(context).edit { putBoolean(PREF, on) }
        }
    }
}

/** Preferencias de la app (lo del núcleo va en su config.json). */
object AppPrefs {
    fun of(context: Context) = context.getSharedPreferences("lanchat", Context.MODE_PRIVATE)
}
