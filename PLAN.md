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
- `POST /msg` — mensaje de chat (con ID para acuse y deduplicar).
- `POST /offer` — oferta de archivos (nombre, tamaño, id, token).
- `POST /offer/{id}/accept|reject|cancel`
- `GET /file/{id}?token=…` — descarga (soporta `Range` para reanudar).

### Reglas de archivos
1. El remitente ofrece; nada se transfiere hasta que el destinatario **acepta**.
2. El destinatario descarga desde el remitente; progreso, velocidad y cancelación en ambos lados.
3. Al completarse con hash SHA-256 correcto, el token se invalida → `410 Gone`. Para volver a recibirlo, el remitente debe reenviarlo.
4. Una descarga interrumpida puede reanudarse mientras no se haya completado.
5. La oferta caduca si el remitente cierra el programa, cancela o pasa el plazo (24 h por defecto).
6. Si el archivo original cambia (tamaño/fecha) antes de enviarse → error claro.
7. Nombres sanitizados (sin rutas, sin `..`), colisiones → `archivo (1).pdf`.

## Almacenamiento
`%APPDATA%\LanChat\`: `config.json`, `lanchat.db` (SQLite, `modernc.org/sqlite`): alias, historial, cola de pendientes, ofertas.

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
- [ ] **Hito 4 — Archivos**: ofertas, aceptar/rechazar, descarga única, progreso, cancelar, reanudar.
- [ ] **Hito 5 — Distribución**: `.exe` sin consola, script de firewall (`netsh`), arranque con Windows.

### Fase 2
Mensaje a varios / a todos · grupos locales de contactos · estados (Disponible/Ausente/Ocupado) con auto-ausente · confirmación de lectura · envío de carpetas · intercambio de listas de equipos entre PCs (gossip) para otras subredes.

### Fase 3
TLS entre equipos (certificado propio por PC, confianza en el primer uso) · salas de chat grupales · UI alternativa en tview.
