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
- `POST /v1/msg` — mensaje de chat (ID para acuse y deduplicar). Una oferta de archivos es un mensaje con el campo `offer` (nombres, tamaños, token, caducidad), así reutiliza la cola para equipos desconectados.
- `GET /v1/transfers/{id}/files/{idx}` — descarga; encabezado `X-Lanchat-Token`, admite `Range: bytes=N-` y manda el SHA-256 del archivo completo en el trailer `X-Lanchat-Sha256`.
- `POST /v1/transfers/{id}/files/{idx}/done` — el destinatario confirma que llegó íntegro.
- `POST /v1/transfers/{id}/state` — avisa un rechazo o cancelación al otro equipo.

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
internal/ui/          servidor web local, frontend embebido, bandeja, notificaciones
```

## Hitos

- [x] **Hito 1 — Descubrimiento**: los equipos se ven, aparecen y desaparecen (salida por consola).
- [x] **Hito 2 — Mensajería**: chat 1 a 1, historial SQLite, cola para desconectados, acuse de entrega (por ahora desde consola: `/ayuda`).
- [x] **Hito 3 — Interfaz**: UI web + `msedge --app` + bandeja + notificaciones + alias + cambio de nombre propio + instancia única + equipos manuales editables + no leídos.
- [x] **Hito 4 — Archivos**: ofertas, aceptar/rechazar, descarga única, progreso, cancelar, reanudar, verificación SHA-256, arrastrar y soltar.
- [ ] **Hito 5 — Distribución**: `.exe` sin consola, script de firewall (`netsh`), arranque con Windows.

### Fase 2
Mensaje a varios / a todos · grupos locales de contactos · estados (Disponible/Ausente/Ocupado) con auto-ausente · confirmación de lectura · envío de carpetas · intercambio de listas de equipos entre PCs (gossip) para otras subredes.

### Fase 3
TLS entre equipos (certificado propio por PC, confianza en el primer uso) · salas de chat grupales · UI alternativa en tview.
