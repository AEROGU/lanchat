package io.github.aerogu.lanchat

import android.app.LocaleManager
import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import android.os.Build
import android.os.LocaleList
import androidx.core.content.edit

/**
 * Idioma de LanChat en Android. Manda el elegido en Ajustes de LanChat (lo
 * guarda el núcleo en Go); desde Android 13 se refleja además en el idioma por
 * app del sistema (Ajustes > Apps > LanChat > Idioma), y lo que se elija ahí
 * llega al núcleo al arrancar. Los textos de Android (avisos, diálogos) siguen
 * ese idioma; los de la página los da el núcleo.
 */
object Language {
    private const val PREFS = "language"
    private const val KEY = "chosen"

    /** Los idiomas del sistema en orden de preferencia ("es-MX,en-US"), para el núcleo. */
    fun system(): String = Resources.getSystem().configuration.locales.toLanguageTags()

    /** El idioma elegido para LanChat ("" = el del sistema). */
    fun chosen(context: Context): String =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            context.getSystemService(LocaleManager::class.java).applicationLocales.toLanguageTags()
        } else {
            prefs(context).getString(KEY, "") ?: ""
        }

    /**
     * Lo llama el núcleo cuando se cambia el idioma en Ajustes de LanChat
     * ("" = el del sistema). Los avisos ya creados se traducen al momento.
     */
    fun set(context: Context, tag: String) {
        prefs(context).edit { putString(KEY, tag) }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            context.getSystemService(LocaleManager::class.java).applicationLocales =
                if (tag.isEmpty()) LocaleList.getEmptyLocaleList() else LocaleList.forLanguageTags(tag)
        }
        Notifications.createChannels(context)
    }

    /**
     * [context] con el idioma elegido, para leer sus textos. Desde Android 13
     * el sistema ya lo aplica a toda la app.
     */
    fun wrap(context: Context): Context {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) return context
        val tag = prefs(context).getString(KEY, "") ?: ""
        if (tag.isEmpty()) return context
        val config = Configuration(context.resources.configuration)
        config.setLocales(LocaleList.forLanguageTags(tag))
        return context.createConfigurationContext(config)
    }

    private fun prefs(context: Context) =
        context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
}
