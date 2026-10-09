package io.github.aerogu.lanchat

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.net.Uri
import android.text.TextUtils
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import androidx.activity.ComponentActivity
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat

/** La ventana de LanChat: la misma interfaz web del escritorio, en una WebView. */
class MainActivity : ComponentActivity() {
    private lateinit var web: WebView

    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    // <input type="file"> de la página (📎): selector de archivos de Android.
    private var fileCallback: ValueCallback<Array<Uri>>? = null
    private val pickFiles = registerForActivityResult(ActivityResultContracts.GetMultipleContents()) { uris ->
        fileCallback?.onReceiveValue(uris.takeIf { it.isNotEmpty() }?.toTypedArray())
        fileCallback = null
    }

    private val onStarted: (Result<String>) -> Unit = { r ->
        r.onSuccess { web.loadUrl(it) }.onFailure { showError(it) }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        if (applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0) {
            WebView.setWebContentsDebuggingEnabled(true) // chrome://inspect desde la PC
        }
        web = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            webViewClient = Client()
            webChromeClient = ChromeClient()
        }
        // Edge-to-edge: la página no debe quedar bajo las barras ni el teclado.
        val root = FrameLayout(this).apply { addView(web) }
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val bars = insets.getInsets(
                WindowInsetsCompat.Type.systemBars() or
                    WindowInsetsCompat.Type.displayCutout() or
                    WindowInsetsCompat.Type.ime(),
            )
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            WindowInsetsCompat.CONSUMED
        }
        setContentView(root)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        ContextCompat.startForegroundService(this, Intent(this, LanChatService::class.java))
        LanChatService.whenStarted(onStarted)
    }

    override fun onDestroy() {
        LanChatService.cancel(onStarted)
        web.destroy()
        super.onDestroy()
    }

    private fun showError(e: Throwable) {
        val html = "<meta name=viewport content='width=device-width'>" +
            "<h3>${getString(R.string.start_failed)}</h3><p>${TextUtils.htmlEncode(e.message ?: e.toString())}</p>"
        web.loadDataWithBaseURL(null, html, "text/html", "utf-8", null)
    }

    /** La interfaz de LanChat se queda en la WebView; los demás enlaces van al navegador. */
    private inner class Client : WebViewClient() {
        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            if (request.url.host == "127.0.0.1") return false
            runCatching { startActivity(Intent(Intent.ACTION_VIEW, request.url)) }
            return true
        }
    }

    private inner class ChromeClient : WebChromeClient() {
        override fun onShowFileChooser(
            view: WebView,
            callback: ValueCallback<Array<Uri>>,
            params: FileChooserParams,
        ): Boolean {
            // La WebView no vuelve a abrir el selector hasta recibir respuesta.
            fileCallback?.onReceiveValue(null)
            fileCallback = callback
            try {
                pickFiles.launch("*/*")
            } catch (_: ActivityNotFoundException) {
                fileCallback = null
                callback.onReceiveValue(null)
            }
            return true
        }
    }
}
