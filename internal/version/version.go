// Package version guarda la versión del programa. La versión del protocolo
// entre equipos está en el paquete protocol.
package version

// App es la versión del programa que se muestra al usuario y se anuncia a los
// demás equipos. La fija el target de compilación (mage build) con:
//
//	-ldflags "-X github.com/AEROGU/lanchat/internal/version.App=1.2.0"
var App = "dev"
