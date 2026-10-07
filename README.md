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
```

La versión del ejecutable sale de `git describe`: para publicar la 1.0.0 se
crea la etiqueta `git tag v1.0.0` y se compila.

## Uso

`dist/lanchat.exe` abre la ventana y queda en la bandeja del sistema (clic
izquierdo: abrir; clic derecho: menú con **Salir**). Cerrar la ventana no
cierra LanChat. Abrirlo de nuevo con LanChat ya abierto solo muestra la ventana.

La primera vez Windows pedirá permiso de red: hay que permitirlo en **redes
privadas** (UDP 50000 y TCP 50001).

Opciones:

| Opción | Uso |
|---|---|
| `-hidden` | arrancar solo en la bandeja (para el inicio con Windows) |
| `-dir carpeta` | otra carpeta de datos (por defecto `%APPDATA%\LanChat`) |
| `-version` | mostrar la versión |

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
