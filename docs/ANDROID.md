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

- [x] App mínima: servicio + `Mobile.start` + WebView (puntos 3 y 4 sin
  pulir).
- [x] **En un teléfono real** (el emulador no sirve, ver abajo). Probado en
  casa con un OPPO Reno11 (Android 16, arm64) y la laptop con Windows:
  - [x] el núcleo arranca (si falla, la app muestra el error; ver también
    `adb logcat` y `lanchat.log`);
  - [x] el teléfono aparece en la PC y la PC en el teléfono, también con el
    teléfono detrás de un repetidor Wi-Fi;
  - [x] llegan mensajes en ambos sentidos, con confirmación de lectura; el
    cambio de IP del teléfono (al pasar del repetidor al módem) no afectó.
  - [x] Notificación de mensaje nuevo con la app en segundo plano, y el
    aviso fijo cuenta los no leídos.
  - [ ] Repetir en la oficina, con su Wi-Fi.
- Si la PC no acepta conexiones (el teléfono la ve pero los mensajes quedan
  "pendiente"), es el Firewall de Windows: hay que aceptar su aviso la
  primera vez que se abre LanChat. La regla queda solo para el tipo de red
  de ese momento (Pública o Privada).

  Para instalarla:

  ```bash
  go tool mage apk
  ```

  deja `dist/LanChat-<versión>-debug.apk` (núcleo + app; Gradle toma la
  versión de git, como el .exe; `go tool mage apkRelease` deja el firmado).
  Se copia al teléfono (cable, correo, etc.) y se abre: Android pide permitir
  "instalar apps desconocidas" a la app con que se abra. Va firmado con la
  clave de depuración: sirve para pruebas, pero la versión con clave propia
  (punto 6) no podrá instalarse encima; habrá que desinstalar, y las PCs
  verán "la identidad cambió".

  Para desarrollar, con *Depuración USB* activada y el teléfono conectado:
  `go tool mage android` y luego, en `android/`, `./gradlew installDebug`
  (o *Run* en Android Studio).
- [ ] Si no se ven: revisar `lanchat.log` (en `filesDir`), probar con la IP
  de la PC en Ajustes > "Equipos de otras subredes", y preguntar si el Wi-Fi
  tiene aislamiento de clientes o es una red aparte.
- [x] En Android 16 Go no puede leer las interfaces de red
  (`net.Interfaces` y `net.InterfaceAddrs` fallan por permisos de netlink):
  el log avisaba por error "otro equipo usa el mismo ID" con la IP del
  propio teléfono y no se enviaba el broadcast dirigido (p. ej.
  192.168.1.255). Resuelto: `LocalNetworks` (Kotlin) toma las redes Wi-Fi y
  Ethernet de `ConnectivityManager` y las pasa con `Mobile.setNetworks`; la
  red activa se entrega antes de `Mobile.start` y los cambios después (el
  núcleo vuelve a saludar). En el núcleo es `discovery.Config.LocalNets`.
  `lanchat.log` muestra las redes al arrancar (`redes=[...]`) y al cambiar
  ("redes locales").
  - [x] Probado el cambio de red en vivo (del módem al repetidor y al revés
    con LanChat abierto).

#### El emulador no sirve para probar el núcleo

Probado con el emulador x86_64 de Android Studio (API 37):

- **ABI x86_64**: el proceso muere con `SIGSYS` (seccomp, syscall 6 =
  `lstat`). `modernc.org/sqlite` usa una libc traducida de musl, que en
  x86_64 llama a syscalls antiguas (`lstat`, `access`, …) que Android
  prohíbe a las apps. En arm64 esas syscalls no existen y musl usa las
  `*at` (`newfstatat`), que sí están permitidas.
- **ABI arm64 en el emulador** (`adb install --abi arm64-v8a`): el
  traductor ARM del emulador (Berberis) no implementa `mrs MIDR_EL1`, que el
  runtime de Go lee al arrancar → `SIGILL`. En un teléfono real el kernel sí
  la atiende.

En un teléfono real (arm64) funciona. Por eso:

- [x] Sin `android/amd64` en `androidTargets` (no funciona; el APK baja
  ~14 MB). `android/arm` (32 bits) sigue, con el mismo riesgo que x86_64
  (`lstat64`), sin probar: hoy casi no hay teléfonos solo de 32 bits.
- [ ] Si hiciera falta x86_64 o 32 bits: en Android usar un SQLite con cgo
  (gomobile ya compila con el NDK), p. ej. `mattn/go-sqlite3` con etiqueta
  de compilación, y dejar `modernc.org/sqlite` en escritorio.

### 3. Proyecto Android (carpeta `android/`) (hecho)

