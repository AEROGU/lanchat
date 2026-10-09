# LanChat para Android: guía y pendientes

La app de Android usa el **mismo núcleo en Go** que Windows (descubrimiento,
cifrado, mensajes, salas, archivos, SQLite) y la **misma interfaz web**. Solo
cambia la "cáscara": en lugar de Edge, la bandeja y las notificaciones de
Windows, una app pequeña en Kotlin.

```
App Android (Kotlin, carpeta android/)
├── LanChatService (servicio en primer plano)
│     └── Mobile.start(...) ── núcleo Go (lanchat.aar, gomobile)
│           descubrimiento UDP :50000 · TLS :50001 · SQLite · salas · archivos
│           interfaz web en http://127.0.0.1:<puerto>
├── MainActivity ── WebView que carga esa dirección
└── Host (Kotlin) ── notificaciones, contador de no leídos
```

## Ya hecho (en la PC de la oficina)

- El núcleo compila para Android (arm64, arm, amd64). `go tool mage check`
  lo comprueba en cada commit con el target `portable`.
- `internal/ui/gui.go` (bandeja y ventana de Edge) solo se compila en
  escritorio. La decisión de cuándo y con qué texto notificar quedó en
  `Server.Notification`, que comparten Windows y Android.
- Paquete [`mobile/`](../mobile/mobile.go): la API para Kotlin, con pruebas
  que corren en Windows.
  - `Mobile.start(dataDir, deviceName, downloadDir, host): String` arranca
    LanChat y devuelve la dirección para la WebView.
  - `Mobile.stop()`, `Mobile.running()`, `Mobile.version()`.
  - `Host` (lo implementa Kotlin): `notify(title, body)` y
    `unreadChanged(total)`.
- Target `go tool mage android`: genera `android/app/libs/lanchat.aar`
  (~17 MB). Probado en la laptop con NDK 30 y el JDK 25 de Android Studio.
- `.gitignore` ya excluye lo generado por Android Studio, el `.aar` y las
  claves de firma.

## Pendientes

### 1. Preparar la laptop (hecho)

Sirve también para preparar otra PC:

- [x] Android Studio con, en *Settings > Languages & Frameworks > Android SDK*:
  - SDK Platform reciente.
  - *SDK Tools*: Android SDK Build-Tools, **NDK (Side by side)** y Android
    SDK Command-line Tools. CMake no hace falta (gomobile usa el clang del
    NDK y la app no tiene código C++ propio).
- [x] Variables de entorno de usuario (ajusta las rutas):
  - `ANDROID_HOME` = `C:\Users\<usuario>\AppData\Local\Android\Sdk`
  - `ANDROID_NDK_HOME` = `%ANDROID_HOME%\ndk\<versión>`
  - `JAVA_HOME` = el JDK que trae Android Studio (`...\Android Studio\jbr`)
  - `%JAVA_HOME%\bin` en el `Path`: gomobile llama a `javac` directamente.
- [x] Go en la versión de `go.mod`, y gomobile:

  ```bash
  go install golang.org/x/mobile/cmd/gomobile@latest
  gomobile init
  ```

- [x] `go tool mage android` → crea `android/app/libs/lanchat.aar`.
  `gobind` ya quedó como herramienta en `go.mod` (`go get -tool
  golang.org/x/mobile/cmd/gobind`), igual que mage.
  - Si no encuentra el NDK, revisar `ANDROID_NDK_HOME`.
  - Si dice `"javac": executable file not found`, falta `%JAVA_HOME%\bin`
    en el `Path` (abrir una terminal nueva después de cambiarlo).

### 2. Prueba de red, antes que todo lo demás

El mayor riesgo no es el código, es la red: en muchas oficinas el Wi-Fi aísla
a los clientes o pone los teléfonos en otra red.

- [ ] App mínima: servicio + `Mobile.start` + WebView (puntos 3 y 4 sin
  pulir). Con LanChat abierto en una PC de la oficina, comprobar que:
  - [ ] el teléfono aparece en la PC y la PC en el teléfono;
  - [ ] llegan mensajes en ambos sentidos.
- [ ] Si no se ven: revisar `lanchat.log` (en `filesDir`), probar con la IP
  de la PC en Ajustes > "Equipos de otras subredes", y preguntar si el Wi-Fi
  tiene aislamiento de clientes o es una red aparte.
- [ ] Verificar en el log si Go pudo leer las interfaces de red: en
  Android 11+ `net.Interfaces()` puede fallar por permisos. El descubrimiento
  sigue funcionando con `255.255.255.255` y unicast, pero sin el broadcast
  dirigido por red. Si hiciera falta, pasar las redes desde Kotlin
  (`ConnectivityManager` / `LinkProperties`) con una opción nueva en
  `discovery.Config`.

### 3. Proyecto Android (carpeta `android/`)

- [ ] Android Studio: *New Project > Empty Views Activity*, Kotlin, nombre
  LanChat, paquete `io.github.aerogu.lanchat`, guardado en `<repo>/android`,
  mínimo **API 24** (igual que `androidAPI` en `magefiles/android.go`).
- [ ] `app/build.gradle.kts`: `implementation(files("libs/lanchat.aar"))`.
  La clase generada es `io.github.aerogu.lanchat.mobile.Mobile`.
