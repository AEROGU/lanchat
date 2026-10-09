package io.github.aerogu.lanchat

import android.Manifest
import android.app.AlertDialog
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.text.TextUtils
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.core.content.edit
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat

/** La ventana de LanChat: la misma interfaz web del escritorio, en una WebView. */
class MainActivity : ComponentActivity() {
    private lateinit var web: WebView

    // Primero el permiso de notificaciones y después lo de la batería.
    private val askNotifications =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { askBatteryOnce() }

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
            // Solo carga la interfaz de 127.0.0.1 (ver Client): nadie más lo ve.
            addJavascriptInterface(Bridge(), "LanChatAndroid")
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

        // Atrás cierra primero lo que la página tenga abierto (diálogo, menú,
        // conversación); si no hay nada, la app pasa a segundo plano sin
        // cerrarse, y LanChat sigue recibiendo mensajes.
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                web.evaluateJavascript("window.lanchatBack?.() === true") { handled ->
                    if (handled != "true") moveTaskToBack(true)
                }
            }
        })

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        } else {
            askBatteryOnce()
        }
        ContextCompat.startForegroundService(this, Intent(this, LanChatService::class.java))
        LanChatService.whenStarted(onStarted)
    }

    /**
     * La primera vez, explica por qué y pide que Android no pause LanChat. No
     * vuelve a preguntar: después se cambia en Ajustes > Segundo plano.
     */
    private fun askBatteryOnce() {
        val prefs = getSharedPreferences(PREFS, MODE_PRIVATE)
        if (prefs.getBoolean(PREF_BATTERY_ASKED, false) || !BatteryOptimization.restricted(this)) return
        prefs.edit { putBoolean(PREF_BATTERY_ASKED, true) }
        AlertDialog.Builder(this)
            .setTitle(R.string.battery_title)
            .setMessage(R.string.battery_message)
            .setPositiveButton(R.string.battery_continue) { _, _ -> BatteryOptimization.request(this) }
            .setNegativeButton(R.string.battery_later, null)
            .show()
    }

    override fun onResume() {
        super.onResume()
        web.evaluateJavascript("window.lanchatResume?.()", null)
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

    /** Lo que la página puede pedir a Android: Ajustes > Segundo plano. */
    private inner class Bridge {
        @JavascriptInterface
        fun backgroundRestricted() = BatteryOptimization.restricted(this@MainActivity)

        @JavascriptInterface
        fun allowBackground() = runOnUiThread { BatteryOptimization.request(this@MainActivity) }

        @JavascriptInterface
        fun openAppSettings() = runOnUiThread { BatteryOptimization.openAppSettings(this@MainActivity) }
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

private const val PREFS = "lanchat"
private const val PREF_BATTERY_ASKED = "battery_asked"