- [x] Proyecto Gradle a mano (equivale a *Empty Views Activity*): AGP 9.4.1
  con Kotlin integrado (sin plugin `kotlin-android`), Gradle 9.6.1 (el
  wrapper verifica el SHA-256), `compileSdk`/`targetSdk` 37, `minSdk` 24
  (igual que `androidAPI` en `magefiles/android.go`). Dependencias:
  `lanchat.aar`, `activity-ktx` y `core-ktx`.
- [x] `AndroidManifest.xml`: `INTERNET`, `ACCESS_NETWORK_STATE`,
  `ACCESS_WIFI_STATE`, `CHANGE_WIFI_MULTICAST_STATE`, `POST_NOTIFICATIONS`,
  `FOREGROUND_SERVICE` y `FOREGROUND_SERVICE_SPECIAL_USE`.
  - `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` (punto 4). Google Play solo lo
    acepta en ciertos tipos de app: revisarlo si algún día se publica ahí.
  - `RECEIVE_BOOT_COMPLETED`: `StartReceiver` arranca LanChat al encender
    el teléfono y tras actualizar la app (`MY_PACKAGE_REPLACED`), si está
    activado "Iniciar LanChat al encender el teléfono" (Ajustes; activado de
    forma predeterminada). Probado tras actualizar; falta probar reiniciando.
  - `allowBackup="false"`: la identidad TLS no debe pasar a otro teléfono.
- [x] Servicio `specialUse` con su explicación (`dataSync` tiene límite de
  6 h diarias desde Android 15). Revisar la documentación vigente antes de
  publicar en Google Play.
- [x] `network_security_config.xml`: texto plano solo hacia `127.0.0.1`.
- [x] Ícono: el globo de `internal/icon` como ícono adaptable (vector,
  también monocromo) y de notificación; PNG generados con `icon.Draw` para
  Android 7 (sin íconos adaptables).
- El aviso `Unable to strip ... libgojni.so` al compilar es normal: AGP
  busca el NDK 28 y gomobile ya quita los símbolos (`-s -w`).

### 4. Código Kotlin

Versión mínima hecha para la prueba de red; falta pulir:

- [x] **LanChatService**: servicio en primer plano con aviso fijo,
  `MulticastLock`, `Mobile.start`/`Mobile.stop` en un hilo propio (en orden),
  nombre del equipo (`Settings.Global.DEVICE_NAME` o `Build.MODEL`) y
  descargas en `getExternalFilesDir(DIRECTORY_DOWNLOADS)`.
  - [x] Lo recibido va directo a **Descargas/LanChat** (Android 11+): una
    app puede crear archivos ahí sin permisos, Android los indexa (explorador
    de archivos, galería) y no se borran al desinstalar. No se copia nada:
    el núcleo escribe el `.part` ahí mismo y lo renombra al terminar. En
    Android 7 a 10, o si no se puede crear la carpeta, se usa la de la app
    (`getExternalFilesDir`). `FileProvider` admite las dos.
    - Ojo: tras reinstalar la app, los archivos anteriores de esa carpeta ya
      no son "suyos": no puede abrirlos desde LanChat, y si llega uno con el
      mismo nombre, el renombrado final podría fallar. Se abren desde el
      explorador de archivos.
    - [x] Probado: recibir, abrir desde LanChat y verlo en Archivos y la galería.
- [x] **LanChatHost**: `notify` → canal "Mensajes" (un aviso por
  conversación; al tocarlo abre la app); `unreadChanged` → texto y número en
  el aviso fijo.
- [x] **MainActivity**: WebView (JavaScript, `domStorage`) con la URL del
  servicio, edge-to-edge con márgenes de barras y teclado, enlaces externos al
  navegador, depuración con `chrome://inspect` en compilaciones debug, y pide
  `POST_NOTIFICATIONS`.
  - [x] Botón Atrás: `OnBackPressedCallback` llama a `window.lanchatBack()`
    de la página, que cierra lo que haya encima (diálogo, menú ⋯,
    conversación en pantalla angosta). Si no había nada, `moveTaskToBack`:
    la app pasa a segundo plano sin cerrarse. En el teléfono, abrir Ajustes
    o una conversación no despliega el teclado.
  - [ ] `WebChromeClient.onShowFileChooser` para `<input type="file">`
    (punto 5).
