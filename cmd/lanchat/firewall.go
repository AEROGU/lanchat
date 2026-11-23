package main

import (
	"fmt"

	"github.com/AEROGU/lanchat/internal/i18n"
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
		err = i18n.E("firewall.badOption", "op", op)
	}
	if err == nil {
		if platform.IsAdmin() {
			err = applyFirewall(op, exe)
		} else if elevated {
			err = i18n.E("firewall.noAdmin")
		} else {
			var code int
			code, err = platform.RunElevated(exe, fmt.Sprintf("-firewall %s -%s", op, elevatedFlag))
			if err == nil && code != 0 {
				err = i18n.E("firewall.exitCode", "code", code)
			}
		}
	}
	if !elevated { // quien lo pidió ve el resultado
		switch {
		case err != nil:
			ui.ShowError(i18n.T("firewall.failed", "err", err))
		case op == "add":
			ui.ShowInfo(i18n.T("firewall.added"))
		default:
			ui.ShowInfo(i18n.T("firewall.removed"))
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
	platform.RemoveFirewallFor(exe)
	return nil
}
