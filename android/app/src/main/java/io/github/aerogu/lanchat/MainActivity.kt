package io.github.aerogu.lanchat

import android.Manifest
import android.annotation.SuppressLint
import android.app.AlertDialog
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.core.content.edit
import androidx.core.text.htmlEncode
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import org.json.JSONObject

/** La ventana de LanChat: la misma interfaz web del escritorio, en una WebView. */
class MainActivity : ComponentActivity() {
    private lateinit var web: WebView

    // Primero los permisos (notificaciones y red local) y después lo de la
    // batería. Sin la red local (Android 17+) no se ve a nadie: si se niega,
    // se explica y se ofrece abrir los ajustes de la app.
    private val askPermissions =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { granted ->
            if (granted[LOCAL_NETWORK] == false) explainLocalNetwork() else askBatteryOnce()
        }

    // <input type="file"> de la página (📎): selector de archivos de Android.
    private var fileCallback: ValueCallback<Array<Uri>>? = null
    private val pickFiles = registerForActivityResult(ActivityResultContracts.GetMultipleContents()) { uris ->
        fileCallback?.onReceiveValue(uris.takeIf { it.isNotEmpty() }?.toTypedArray())
        fileCallback = null
    }

    // <input type="file" accept="image/..."> (la foto de perfil): una sola
    // imagen, convertida a JPEG si su formato no está en accept (PhotoPick).
    private var imageTypes: Set<String> = emptySet()
    private val pickImage = registerForActivityResult(ActivityResultContracts.GetContent()) { uri ->
        val callback = fileCallback ?: return@registerForActivityResult
        fileCallback = null
        if (uri == null) {
            callback.onReceiveValue(null)
        } else {
            PhotoPick.prepare(this, uri, imageTypes) { callback.onReceiveValue(arrayOf(it)) }
        }
    }

    private val onStarted: (Result<String>) -> Unit = { r ->
        r.onSuccess { web.loadUrl(it) }.onFailure { showError(it) }
    }

    // Al detener LanChat (aviso fijo o Ajustes) se cierra también la ventana.
    private val onStopped: () -> Unit = { finishAndRemoveTask() }

