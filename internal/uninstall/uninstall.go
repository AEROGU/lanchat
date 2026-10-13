// Package uninstall borra de un equipo los archivos que LanChat guardó: sus
// datos (conversaciones, identidad, registro) y, si el usuario quiere, los
// archivos recibidos. Regla de seguridad: solo se borra lo que LanChat creó,
// y una carpeta solo se elimina si queda vacía (salvo Descargas\LanChat, que
// es suya). Lo propio de Windows (inicio automático, firewall,
// notificaciones) lo quita cmd/lanchat.
package uninstall

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// configFile identifica una carpeta de datos de LanChat.
	configFile = "config.json"
	// partSuffix es el de las descargas a medias (transfer).
	partSuffix = ".lanchat-part"
	// retryEvery: pausa entre intentos de borrar algo que otro proceso aún
	// tiene abierto (Edge tarda un momento en soltar su perfil).
	retryEvery = 250 * time.Millisecond
)

// dataEntries es todo lo que LanChat crea en su carpeta de datos.
var dataEntries = []string{
	configFile,
	"identity.key", "identity.crt",
	"lanchat.db", "lanchat.db-wal", "lanchat.db-shm",
	"lanchat.log", "lanchat.log.1",
	"ui.json", "icon.png",
	"edge",   // perfil de Edge de la ventana
	"outbox", // copias temporales de archivos arrastrados
}

// IsDataDir indica si dir es una carpeta de datos de LanChat.
func IsDataDir(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, configFile))
	return err == nil && !fi.IsDir()
}

// RemoveData borra de dir los archivos de LanChat y, si queda vacía, la
// carpeta. Reintenta hasta wait lo que esté en uso. removed indica si la
// carpeta se eliminó; false sin error significa que tenía otros archivos.
func RemoveData(dir string, wait time.Duration) (removed bool, err error) {
	if !IsDataDir(dir) {
		return false, errors.New("no parece una carpeta de datos de LanChat: " + dir)
	}
	var errs []error
	for _, name := range dataEntries {
		errs = append(errs, removeAllRetry(filepath.Join(dir, name), wait))
	}
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	return os.Remove(dir) == nil, nil
}

func removeAllRetry(path string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := os.RemoveAll(path)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(retryEvery)
	}
}

// RemoveDownloads borra los archivos recibidos: los de la lista received
// (rutas que LanChat anotó al guardarlos, estén donde estén), las descargas
// a medias de dir y las subcarpetas que queden vacías. Si own (dir es
// Descargas\LanChat, la carpeta propia de LanChat) borra la carpeta entera;
// si no, solo si queda vacía. removed indica si dir se eliminó.
func RemoveDownloads(dir string, own bool, received []string) (removed bool, err error) {
	var errs []error
	for _, p := range received {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if dir == "" {
		return false, errors.Join(errs...)
	}
	if own {
		errs = append(errs, os.RemoveAll(dir))
		return errors.Join(errs...) == nil, errors.Join(errs...)
	}
	// Carpeta elegida por el usuario: puede tener archivos suyos.
	var dirs []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
		case d.IsDir():
			dirs = append(dirs, p)
		case strings.HasSuffix(d.Name(), partSuffix):
			os.Remove(p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- { // de las más hondas a dir
		os.Remove(dirs[i]) // solo borra las vacías
	}
	_, statErr := os.Stat(dir)
	return errors.Is(statErr, fs.ErrNotExist), errors.Join(errs...)
}
