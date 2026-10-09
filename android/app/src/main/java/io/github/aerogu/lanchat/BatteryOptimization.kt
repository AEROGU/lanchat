package io.github.aerogu.lanchat

import android.annotation.SuppressLint
import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.os.PowerManager
import android.provider.Settings
import androidx.core.net.toUri

/**
 * Que Android no pause LanChat para ahorrar batería: si lo hace, los mensajes
 * llegan con minutos de retraso. Algunos fabricantes (OPPO, Xiaomi, Huawei…)
 * tienen además sus propios ajustes en la información de la app.
 */
object BatteryOptimization {
    fun restricted(context: Context): Boolean =
        !context.getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(context.packageName)

    /** Diálogo del sistema "¿Permitir que la app se ejecute siempre en segundo plano?". */
    @SuppressLint("BatteryLife") // un chat necesita recibir los mensajes al momento
    fun request(activity: Activity) {
        val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, packageUri(activity))
        try {
            activity.startActivity(intent)
        } catch (_: ActivityNotFoundException) {
            openAppSettings(activity)
        }
    }

    /** Información de la app, donde cada fabricante pone sus ajustes de batería. */
    fun openAppSettings(activity: Activity) {
        activity.startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, packageUri(activity)))
    }

    private fun packageUri(context: Context) = "package:${context.packageName}".toUri()
}
