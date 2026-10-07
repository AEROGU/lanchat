# LanChat

Mensajería y envío de archivos en la LAN, sin servidor central. Ver [PLAN.md](PLAN.md).

## Compilar

Requiere Go (la versión indicada en `go.mod`). Mage y staticcheck se instalan
solos como herramientas del módulo (`tool` en `go.mod`), no hace falta nada más.

```bash
go tool mage -l       # lista los targets
go tool mage          # compila dist/lanchat.exe (target build)
go tool mage check    # gofmt, go vet, staticcheck y pruebas: correr antes de cada commit
go tool mage race     # pruebas con detector de carreras (requiere gcc)
```

La versión del ejecutable sale de `git describe`: para publicar la 1.0.0 se
crea la etiqueta `git tag v1.0.0` y se compila.

## Uso (provisional, por consola)

```bash
dist/lanchat.exe -debug
```

Escribe `/ayuda` para ver los comandos. La primera vez Windows pedirá permiso
de red: hay que permitirlo en **redes privadas** (UDP 50000 y TCP 50001).
