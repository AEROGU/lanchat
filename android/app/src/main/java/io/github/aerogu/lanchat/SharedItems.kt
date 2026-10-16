package io.github.aerogu.lanchat

import android.content.ContentResolver
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import android.webkit.MimeTypeMap
import android.webkit.WebResourceResponse
import androidx.core.content.IntentCompat
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.util.UUID

/**
 * Lo que otra app comparte con LanChat (Compartir > LanChat): archivos y, a
 * veces, un texto. La página pregunta a quién enviarlo (window.lanchatShare en
 * app.js) y lee cada archivo de /android-share/<token>/<n>: la WebView lo pide
 * y [serve] lo entrega desde la otra app sin pasar por la red. El token cambia
 * en cada "Compartir" y deja de servirse al terminar (shareDone).
 */
class SharedItems private constructor(
    private val token: String,
    private val uris: List<Uri>,
    /** El objeto para window.lanchatShare: {files: [{name, size, url}], text}. */
    val json: String,
) {
    /** Entrega el archivo pedido si la petición es de este envío; null si no. */
    fun serve(context: Context, url: Uri): WebResourceResponse? {
        val segs = url.pathSegments
        if (url.host != "127.0.0.1" || segs.size != 3 || segs[0] != PATH || segs[1] != token) return null
        val uri = segs[2].toIntOrNull()?.let(uris::getOrNull) ?: return missing()
        val input = runCatching { context.contentResolver.openInputStream(uri) }.getOrNull() ?: return missing()
        val mime = context.contentResolver.getType(uri) ?: "application/octet-stream"
        return WebResourceResponse(mime, null, input)
    }

    private fun missing() =
        WebResourceResponse("text/plain", "utf-8", 404, "Not Found", emptyMap(), ByteArrayInputStream(ByteArray(0)))

    companion object {
        private const val PATH = "android-share"

        /** Lee lo compartido; null si el intent no es un "Compartir" o no trae nada útil. */
        fun from(context: Context, intent: Intent): SharedItems? {
            val streams = when (intent.action) {
                Intent.ACTION_SEND ->
                    listOfNotNull(IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java))
                Intent.ACTION_SEND_MULTIPLE ->
                    IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri::class.java).orEmpty()
                else -> return null
            }
            val uris = streams.filter { allowed(context, it) }
            val text = intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString()?.trim().orEmpty()
            if (uris.isEmpty() && text.isEmpty()) return null
            val token = UUID.randomUUID().toString()
            val files = JSONArray()
            uris.forEachIndexed { i, uri ->
                val (name, size) = describe(context, uri, i)
                files.put(JSONObject().put("name", name).put("size", size).put("url", "/$PATH/$token/$i"))
            }
            return SharedItems(token, uris, JSONObject().put("files", files).put("text", text).toString())
        }

        /**
         * Solo contenido de otras apps. Una app maliciosa podría "compartir"
         * file:///data/data/<LanChat>/files/identity.key: LanChat sí puede
         * leerlo y lo enviaría si el usuario no se fija.
         */
        private fun allowed(context: Context, uri: Uri) =
            uri.scheme == ContentResolver.SCHEME_CONTENT &&
                uri.authority?.startsWith(context.packageName) == false

        /** Nombre y tamaño (-1 si no se sabe) como los muestra la otra app. */
        private fun describe(context: Context, uri: Uri, i: Int): Pair<String, Long> {
            var name: String? = null
            var size = -1L
            runCatching {
                context.contentResolver.query(
                    uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null,
                )?.use { c ->
                    if (c.moveToFirst()) {
                        val n = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                        if (n >= 0 && !c.isNull(n)) name = c.getString(n)
                        val s = c.getColumnIndex(OpenableColumns.SIZE)
                        if (s >= 0 && !c.isNull(s)) size = c.getLong(s)
                    }
                }
            }
            if (name.isNullOrBlank()) {
                val ext = context.contentResolver.getType(uri)
                    ?.let { MimeTypeMap.getSingleton().getExtensionFromMimeType(it) }
                name = "archivo-${i + 1}" + (ext?.let { ".$it" } ?: "")
            }
            return Pair(name!!, size)
        }
    }
}
