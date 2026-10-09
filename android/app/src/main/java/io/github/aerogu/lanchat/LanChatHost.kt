package io.github.aerogu.lanchat

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.webkit.MimeTypeMap
import androidx.core.content.FileProvider
import io.github.aerogu.lanchat.mobile.Host
import java.io.File

/**
 * Lo que el núcleo en Go pide a Android. Go lo llama desde sus propios hilos;
 * una excepción le llega a Go como error y la página la muestra.
 */
class LanChatHost(private val context: Context) : Host {
    override fun notify(title: String, body: String) = Notifications.message(context, title, body)

    override fun unreadChanged(total: Long) = Notifications.updateService(context, total.toInt())

    override fun openFile(path: String) {
        val file = File(path)
        val uri = try {
            FileProvider.getUriForFile(context, "${context.packageName}.files", file)
        } catch (_: IllegalArgumentException) {
            throw Exception("«${file.name}» está fuera de la carpeta de archivos recibidos")
        }
        val type = MimeTypeMap.getSingleton().getMimeTypeFromExtension(file.extension.lowercase()) ?: "*/*"
        val intent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, type)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        start(intent, "No hay ninguna app para abrir «${file.name}»")
    }

    override fun openURL(url: String) {
        start(Intent(Intent.ACTION_VIEW, Uri.parse(url)), "No hay un navegador para abrir $url")
    }

    private fun start(intent: Intent, missing: String) {
        try {
            context.startActivity(intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        } catch (_: ActivityNotFoundException) {
            throw Exception(missing)
        }
    }
}
