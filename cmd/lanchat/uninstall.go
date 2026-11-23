package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AEROGU/lanchat/internal/config"
	"github.com/AEROGU/lanchat/internal/i18n"
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
		ui.ShowError(i18n.T("uninstall.failed", "err", err))
		return 1
	}
	if !ui.Confirm(uninstallQuestion(dir, customDir, exe)) {
		return 0
	}
	if _, err := ui.QuitRunning(dir, quitTimeout); err != nil {
		ui.ShowError(i18n.T("uninstall.nothing", "err", err))
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
			fail(i18n.T("uninstall.autostartFailed", "err", err))
		}
		if err := ui.UnregisterNotifications(); err != nil {
			fail(i18n.T("uninstall.notificationsFailed", "err", err))
		}
		if platform.FirewallRuleExists(exe) {
			code, err := platform.RunElevated(exe, "-firewall remove -"+elevatedFlag)
			if err != nil || code != 0 {
				fail(i18n.T("uninstall.firewallLeft"))
			}
		}
	}

	if uninstall.IsDataDir(dir) {
		switch removed, err := uninstall.RemoveData(dir, removeWait); {
		case err != nil:
			fail(i18n.T("uninstall.dataFailed", "dir", dir, "err", err))
		case !removed:
			notes = append(notes, i18n.T("uninstall.dataKept", "dir", dir))
		}
	}

	if anyExists(append([]string{downloads}, received...)) {
		if ui.Confirm(downloadsQuestion(downloads)) {
			if _, err := uninstall.RemoveDownloads(downloads, own, received); err != nil {
				fail(i18n.T("uninstall.downloadsFailed", "err", err))
			}
		} else if downloads != "" {
			notes = append(notes, i18n.T("uninstall.downloadsKept", "dir", downloads))
		}
	}

	msg := i18n.T("uninstall.done")
	if len(notes) > 0 {
		msg += "\n\n" + strings.Join(notes, "\n\n")
	}
	msg += "\n\n" + i18n.T("uninstall.deleteProgram", "dir", filepath.Dir(exe))
	ui.ShowInfo(msg)
	ui.RevealFile(exe)
	if failed {
		return 1
	}
	return 0
}

func uninstallQuestion(dir string, customDir bool, exe string) string {
	var b strings.Builder
	b.WriteString(i18n.T("uninstall.ask") + "\n")
	if !customDir {
		b.WriteString("• " + i18n.T("uninstall.askAutostart") + "\n")
		if platform.FirewallRuleExists(exe) {
			b.WriteString("• " + i18n.T("uninstall.askFirewall") + "\n")
		}
	}
	b.WriteString("• " + i18n.T("uninstall.askData") + "\n   " + dir + "\n\n")
	b.WriteString(i18n.T("uninstall.askWarning"))
	return b.String()
}

func downloadsQuestion(downloads string) string {
	msg := i18n.T("uninstall.askDownloads") + "\n\n"
	if downloads != "" {
		msg += i18n.T("uninstall.folder", "dir", downloads) + "\n\n"
	}
	return msg + i18n.T("uninstall.yesNo")
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
