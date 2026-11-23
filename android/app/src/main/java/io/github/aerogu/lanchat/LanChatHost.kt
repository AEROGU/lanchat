package io.github.aerogu.lanchat

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.webkit.MimeTypeMap
import androidx.core.content.FileProvider
import androidx.core.net.toUri
import io.github.aerogu.lanchat.mobile.Host
import java.io.File

/**
 * Lo que el núcleo en Go pide a Android. Go lo llama desde sus propios hilos;
 * una excepción le llega a Go como error y la página la muestra.
 */
class LanChatHost(private val context: Context, private val screen: ScreenIdle) : Host {
    private val main = Handler(Looper.getMainLooper())

    override fun notify(title: String, body: String, chat: String) =
        Notifications.message(context, title, body, chat)

    override fun unreadChanged(total: Long) = Notifications.updateService(context, total.toInt())

    override fun idleSeconds() = screen.idleSeconds()

    override fun openFile(path: String) {
        val file = File(path)
        val uri = try {
            FileProvider.getUriForFile(context, "${context.packageName}.files", file)
        } catch (_: IllegalArgumentException) {
            throw Exception(text(R.string.file_outside_downloads, file.name))
        }
        val type = MimeTypeMap.getSingleton().getMimeTypeFromExtension(file.extension.lowercase()) ?: "*/*"
        val intent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, type)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        start(intent, text(R.string.no_app_to_open, file.name))
    }

    override fun notifyUpdate(title: String, body: String) = Notifications.update(context, title, body)

    /**
     * Abre el instalador de Android con el APK de una versión nueva (ya
     * verificado). Android solo lo acepta firmado con la misma clave que la
     * app instalada, y siempre pide confirmarlo. La primera vez hace falta
     * permitir "Instalar apps desconocidas" para LanChat.
     */
    override fun installUpdate(path: String) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !context.packageManager.canRequestPackageInstalls()) {
            start(
                Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, "package:${context.packageName}".toUri()),
                text(R.string.no_unknown_sources_setting),
            )
            throw Exception(text(R.string.allow_unknown_sources))
        }
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.files", File(path))
        val intent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, "application/vnd.android.package-archive")
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        start(intent, text(R.string.no_installer))
    }

    override fun openURL(url: String) {
        start(Intent(Intent.ACTION_VIEW, url.toUri()), text(R.string.no_browser, url))
    }

    /** El núcleo cambió el idioma en Ajustes de LanChat ("" = el del sistema). */
    override fun languageChanged(language: String) {
        main.post { Language.set(context, language) }
    }

    /** Un texto de Android en el idioma de LanChat. */
    private fun text(id: Int, vararg args: Any): String = Language.wrap(context).getString(id, *args)

    private fun start(intent: Intent, missing: String) {
        try {
            context.startActivity(intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        } catch (_: ActivityNotFoundException) {
            throw Exception(missing)
        }
    }
}
