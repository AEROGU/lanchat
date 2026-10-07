//go:build !windows

package ui

import (
	"errors"
	"os/exec"
	"path/filepath"
)

func pickFiles() ([]string, error) {
	return nil, errors.New("el selector de archivos solo existe en Windows; arrastra los archivos a la ventana")
}

func openPath(path string) error { return exec.Command("xdg-open", path).Start() }

func revealPath(path string) error { return openPath(filepath.Dir(path)) }
