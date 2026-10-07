// Package version centraliza los números de versión de LanChat.
package version

import "strconv"

// App es la versión del programa que se muestra al usuario y se anuncia a los
// demás equipos. Se fija al compilar, sin tocar el código:
//
//	go build -ldflags "-X github.com/AEROGU/lanchat/internal/version.App=1.2.0" ./cmd/lanchat
var App = "dev"

// Protocol es la versión del protocolo entre equipos (paquetes UDP y API HTTP).
// Solo se incrementa con cambios incompatibles: agregar un campo nuevo al JSON
// no lo es, porque los equipos viejos ignoran los campos que no conocen.
const Protocol = 1

// APIPrefix es el prefijo de las rutas HTTP entre equipos, p. ej. "/v1".
var APIPrefix = "/v" + strconv.Itoa(Protocol)
