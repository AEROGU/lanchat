# LanChat — Plan

Mensajería y envío de archivos en LAN, sin servidor central, al estilo SOFTROS LAN Messenger.
Un solo ejecutable Go (sin cgo) por PC; las PCs se descubren solas.

## Decisiones tomadas

| Tema | Decisión |
|---|---|
| Interfaz | Web local embebida (`go:embed`), abierta con `msedge --app=http://127.0.0.1:PUERTO/?t=TOKEN` (fallback: navegador por defecto) |
| Consola | Oculta: compilación con `-ldflags "-H=windowsgui"` |
| Segundo plano | Icono en bandeja (`fyne.io/systray`) + notificaciones de Windows (toast). Cerrar la ventana no cierra el programa |
| Red | ~4 PCs en la misma subred + 1-2 en otra subred (vía IPs/hostnames manuales) |
| Nombres | Cada usuario elige su nombre; siempre se muestran también hostname e IP. Además alias locales |
| Cifrado | Fase 3 |

### Wails vs `msedge --app`
Se eligió `msedge --app` porque:
- Wails v2 no trae icono de bandeja, y mezclarlo con librerías de systray da conflictos con el bucle de mensajes de Windows.
- `msedge --app` no requiere Node ni el CLI de Wails: basta `go build`.
- La UI se comunica con el núcleo por HTTP + WebSocket local, así que migrar a Wails/WebView2 después solo cambia el "contenedor", no el frontend.

## Identidad y nombres

- **ID interno**: UUID generado en la primera ejecución (`%APPDATA%\LanChat\config.json`). Sobrevive a cambios de IP (DHCP).
- **Nombre mostrado**, por prioridad: alias local > nombre elegido por el usuario remoto > hostname > IP.
- Debajo del nombre siempre se ve `HOSTNAME · IP`.

## Protocolo

### Descubrimiento — UDP :50000
Paquetes JSON: `{"m":"lanchat","v":1,"t":"hello|announce|bye","id":"…","name":"…","host":"…","port":50001}`

- Al iniciar: `hello` → los demás responden `announce` por unicast.
- Cada 10 s: `announce`. Sin anuncios durante 35 s → desconectado.
- Al salir: `bye`.
- Destinos: broadcast dirigido por cada interfaz + `255.255.255.255` + unicast a:
  - equipos manuales (`manual_peers` en config: `ip`, `ip:puerto` u `hostname`),
  - equipos conocidos de otras subredes (basta con que **un** lado tenga al otro configurado).
- Si llega un paquete con nuestro ID desde otra IP → aviso de ID duplicado (config copiada entre PCs).

### Comunicación — HTTP :50001 (entre equipos)
Rutas definidas en `internal/protocol/routes.go`:
- `POST /v1/msg` — mensaje de chat (ID para acuse y deduplicar). Una oferta de archivos es un mensaje con el campo `offer` (nombres, tamaños, token, caducidad), así reutiliza la cola para equipos desconectados; un mensaje de sala lleva el campo `room`.
- `GET /v1/transfers/{id}/files/{idx}` — descarga; encabezado `X-Lanchat-Token`, admite `Range: bytes=N-` y manda el SHA-256 del archivo completo en el trailer `X-Lanchat-Sha256`.
- `POST /v1/transfers/{id}/files/{idx}/done` — el destinatario confirma que llegó íntegro.
- `POST /v1/transfers/{id}/state` — avisa un rechazo o cancelación al otro equipo.
- `POST /v1/read` — avisos de lectura (IDs leídos, en tandas).
- `GET /v1/peers` — equipos en línea que conoce este equipo (listas compartidas).

