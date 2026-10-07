package ui

import (
	"os"
	"os/exec"
	"path/filepath"
)

// windowSize es el tamaño inicial de la ventana; Edge recuerda después el
// tamaño y la posición que deje el usuario.
const windowSize = "960,680"

// openWindow abre la interfaz como ventana de aplicación de Edge (sin barra de
// direcciones), con un perfil propio en profileDir para no mezclarse con el
// navegador del usuario. Sin Edge, usa el navegador por defecto.
func openWindow(url, profileDir string) error {
	if edge := findEdge(); edge != "" {
		cmd := exec.Command(edge,
			"--app="+url,
			"--user-data-dir="+profileDir,
			"--window-size="+windowSize,
			"--no-first-run",
			"--no-default-browser-check",
		)
		if err := cmd.Start(); err == nil {
			go cmd.Wait()
			return nil
		}
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func findEdge() string {
	rel := filepath.Join("Microsoft", "Edge", "Application", "msedge.exe")
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			p := filepath.Join(base, rel)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}
