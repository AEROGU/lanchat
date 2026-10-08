# LanChat

Mensajería y envío de archivos en la LAN, sin servidor central. Ver [PLAN.md](PLAN.md).

## Compilar

Requiere Go (la versión indicada en `go.mod`). Mage y staticcheck se instalan
solos como herramientas del módulo (`tool` en `go.mod`), no hace falta nada más.

```bash
go tool mage         # lista los targets con su descripción
go tool mage build   # compila dist/lanchat.exe (sin consola)
go tool mage debug   # compila dist/lanchat-debug.exe, con consola
go tool mage check   # gofmt, go vet, staticcheck y pruebas: correr antes de cada commit
go tool mage race    # pruebas con detector de carreras (requiere gcc)
go tool mage dist    # dist/LanChat-<versión>.zip: exe, LEEME, licencia y avisos de terceros
```

`build` genera antes `cmd/lanchat/rsrc_windows_amd64.syso` (ícono, versión y
manifest del .exe, target `resources`); el archivo no se versiona. El texto de
`LEEME.txt` del zip está en `packaging/LEEME.txt`.

La versión del ejecutable sale de `git describe`: para publicar la 1.0.0 se
crea la etiqueta `git tag v1.0.0` y se compila.

## Aviso de Windows SmartScreen

LanChat no está firmado con un certificado de firma de código (cuestan
dinero cada año). Por eso, la primera vez que se abre un `lanchat.exe`
descargado de Internet, Windows puede mostrar **"Windows protegió su PC"**.
Para abrirlo:

1. Haz clic en **Más información**.
2. Haz clic en **Ejecutar de todas formas**.

Solo hace falta la primera vez en cada PC. Descarga LanChat únicamente de la
página oficial del proyecto (https://github.com/AEROGU/lanchat) y, si
quieres verificarlo, compila tú mismo el código fuente (ver arriba).

## Uso

`dist/lanchat.exe` abre la ventana y queda en la bandeja del sistema (clic
izquierdo: abrir; clic derecho: menú con **Salir**). Cerrar la ventana no
cierra LanChat. Abrirlo de nuevo con LanChat ya abierto solo muestra la ventana.

La primera vez Windows pedirá permiso de red: hay que permitirlo en **redes
privadas** (UDP 50000 y TCP 50001). También se puede crear la regla desde
Ajustes > Firewall de Windows, o con `lanchat.exe -firewall add`.

En la primera ejecución se activa el inicio con Windows (en la bandeja); se
desactiva en Ajustes. Con `-dir` no se toca, para no reemplazar el de la
instalación normal.

Opciones:

| Opción | Uso |
|---|---|
| `-hidden` | arrancar solo en la bandeja (para el inicio con Windows) |
| `-dir carpeta` | otra carpeta de datos (por defecto `%APPDATA%\LanChat`) |
| `-version` | mostrar la versión |
| `-firewall add` / `remove` | crear o quitar la regla del Firewall de Windows (pide administrador) |

Para diagnóstico, `go tool mage debug` compila `dist/lanchat-debug.exe`, que
tiene consola:

| Opción | Uso |
|---|---|
| `-debug` | registro detallado en la consola en vez de `lanchat.log` |
| `-console` | usar LanChat desde la consola, sin ventana (`/ayuda` lista los comandos) |

## Seguridad

Toda la comunicación entre PCs (mensajes, archivos, avisos) va cifrada con
**TLS 1.3 mutuo**. Cada instalación crea su propia identidad (clave ECDSA
P-256 en `identity.key`) y los demás la reconocen por su **huella**:

- La primera vez que una PC ve a otra, guarda su huella (confianza en el
  primer uso, como SSH). Desde entonces solo se comunica con esa identidad.
- Si la huella cambia (reinstalaron LanChat, o alguien intenta hacerse pasar
  por esa PC), los envíos se detienen y la conversación muestra un aviso con
  **Confiar en la nueva identidad**.
- Las huellas se ven en el botón **Identidad** de cada conversación; para
  verificar, compáralas con la otra persona.
- El descubrimiento por UDP no va cifrado (nombre, hostname, estado y huella
  anunciados son visibles en la red), pero no permite suplantar a nadie: la
  conexión TLS comprueba la huella.

Desde la 0.10 se usa el protocolo v2 (cifrado), que no se comunica con
versiones anteriores: hay que actualizar todas las PCs.

## Privacidad

En **Ajustes > Privacidad**:

- **Descargar mis datos**: guarda en Descargas una copia de `lanchat.db`
  (historial, contactos, salas y ofertas de archivos). Se abre con cualquier
  programa para SQLite, p. ej. DB Browser for SQLite.
- **Borrar todos mis datos** (p. ej. cuando alguien deja la empresa): sale de
  las salas avisando a los demás, cancela los archivos pendientes y borra las
  conversaciones, salas, alias y grupos de contactos, el nombre y el mensaje
  de estado. Pide escribir `BORRAR` para confirmar. Conserva la configuración
  de red y la identidad de la PC. Los archivos ya recibidos quedan en su
  carpeta.

Cada conversación (o sala) tiene además el botón **Borrar**: borra sus
mensajes y cancela los archivos pendientes con ese contacto; el contacto se
conserva. Borrar una sala de la que ya saliste la quita de la lista.

Lo borrado no queda recuperable en `lanchat.db`: la base usa `secure_delete`
(sobrescribe con ceros) y se vacía su registro (`-wal`). Borrar es solo en
esta PC: los demás conservan su copia de las conversaciones.

## Archivos en `%APPDATA%\LanChat`

| Archivo | Contenido |
|---|---|
| `config.json` | ID del equipo, nombre, puertos, equipos de otras subredes |
| `identity.key`, `identity.crt` | identidad de esta PC; no copiarlos a otra PC (si se borran, los demás verán "identidad cambió") |
| `lanchat.db` | contactos, alias e historial (SQLite) |
| `lanchat.log` | registro (se rota al pasar de 1 MB); no guarda mensajes |
| `ui.json` | puerto y token de la ventana; existe mientras LanChat está abierto |
| `icon.png` | icono que usan las notificaciones |
| `edge\` | perfil de Edge de la ventana de LanChat |

## Licencia

Copyright © 2026 Arturo Enrique Rosas Gutiérrez.

LanChat es software libre: puedes usarlo, estudiarlo, modificarlo y
redistribuirlo bajo los términos de la **Licencia Pública General de GNU,
versión 3** (GPL v3), publicada por la Free Software Foundation. Quien
distribuya LanChat o una versión modificada debe conservar este aviso y la
mención del autor original, y publicar el código fuente bajo la misma
licencia.

Se distribuye con la esperanza de que sea útil, pero **sin ninguna
garantía**, ni siquiera la implícita de comerciabilidad o de idoneidad para
un propósito particular. Ver el texto completo en [LICENSE](LICENSE).

Las librerías de terceros incluidas en el ejecutable (todas con licencias
BSD, MIT o Apache 2.0, compatibles con la GPL v3) se listan con sus avisos
en `THIRD_PARTY_NOTICES.txt`, que genera `go tool mage notices` y se incluye
en el zip.