- [x] Optimización de batería (`BatteryOptimization`): la primera vez, tras
  el permiso de notificaciones, un diálogo explica por qué y abre el del
  sistema ("Permitir en segundo plano"); no vuelve a preguntar solo. En
  Ajustes, la sección "Segundo plano" (solo en la app: la página la muestra
  si existe `window.LanChatAndroid`, el puente de `MainActivity`) da el
  estado, el botón "Permitir" y el enlace a la información de la app, donde
  OPPO, Xiaomi, Huawei… tienen sus propios ajustes de batería.
  - [x] **OPPO (ColorOS, Android 16)**: con solo "Sin restricción" de
    Android, al apagar la pantalla ColorOS congela todo LanChat
    (`/sys/fs/cgroup/apps/uid_<uid>/cgroup.freeze` = 1; en logcat,
    `OplusHansManager … unfreeze … scene: LcdOff` al encenderla), aunque
    tenga servicio en primer plano: no se anuncia ni recibe. Con
    **"Permitir actividad en segundo plano"** (información de la app > Uso
    de la batería; venía en "inteligente") ya no lo congeló en 11 minutos.
    Hay que indicarlo en las instrucciones de instalación (punto 6).
  - [x] **Con la pantalla apagada no llegan los broadcast**: el Wi-Fi del
    teléfono en ahorro de energía no escucha todos los beacons y el módem
    no guarda los broadcast (sí guarda el unicast hasta que despierta). Con
    la pantalla apagada el teléfono dejaba de ver la laptop (ni un anuncio
    en 12 min), aunque LanChat seguía despierto y el filtro de paquetes
    (APF) no descartaba nada. Resuelto en el núcleo: cada equipo manda su
    anuncio además por unicast a todos los que conoce (antes solo a los de
    otras subredes). Probado: 4 min con la pantalla apagada, anuncios cada
    10 s. **Las PCs necesitan la versión nueva** para que esto funcione.
  - [ ] Probar con el teléfono **desconectado del USB** (por USB el sistema
    no se suspendió: 99 % despierto). Con batería podría suspenderse; el
    unicast debería despertarlo. Prueba: pantalla apagada 10 min, mandar un
    mensaje desde la PC y ver si suena al momento.
- [x] Detener LanChat: botón "Detener" en el aviso fijo (`ACTION_STOP` al
  servicio) y "Detener LanChat" en Ajustes (puente `stop()`). El núcleo se
  despide de la red, la ventana se cierra y el aviso fijo desaparece; al
  abrir la app arranca de nuevo. `LanChatService.running` evita que un aviso
  de no leídos tardío vuelva a poner el aviso fijo.
  - [x] Probado el botón "Detener" de la notificación.

### 5. Ajustes a la interfaz web y al núcleo

En el núcleo, `ui.Server.Shell` reemplaza las funciones del escritorio
(selectores de Windows, `openPath`); `mobile.Start` lo arma con el `Host`, y
`/api/state` lo informa a la página como `mobile: true` (clase `mobile` en
`<body>`; lo marcado `desktop-only` se oculta).

- [x] **📎 Enviar archivos**: con `mobile`, el 📎 abre un
  `<input type="file" multiple>` (en Kotlin, `onShowFileChooser` con el
  selector de Android) y sube por `/api/files/upload`, como al arrastrar.
- [x] **📁 Enviar carpeta**: oculto en Android (la WebView no elige carpetas).
- [x] **Abrir archivo recibido / enlace del proyecto**: `Host.openFile`
  (`ACTION_VIEW` con `FileProvider`, solo la carpeta de recibidos) y
  `Host.openURL`. "Mostrar en carpeta" y "Abrir carpeta" se ocultan; una
  carpeta recibida se lista archivo por archivo, cada uno con "Abrir".
  - [x] Probado: enviar un archivo desde el teléfono, y recibir y abrir uno.
- [x] **Descargar mis datos**: la WebView no descarga archivos, y en Android
  `os.TempDir()` es `/data/local/tmp` (sin permiso; gomobile solo define
  `TMPDIR` en modo app, no en `bind`). Con `mobile`, la página llama a
  `POST /api/data/export` y el núcleo guarda la copia (`VACUUM INTO`)
  directamente en la carpeta de recibidos (Descargas/LanChat), con
  "nombre (n).db" si ya existe: sin temporales ni descargas. En el escritorio
  sigue `GET /api/data/export`.
  - El aviso de la página ahora es un `popover`: se ve encima de los
    diálogos (antes "Descargando la copia…" quedaba tapado por Ajustes).
  - [x] Probado desde Ajustes y desde "Borrar todos mis datos… > Descargar
    antes una copia".
- [x] **Ajustes**: la sección Sistema ya se oculta fuera de Windows; la
  carpeta de descargas también se oculta en Android (y el servidor no deja
  cambiarla).
- [x] **Pantalla angosta**: la columna usa `minmax(0, 1fr)` (antes el texto
  ensanchaba la página y se cortaban ℹ y ⚙); las acciones de la
  conversación (Identidad, Grupo, Renombrar, Borrar) van en un menú "⋯".
  En el teléfono abrir una conversación ya no despliega el teclado.