### Salas
- Cada mensaje de sala es un `POST /v1/msg` con el campo `room` (`id`, `name`, `members`, `version`) que se entrega **a cada miembro por separado** con la misma cola que los mensajes 1 a 1 (tabla `room_deliveries`): un miembro desconectado lo recibe al volver. El mensaje queda ✓ cuando lo recibieron todos.
- La sala viaja completa con cada mensaje; cada equipo se queda con la de mayor `version`. Crear, agregar miembros, renombrar o salir sube la versión y envía un aviso ("Ana agregó a Ceci") que no cuenta como no leído.
- Cualquier miembro puede agregar a otros y renombrar; cada quien puede salir (conserva el historial). Los nuevos ven desde que entran. Máximo `protocol.MaxRoomMembers` (50).
- Se aceptan mensajes solo de miembros (con la identidad TLS verificada). Si quien escribe no es miembro aquí, debe traer una versión más nueva de la sala (otro miembro lo agregó y el cambio aún no llega).
- Sin archivos ni ✓✓ en las salas por ahora. Dos cambios simultáneos con la misma versión pueden dejar copias distintas hasta el siguiente cambio.

### Reglas de archivos
1. El remitente ofrece; nada se transfiere hasta que el destinatario **acepta**.
2. El destinatario descarga desde el remitente; progreso, velocidad y cancelación en ambos lados.
3. Al confirmar un archivo con SHA-256 correcto, el remitente responde `410 Gone` a nuevas descargas. Para volver a recibirlo, el remitente debe reenviarlo.
4. Una descarga interrumpida se reanuda desde el `.lanchat-part` (Reintentar).
5. La oferta caduca a las 24 h o si el remitente la cancela. Se guarda en SQLite: sobrevive a reinicios del remitente.
6. Si el archivo original cambia (tamaño/fecha) antes de enviarse → error claro.
7. Nombres adaptados a Windows (sin rutas, caracteres prohibidos ni nombres reservados), colisiones → `archivo (1).pdf`.
8. Los archivos recibidos llevan la "marca de la Web" (Zone.Identifier) como los descargados por un navegador: Windows/Office avisan antes de ejecutar programas o macros. La interfaz pide confirmación para abrir programas o scripts.
9. Se comprueba el espacio libre antes de aceptar.
10. Archivos arrastrados a la ventana: el navegador no da su ruta, así que se copian a `%APPDATA%\LanChat\outbox` y se borran al terminar la oferta.

