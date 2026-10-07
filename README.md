# LanChat

Mensajería y envío de archivos en la LAN, sin servidor central. Ver [PLAN.md](PLAN.md).

## Compilar

Requiere Go (la versión indicada en `go.mod`). Mage y staticcheck se instalan
solos como herramientas del módulo (`tool` en `go.mod`), no hace falta nada más.

```bash
go tool mage -l       # lista los targets
go tool mage          # compila dist/lanchat.exe, sin consola (target build)
go tool mage debug    # compila dist/lanchat-debug.exe, con consola
go tool mage check    # gofmt, go vet, staticcheck y pruebas: correr antes de cada commit
go tool mage race     # pruebas con detector de carreras (requiere gcc)
go tool mage dist     # dist/LanChat-<versión>.zip: exe, LEEME, licencia y avisos de terceros
```

`build` genera antes `cmd/lanchat/rsrc_windows_amd64.syso` (ícono, versión y
manifest del .exe, target `resources`); el archivo no se versiona. El texto de
`LEEME.txt` del zip está en `packaging/LEEME.txt`.

La versión del ejecutable sale de `git describe`: para publicar la 1.0.0 se
crea la etiqueta `git tag v1.0.0` y se compila.

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

## Archivos en `%APPDATA%\LanChat`

| Archivo | Contenido |
|---|---|
| `config.json` | ID del equipo, nombre, puertos, equipos de otras subredes |
| `lanchat.db` | contactos, alias e historial (SQLite) |
| `lanchat.log` | registro (se rota al pasar de 1 MB) |
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