- [ ] `AndroidManifest.xml`, permisos:
  - `INTERNET`, `ACCESS_NETWORK_STATE`, `ACCESS_WIFI_STATE`
  - `CHANGE_WIFI_MULTICAST_STATE` (sin él Android descarta los broadcasts)
  - `POST_NOTIFICATIONS` (Android 13+, pedirlo en tiempo de ejecución)
  - `FOREGROUND_SERVICE` y el del tipo de servicio elegido (ver abajo)
  - Opcionales: `RECEIVE_BOOT_COMPLETED` (arrancar al encender) y
    `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`
- [ ] Tipo del servicio en primer plano (obligatorio desde Android 14).
  `dataSync` tiene límite de 6 h diarias en Android 15, así que no sirve.
  Para la APK de la oficina, `specialUse` con su explicación en el manifest.
  Revisar la documentación vigente antes de publicar en Google Play.
- [ ] La WebView carga `http://127.0.0.1`: permitir texto plano solo para
  esa dirección con `network_security_config.xml`:

  ```xml
  <network-security-config>
    <domain-config cleartextTrafficPermitted="true">
      <domain includeSubdomains="false">127.0.0.1</domain>
    </domain-config>
  </network-security-config>
  ```

### 4. Código Kotlin

- [ ] **LanChatService** (servicio en primer plano):
  - Tomar un `WifiManager.MulticastLock` (y soltarlo en `onDestroy`).
  - `startForeground` con una notificación fija "LanChat activo".
  - En un hilo aparte: `Mobile.start(filesDir.absolutePath, nombre,
    descargas, host)`. Guardar la URL para la Activity.
  - Nombre del equipo: `Settings.Global.DEVICE_NAME`, o `Build.MODEL` si
    no hay.
  - Descargas: `getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS)` (no
    pide permisos). Más adelante copiar lo recibido a Descargas con
    `MediaStore`.
  - `onDestroy`: `Mobile.stop()`.
- [ ] **Host**:
  - `notify` → `NotificationCompat` en un canal "Mensajes"; al tocarla abre
    la app.
  - `unreadChanged` → número en la notificación fija o en el ícono.
- [ ] **MainActivity**:
  - WebView con JavaScript y `domStorageEnabled`; carga la URL del servicio.
  - El botón Atrás vuelve de la conversación a la lista (la página ya tiene
    el botón ‹ para pantallas angostas).
  - `WebChromeClient.onShowFileChooser` para `<input type="file">` (punto 5).
- [ ] Pedir que se ignore la optimización de batería; explicar por qué (si
  no, algunos fabricantes pausan la app y los mensajes llegan con retraso).

### 5. Ajustes a la interfaz web y al núcleo

Hoy algunas acciones usan funciones de Windows; en Android devuelven error.

- [ ] **📎 Enviar archivos**: usa el selector de Windows (`/api/files/pick`).
  Agregar a `/api/state` un indicador (p. ej. `nativeDialogs`). Si es
  `false`, el 📎 abre un `<input type="file" multiple>` y sube por
  `/api/files/upload`, que ya existe para arrastrar y soltar.
- [ ] **📁 Enviar carpeta**: ocultarlo en Android; la WebView no permite
  elegir carpetas.
- [ ] **Abrir archivo recibido / Mostrar en carpeta / Abrir carpeta / enlace
  del proyecto**: hoy llaman a `openPath` (ShellExecute). Agregar a `Host`
  `openFile(path)` (Intent `ACTION_VIEW` con `FileProvider`) y
  `openUrl(url)`, y que `Server` use un gancho en lugar de la función del
  paquete. En Android "Mostrar en carpeta" puede omitirse.
- [ ] **Descargar mis datos**: en la WebView las descargas necesitan un
  `DownloadListener`; o exponer la copia por `Host`.
- [ ] **Ajustes**: la sección Sistema ya se oculta fuera de Windows. Ocultar
  o hacer de solo lectura la carpeta de descargas.
- [ ] **Táctil**: botones más grandes. Los botones del encabezado de la
  conversación (Identidad, Grupo, Renombrar, Borrar) no caben en un
  teléfono: pasarlos a un menú "⋯".
- [ ] **Ausente automático**: en Android no hay tiempo de inactividad;
  opcionalmente usar pantalla apagada = ausente (`app.Options.IdleTime`).

### 6. Distribución

- [ ] Clave de firma (`.jks`): crearla una vez, **guardar copia segura y
  nunca subirla al repositorio**. Sin ella no se pueden publicar
  actualizaciones de la app.
- [ ] Publicar la APK firmada en GitHub Releases junto al zip de Windows.
- [ ] GitHub Actions: un job en Ubuntu con Java, el SDK y el NDK, gomobile,
  `go tool mage android` y `./gradlew assembleRelease`, con la clave en
  *secrets*.
- [ ] Más adelante, si se quiere: Google Play (cuenta de 25 USD, revisión
  del servicio en primer plano).
- [ ] Documentar en README y LEEME la instalación en Android ("instalar
  apps desconocidas").

## Notas

- Cada teléfono tiene su propia identidad TLS (en `filesDir`). Si se
  desinstala la app, las PCs verán "la identidad cambió" la próxima vez.
- El teléfono usa los mismos puertos que Windows (UDP 50000 y TCP 50001);
  en Android no hace falta permiso especial para puertos mayores a 1024.
- Los mensajes no se pierden si Android pausa la app: el remitente los
  reintenta y llegan al volver.