## Almacenamiento
`%APPDATA%\LanChat\`: `config.json` (preferencias), `lanchat.db` (SQLite embebido en el ejecutable con `modernc.org/sqlite`, sin instalar nada): alias, historial, cola de pendientes, ofertas de archivos.

## Privacidad
- `GET /api/data/export` (interfaz local): copia consistente con `VACUUM INTO`.
- Borrar una conversación: mensajes y transferencias con ese contacto (las pendientes se cancelan y se avisa al otro); el contacto (alias, grupo, huella) se conserva.
- Borrar una sala: sus mensajes, salvo los avisos de salida aún sin entregar. Si ya se salió, la sala queda oculta (`rooms.hidden`) y, entregado el aviso, sin nombre ni miembros: solo su ID, para ignorar los mensajes de quienes aún no saben que salió. Si alguien vuelve a agregar al equipo (versión nueva), reaparece.
- Borrar todo: salir de las salas, cancelar transferencias, borrar mensajes/salas/transferencias, alias y grupos, nombre y estado; `VACUUM`. Se conservan el directorio de equipos con sus huellas (no son datos del usuario y protegen contra suplantaciones), la configuración de red y la identidad.
- La base usa `secure_delete` y tras cada borrado se vacía el WAL (`wal_checkpoint(TRUNCATE)`): lo borrado no queda en el disco.

## Estructura
```
cmd/lanchat/          main
magefiles/            targets de compilación (go tool mage)
internal/protocol/    contrato entre equipos: versión, puertos, tiempos, límites, validación
internal/version/     versión del programa (la fija mage build)
internal/ids/         UUID
internal/app/         une todo; lo único que usa la interfaz
internal/config/      config.json (ID, nombre, puertos, equipos manuales)
internal/discovery/   anuncios UDP, registro de equipos
internal/peer/        servidor/cliente HTTP entre equipos
internal/chat/        mensajes, cola offline, acuses
internal/transfer/    ofertas, tokens de un solo uso, progreso, hash
internal/store/       SQLite
internal/ui/          servidor web local, frontend embebido, bandeja, notificaciones (gui.go: solo escritorio)
mobile/               API del núcleo para la app de Android (gomobile)
internal/icon/        ícono dibujado por código (bandeja, página, .exe)
internal/platform/    Windows: inicio automático, firewall, permisos de administrador
packaging/            LEEME.txt del zip
docs/                 capturas del README, ANDROID.md (guía y pendientes de Android)
```

## Hitos

- [x] **Hito 1 — Descubrimiento**: los equipos se ven, aparecen y desaparecen (salida por consola).
- [x] **Hito 2 — Mensajería**: chat 1 a 1, historial SQLite, cola para desconectados, acuse de entrega (por ahora desde consola: `/ayuda`).
- [x] **Hito 3 — Interfaz**: UI web + `msedge --app` + bandeja + notificaciones + alias + cambio de nombre propio + instancia única + equipos manuales editables + no leídos.
- [x] **Hito 4 — Archivos**: ofertas, aceptar/rechazar, descarga única, progreso, cancelar, reanudar, verificación SHA-256, arrastrar y soltar.
- [x] **Hito 5 — Distribución**: `.exe` sin consola con ícono, versión y manifest; regla del firewall (`-firewall`, botón en Ajustes); inicio con Windows; `mage dist` arma el zip con LEEME.txt.

### Fase 2 (completa)
- [x] Estados Disponible / Ausente / Ocupado con texto y ausente automático (10 min); Ocupado silencia notificaciones.
- [x] Mensaje a varios / a todos (marcado "📢 Mensaje a varios").
- [x] Confirmación de lectura (✓✓), desactivable.
- [x] Grupos locales de contactos (lista plegable; selección por grupo en el mensaje a varios).
- [x] Envío de carpetas (selector 📁 y arrastrar; estructura conservada; "Proyecto (1)" si ya existe).
- [x] Listas de equipos compartidas (`GET /v1/peers`): una PC de otra subred configurada en un solo equipo llega a todos.

Compatibilidad: todos los campos nuevos son opcionales; una PC con 0.9.0 sigue chateando con las nuevas (no ve estados ni ✓✓ y recibe las carpetas como archivos sueltos).

### Fase 3
- [x] TLS 1.3 mutuo entre equipos: identidad propia por PC, confianza en el primer uso, aviso y "Confiar en la nueva identidad" si cambia; protocolo v2.
- [x] Salas de chat grupales (👥): sin servidor, cada miembro guarda su copia; ver "Salas" arriba.
- [ ] Interfaz alternativa en consola (tview).

### Fase 4: Android
- [x] Núcleo portable (`mage portable` en `check`) y API `mobile/` para gomobile.
- [ ] App Android (Kotlin + WebView + servicio en primer plano): ver [docs/ANDROID.md](docs/ANDROID.md).

### Fase 5 (después de Android)
- [ ] **Vista previa de imágenes y archivos**, como en WhatsApp, en PC y en
  teléfono: si el archivo es compatible se ve en la conversación; si no, se
  muestra como ahora.
  - Hoy un archivo se ofrece y hay que aceptarlo antes de descargarlo. Para
    ver la imagen antes de aceptar, el remitente mandaría una miniatura
    pequeña (p. ej. JPEG de ~320 px) en la oferta, como campo opcional del
    protocolo (las versiones anteriores lo ignoran).
  - Ya recibida, mostrar la imagen completa en la conversación (tocar para
    ampliar) desde una ruta local que solo sirva archivos de transferencias
    completadas.
  - Por decidir: qué formatos (JPG, PNG, GIF, WebP seguro; ¿video y PDF con
    miniatura?), tamaño máximo de la miniatura y si se generan solo en Go
    (sin cgo) o también con ayuda de Android.
