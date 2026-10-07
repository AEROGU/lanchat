package main

import (
	"errors"
	"fmt"

	"github.com/AEROGU/lanchat/internal/platform"
	"github.com/AEROGU/lanchat/internal/ui"
)

// elevatedFlag marca al proceso que se relanzó como administrador: no muestra
// mensajes (los muestra quien lo lanzó) y no vuelve a pedir permisos.
const elevatedFlag = "elevated"

// runFirewall crea (add) o borra (remove) la regla del Firewall de Windows
// para este ejecutable. Si no es administrador, se relanza pidiendo permiso.
// Devuelve el código de salida del proceso.
func runFirewall(op string, elevated bool) int {
	exe, err := platform.Executable()
	if err == nil && op != "add" && op != "remove" {
		err = fmt.Errorf("opción -firewall desconocida: %q (usa add o remove)", op)
	}
	if err == nil {
		if platform.IsAdmin() {
			err = applyFirewall(op, exe)
		} else if elevated {
			err = errors.New("no se obtuvieron permisos de administrador")
		} else {
			var code int
			code, err = platform.RunElevated(exe, fmt.Sprintf("-firewall %s -%s", op, elevatedFlag))
			if err == nil && code != 0 {
				err = fmt.Errorf("el proceso de administrador terminó con código %d", code)
			}
		}
	}
	if !elevated { // quien lo pidió ve el resultado
		switch {
		case err != nil:
			ui.ShowError("No se pudo configurar el Firewall de Windows:\n\n" + err.Error())
		case op == "add":
			ui.ShowInfo("Listo: LanChat puede recibir conexiones en redes privadas y de dominio.")
		default:
			ui.ShowInfo("Listo: se quitó la regla de LanChat del Firewall de Windows.")
		}
	}
	if err != nil {
		return 1
	}
	return 0
}

func applyFirewall(op, exe string) error {
	if op == "add" {
		return platform.AllowFirewall(exe)
	}
	return platform.RemoveFirewall()
}
