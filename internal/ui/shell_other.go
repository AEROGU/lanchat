//go:build !windows

package ui

import (
	"os/exec"
	"path/filepath"

	"github.com/AEROGU/lanchat/internal/i18n"
)

func pickFiles() ([]string, error) {
	return nil, i18n.E("shell.filePickerWindows")
}

func openPath(path string) error { return exec.Command("xdg-open", path).Start() }

func revealPath(path string) error { return openPath(filepath.Dir(path)) }

func pickFolder(string, bool) (string, error) {
	return "", i18n.E("shell.folderPickerWindows")
}

// copyFiles: el portapapeles de archivos solo está hecho para Windows.
func copyFiles([]string) error { return errNoShare }

// RevealFile abre la carpeta del archivo.
func RevealFile(path string) error { return revealPath(path) }