    // Antes de Android 13, los textos de la ventana siguen el idioma elegido en
    // LanChat (después lo aplica el sistema; ver Language).
    override fun attachBaseContext(newBase: Context) = super.attachBaseContext(Language.wrap(newBase))

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        if (applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0) {
            WebView.setWebContentsDebuggingEnabled(true) // chrome://inspect desde la PC
        }
        web = WebView(this).apply {
            @SuppressLint("SetJavaScriptEnabled") // solo carga la interfaz de 127.0.0.1 (Client)
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

        val missing = buildList {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) add(Manifest.permission.POST_NOTIFICATIONS)
            if (Build.VERSION.SDK_INT >= ANDROID_17) add(LOCAL_NETWORK)
        }.filter { ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED }
        if (missing.isNotEmpty()) askPermissions.launch(missing.toTypedArray()) else askBatteryOnce()
        ContextCompat.startForegroundService(this, Intent(this, LanChatService::class.java))
        LanChatService.whenStarted(onStarted)
        LanChatService.whenStopped(onStopped)
        pendingChat = intent.getStringExtra(EXTRA_CHAT) // se abre al cargar la página
        receiveShare(intent)
    }

    // Tocar un aviso o compartir algo con la app abierta (launchMode
    // singleTask) llega aquí.
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        intent.getStringExtra(EXTRA_CHAT)?.let(::openChat)
        receiveShare(intent)
    }

    // Lo compartido desde otra app. La WebView pide sus archivos desde otro
    // hilo (Client.shouldInterceptRequest).
    @Volatile private var share: SharedItems? = null
    private var shareShown = false
    private var pageLoaded = false

    /** "Compartir > LanChat": la página pregunta a quién enviarlo. */
    private fun receiveShare(intent: Intent) {
        share = SharedItems.from(this, intent) ?: return
        shareShown = false
        if (pageLoaded) showShare()
    }

    private fun showShare() {
        val s = share ?: return
        if (shareShown) return
        shareShown = true
        web.evaluateJavascript("window.lanchatShare?.(${s.json})", null)
    }

    // Conversación de un aviso tocado con la app cerrada: se abre en onPageFinished.
    private var pendingChat: String? = null

    /** Abre la conversación en la página (ver window.lanchatOpen en app.js). */
    private fun openChat(chat: String) {
        web.evaluateJavascript("window.lanchatOpen?.(${JSONObject.quote(chat)})", null)
    }

    /**
     * La primera vez, explica por qué y pide que Android no pause LanChat. No
     * vuelve a preguntar: después se cambia en Ajustes > Segundo plano.
     */
    private fun askBatteryOnce() {
        val prefs = AppPrefs.of(this)
        if (prefs.getBoolean(PREF_BATTERY_ASKED, false) || !BatteryOptimization.restricted(this)) return
        prefs.edit { putBoolean(PREF_BATTERY_ASKED, true) }
        AlertDialog.Builder(this)
            .setTitle(R.string.battery_title)
            .setMessage(R.string.battery_message)
            .setPositiveButton(R.string.battery_continue) { _, _ -> BatteryOptimization.request(this) }
            .setNegativeButton(R.string.battery_later, null)
            .show()
    }

    /**
     * Se negó el permiso de red local: sin él Android bloquea el descubrimiento
     * y los mensajes. Se vuelve a explicar en cada arranque hasta que se dé,
     * porque sin él LanChat no sirve.
     */
    private fun explainLocalNetwork() {
        AlertDialog.Builder(this)
            .setTitle(R.string.local_network_title)
            .setMessage(R.string.local_network_message)
            .setPositiveButton(R.string.local_network_settings) { _, _ -> BatteryOptimization.openAppSettings(this) }
            .setNegativeButton(R.string.battery_later, null)
            .show()
    }

    override fun onResume() {
        super.onResume()
        web.evaluateJavascript("window.lanchatResume?.()", null)
    }

    override fun onDestroy() {
        LanChatService.cancel(onStarted)
        LanChatService.cancelStopped(onStopped)
        web.destroy()
        super.onDestroy()
    }

    private fun showError(e: Throwable) {
        val html = "<meta name=viewport content='width=device-width'>" +
            "<h3>${getString(R.string.start_failed)}</h3><p>${(e.message ?: e.toString()).htmlEncode()}</p>"
        web.loadDataWithBaseURL(null, html, "text/html", "utf-8", null)
    }

    /** Lo que la página puede pedir a Android (sección del teléfono en Ajustes). */
    private inner class Bridge {
        @JavascriptInterface
        fun stop() = runOnUiThread { LanChatService.stop(this@MainActivity) }

        @JavascriptInterface
        fun autostart() = StartReceiver.enabled(this@MainActivity)

        @JavascriptInterface
        fun setAutostart(on: Boolean) = StartReceiver.setEnabled(this@MainActivity, on)

        @JavascriptInterface
        fun backgroundRestricted() = BatteryOptimization.restricted(this@MainActivity)

        @JavascriptInterface
        fun allowBackground() = runOnUiThread { BatteryOptimization.request(this@MainActivity) }

        @JavascriptInterface
        fun openAppSettings() = runOnUiThread { BatteryOptimization.openAppSettings(this@MainActivity) }

        /** Se envió o canceló lo compartido: sus archivos ya no se sirven. */
        @JavascriptInterface
        fun shareDone() {
            share = null
        }

        /** Se abrió una conversación: su aviso ya no hace falta. */
        @JavascriptInterface
        fun chatOpened(chat: String) = Notifications.clearMessage(this@MainActivity, chat)
    }

    /** La interfaz de LanChat se queda en la WebView; los demás enlaces van al navegador. */
    private inner class Client : WebViewClient() {
        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            if (request.url.host == "127.0.0.1") return false
            runCatching { startActivity(Intent(Intent.ACTION_VIEW, request.url)) }
            return true
        }

        override fun onPageFinished(view: WebView, url: String) {
            pageLoaded = url.startsWith("http://127.0.0.1")
            if (pageLoaded) {
                pendingChat?.let(::openChat)
                showShare()
            }
            pendingChat = null
        }

        // Los archivos compartidos (SharedItems); lo demás va al servidor de LanChat.
        override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? =
            share?.serve(this@MainActivity, request.url)
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
            // accept llega como lista (o separado por comas, según la WebView).
            val types = params.acceptTypes.flatMap { it.split(',') }
                .map { it.trim().lowercase() }.filter { it.isNotEmpty() }
            try {
                if (types.isNotEmpty() && types.all { it.startsWith("image/") }) {
                    imageTypes = types.toSet()
                    pickImage.launch("image/*")
                } else {
                    pickFiles.launch("*/*")
                }
            } catch (_: ActivityNotFoundException) {
                fileCallback = null
                callback.onReceiveValue(null)
            }
            return true
        }
    }

    companion object {
        /** Conversación que abrir (aviso tocado); la pone Notifications. */
        const val EXTRA_CHAT = "io.github.aerogu.lanchat.CHAT"
    }
}

private const val PREF_BATTERY_ASKED = "battery_asked"

/** Android 17 (API 37): desde ahí la red local requiere [LOCAL_NETWORK]. */
private const val ANDROID_17 = 37
private const val LOCAL_NETWORK = Manifest.permission.ACCESS_LOCAL_NETWORK
