package io.github.aerogu.lanchat

import android.app.Activity
import android.content.Context
import android.graphics.Bitmap
import android.graphics.ImageDecoder
import android.net.Uri
import android.os.Build
import android.util.Log
import androidx.core.content.FileProvider
import java.io.File
import java.util.concurrent.Executors
import kotlin.math.max
import kotlin.math.roundToInt

/**
 * Prepara la imagen elegida para un <input type="file" accept="image/..."> de
 * la página (la foto de perfil). Si su formato no está entre los aceptados
 * (p. ej. HEIC, el de las fotos de algunos teléfonos), Android la decodifica
 * y se entrega como JPEG; el núcleo luego la recorta y reduce como cualquier
 * otra. Las que ya sirven pasan tal cual.
 */
object PhotoPick {
    private const val TAG = "LanChat"

    // Lado mayor de la imagen convertida: sobra para la foto de perfil
    // (thumb.AvatarSide = 256) y evita decodificar una foto de 50 MP completa.
    private const val MAX_SIDE = 1024
    private const val JPEG_QUALITY = 92

    private val worker = Executors.newSingleThreadExecutor()

    /** Llama a [done] en el hilo principal con [uri] o con su versión JPEG. */
    fun prepare(activity: Activity, uri: Uri, accepted: Set<String>, done: (Uri) -> Unit) {
        val type = activity.contentResolver.getType(uri)?.lowercase()
        if (type != null && type in accepted) {
            done(uri)
            return
        }
        worker.execute {
            val out = runCatching { toJpeg(activity, uri) }
                .onFailure { Log.w(TAG, "convirtiendo la imagen ($type) a JPEG", it) }
                .getOrNull()
            // Si no se pudo, va la original: la página muestra el error del núcleo.
            activity.runOnUiThread { done(out ?: uri) }
        }
    }

    /** La imagen como JPEG en la caché, o null si Android no sabe leerla. */
    private fun toJpeg(context: Context, uri: Uri): Uri? {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.P) return null // HEIC requiere Android 9
        // ImageDecoder ya aplica la orientación EXIF.
        val bitmap = ImageDecoder.decodeBitmap(ImageDecoder.createSource(context.contentResolver, uri)) { dec, info, _ ->
            val w = info.size.width
            val h = info.size.height
            val scale = MAX_SIDE.toFloat() / max(w, h)
            if (scale < 1) {
                dec.setTargetSize((w * scale).roundToInt().coerceAtLeast(1), (h * scale).roundToInt().coerceAtLeast(1))
            }
            dec.allocator = ImageDecoder.ALLOCATOR_SOFTWARE // compress() no lee bitmaps de hardware
        }
        val dir = File(context.cacheDir, "photo").apply { mkdirs() }
        val file = File(dir, "foto.jpg")
        try {
            file.outputStream().use { bitmap.compress(Bitmap.CompressFormat.JPEG, JPEG_QUALITY, it) }
        } finally {
            bitmap.recycle()
        }
        return FileProvider.getUriForFile(context, "${context.packageName}.files", file)
    }
}