- [ ] **Táctil**: revisar el tamaño de los botones con uso real.
- [x] **Ausente automático**: en el teléfono, inactividad = pantalla apagada.
  `ScreenIdle` (Kotlin) guarda desde cuándo está apagada (`ACTION_SCREEN_OFF`
  / `ON`, con `elapsedRealtime`, que cuenta también con el teléfono dormido)
  y `Host.IdleSeconds` se lo da al núcleo como `app.Options.IdleTime`. Tras
  10 minutos pasa a Ausente; al encenderla vuelve en menos de 30 s (cada
  cuánto se consulta). La casilla dice "con la pantalla apagada".
- [x] Textos: "esta PC" → "este equipo" donde aplica a los dos (borrar
  conversación o datos, identidad).

### 6. Distribución

- [ ] Clave de firma: crearla (ver abajo). **Guardar copia segura y nunca
  subirla al repositorio**: sin ella no se pueden publicar actualizaciones.
- [x] `go tool mage apkRelease` compila el APK firmado (`dist/LanChat-<versión>.apk`).
  Probado con una clave desechable: firma v2 válida (`apksigner verify`).
- [x] GitHub Actions: `ci.yml` compila el APK de prueba en Ubuntu en cada
  cambio (`.github/actions/android`: Java, NDK y gomobile); `release.yml`, al
  subir una etiqueta, compila el firmado con la clave de los *secrets* y lo
  publica en la Release junto al zip, con su hash en `SHA256SUMS.txt` e
  instrucciones de instalación. Validado con `actionlint`; falta la primera
  corrida real en GitHub.
- [ ] Más adelante, si se quiere: Google Play (cuenta de 25 USD, revisión
  del servicio en primer plano y del permiso de batería).
- [x] Instalación en Android documentada en README, LEEME y el texto de la
  Release.

#### Clave de firma

Una sola clave sirve para todas las apps propias; vive **fuera de cualquier
proyecto**. Se crea una vez, en una terminal (pide la contraseña: usar una
larga y guardarla en un gestor de contraseñas):

```bash
keytool -genkeypair -v -keystore "$USERPROFILE/.keystores/aerogu-release.jks" -storetype PKCS12 -keyalg RSA -keysize 4096 -validity 36500 -alias aerogu -dname "CN=Arturo Enrique Rosas Gutiérrez, O=AEROGU, C=MX"
```

(`keytool` viene con el JDK de Android Studio, en `%JAVA_HOME%\bin`; crear
antes la carpeta `.keystores`.) Copia de seguridad del `.jks` y de su
contraseña fuera de la PC. Si se pierde, una app instalada no puede
actualizarse: hay que desinstalarla (y las PCs verán "la identidad cambió").

Para que `mage apkRelease` (y cualquier otra app) la use, en
`%USERPROFILE%\.gradle\gradle.properties` (fuera del proyecto; con PKCS12 las
dos contraseñas son la misma):

```properties
aerogu.signing.storeFile=C:/Users/artur/.keystores/aerogu-release.jks
aerogu.signing.storePassword=LA_CONTRASEÑA
aerogu.signing.keyAlias=aerogu
aerogu.signing.keyPassword=LA_CONTRASEÑA
```

Para GitHub Actions, los *secrets* del repositorio (con `gh`, en PowerShell;
los de contraseña la piden sin mostrarla):

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes("$env:USERPROFILE\.keystores\aerogu-release.jks")) | gh secret set SIGNING_KEYSTORE_BASE64
gh secret set SIGNING_STORE_PASSWORD
gh secret set SIGNING_KEY_PASSWORD
gh secret set SIGNING_KEY_ALIAS --body aerogu
```

Un teléfono con el APK de prueba (`-debug`, otra firma) debe desinstalarlo
antes de instalar el firmado: Android no deja cambiar la firma de una app.

## Notas

- **Depurar la página en el teléfono** (compilación debug): con el teléfono
  conectado por USB, abrir `chrome://inspect` (o `edge://inspect`) en la PC
  y elegir la WebView de LanChat: consola, red y DOM como en el escritorio.
  Para scripts: `adb forward tcp:9333 localabstract:webview_devtools_remote_<pid>`
  y el protocolo de DevTools en `http://127.0.0.1:9333/json`.

- Cada teléfono tiene su propia identidad TLS (en `filesDir`). Si se
  desinstala la app, las PCs verán "la identidad cambió" la próxima vez.
- El teléfono usa los mismos puertos que Windows (UDP 50000 y TCP 50001);
  en Android no hace falta permiso especial para puertos mayores a 1024.
- Los mensajes no se pierden si Android pausa la app: el remitente los
  reintenta y llegan al volver.
