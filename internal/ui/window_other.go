//go:build !windows

package ui

import "os/exec"

// openWindow abre la interfaz en el navegador por defecto.
func openWindow(url, _ string) error {
	cmd := exec.Command("xdg-open", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
