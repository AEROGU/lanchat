package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AEROGU/lanchat/internal/config"
	"github.com/AEROGU/lanchat/internal/platform"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/transfer"
	"github.com/AEROGU/lanchat/internal/ui"
	"github.com/AEROGU/lanchat/internal/uninstall"
)

const (
	// dbFileName es la base de datos dentro de la carpeta de datos.
	dbFileName = "lanchat.db"
	// quitTimeout: espera a que el LanChat abierto termine de cerrarse.
	quitTimeout = 15 * time.Second
	// removeWait: reintentos al borrar lo que Edge tarda en soltar.
	removeWait = 10 * time.Second
)

// runUninstall quita LanChat de este equipo: lo cierra, borra el inicio con
// Windows, el registro de notificaciones y la regla del firewall, borra la
// carpeta de datos y pregunta si también los archivos recibidos. Con -dir
// (customDir) solo borra esa carpeta de datos, sin tocar lo de la instalación
// normal. Lo único que queda es el propio programa, que no puede borrarse
// mientras se ejecuta. Devuelve el código de salida.
func runUninstall(dir string, customDir bool) int {
	exe, err := platform.Executable()
	if err != nil {
		ui.ShowError("No se pudo desinstalar LanChat:\n\n" + err.Error())
		return 1
	}
	if !ui.Confirm(uninstallQuestion(dir, customDir, exe)) {
		return 0
	}
	if _, err := ui.QuitRunning(dir, quitTimeout); err != nil {
		ui.ShowError("No se desinstaló nada.\n\n" + err.Error())
		return 1
	}

	// Antes de borrar los datos: dónde están los archivos recibidos.
	downloads, own, received := receivedFiles(dir)

	// notes se muestran al final; failed: algo no se pudo quitar.
	var notes []string
	failed := false
	fail := func(note string) {
		notes = append(notes, note)
		failed = true
	}
	if !customDir {
		if err := platform.SetAutostart(false, exe); err != nil && !errors.Is(err, platform.ErrUnsupported) {
			fail("No se pudo quitar el inicio con Windows: " + err.Error())
		}
		if err := ui.UnregisterNotifications(); err != nil {
			fail("No se pudo quitar el registro de notificaciones: " + err.Error())
		}
		if platform.FirewallRuleExists(exe) {
			code, err := platform.RunElevated(exe, "-firewall remove -"+elevatedFlag)
			if err != nil || code != 0 {
				fail("La regla del Firewall de Windows sigue ahí (no se dio el permiso de " +
					"administrador). Para quitarla: Firewall de Windows > Configuración avanzada > Reglas de entrada > LanChat.")
			}
		}
	}

	if uninstall.IsDataDir(dir) {
		switch removed, err := uninstall.RemoveData(dir, removeWait); {
		case err != nil:
			fail("No se pudo borrar todo en " + dir + ":\n" + err.Error())
		case !removed:
			notes = append(notes, "La carpeta "+dir+" tenía otros archivos y se conservó; lo de LanChat ya se borró.")
		}
	}

	if anyExists(append([]string{downloads}, received...)) {
		if ui.Confirm(downloadsQuestion(downloads)) {
			if _, err := uninstall.RemoveDownloads(downloads, own, received); err != nil {
				fail("No se pudieron borrar todos los archivos recibidos:\n" + err.Error())
			}
		} else if downloads != "" {
			notes = append(notes, "Los archivos recibidos siguen en "+downloads+".")
		}
	}

	msg := "LanChat se desinstaló de esta PC."
	if len(notes) > 0 {
		msg += "\n\n" + strings.Join(notes, "\n\n")
	}
	msg += "\n\nPara terminar, borra la carpeta del programa (se abrirá al aceptar):\n" + filepath.Dir(exe)
	ui.ShowInfo(msg)
	ui.RevealFile(exe)
	if failed {
		return 1
	}
	return 0
}

func uninstallQuestion(dir string, customDir bool, exe string) string {
	var b strings.Builder
	b.WriteString("¿Desinstalar LanChat de esta PC?\n\nSe cerrará LanChat y se quitará:\n")
	if !customDir {
		b.WriteString("• el inicio con Windows y el registro de notificaciones\n")
		if platform.FirewallRuleExists(exe) {
			b.WriteString("• la regla del Firewall de Windows (pedirá permiso de administrador)\n")
		}
	}
	b.WriteString("• tus conversaciones, contactos, salas, ajustes y la identidad de esta PC:\n   " + dir + "\n\n")
	b.WriteString("No se puede deshacer. Los demás conservan su copia de las conversaciones.\n" +
		"Después se te preguntará si también quieres borrar los archivos recibidos.")
	return b.String()
}

func downloadsQuestion(downloads string) string {
	msg := "¿Borrar también los archivos recibidos con LanChat?\n\n"
	if downloads != "" {
		msg += "Carpeta: " + downloads + "\n\n"
	}
	return msg + "Sí: se borran.\nNo: se conservan donde están."
}

// receivedFiles devuelve la carpeta de descargas, si es la propia de LanChat
// (Descargas\LanChat, que se puede borrar entera) y las rutas de los archivos
// recibidos que la base recuerda. Sin datos legibles devuelve lo que pueda.
func receivedFiles(dir string) (downloads string, own bool, received []string) {
	downloads, own = transfer.DefaultDownloadDir(), true
	if !uninstall.IsDataDir(dir) {
		return downloads, own, nil
	}
	if cfg, err := config.Load(dir); err == nil && cfg.DownloadDir != "" {
		downloads, own = cfg.DownloadDir, false
	}
	st, err := store.Open(filepath.Join(dir, dbFileName))
	if err != nil {
		return downloads, own, nil
	}
	defer st.Close()
	received, _ = st.ReceivedFiles(context.Background())
	return downloads, own, received
}

func anyExists(paths []string) bool {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
