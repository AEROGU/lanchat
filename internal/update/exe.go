package update

import (
	"archive/zip"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// maxExe acota el .exe al descomprimirlo (pesa unos 17 MB).
	maxExe = 200 << 20
	// newSuffix y oldSuffix nombran, junto al ejecutable, el nuevo mientras se
	// descomprime y el anterior hasta que la versión nueva lo borra.
	newSuffix = ".new"
	oldSuffix = ".old"
)

// ZipName es el zip de Windows de una versión.
func ZipName(version string) string { return "LanChat-" + version + ".zip" }

// APKName es el APK de Android de una versión.
func APKName(version string) string { return "LanChat-" + version + ".apk" }

// ExeName es el ejecutable del zip para la arquitectura goarch: el de 32 bits
// en Windows de 32 bits, el de 64 en los demás.
func ExeName(goarch string) string {
	if goarch == "386" {
		return "LanChat_x86.exe"
	}
	return "LanChat.exe"
}

// peMachine es el tipo de máquina de un .exe de cada arquitectura.
var peMachine = map[string]uint16{
	"386":   pe.IMAGE_FILE_MACHINE_I386,
	"amd64": pe.IMAGE_FILE_MACHINE_AMD64,
}

// Install reemplaza el ejecutable exe por el que trae el zip para goarch.
// Windows no deja sobrescribir un .exe en uso, pero sí cambiarle el nombre:
// exe pasa a exe.old (lo borra RemoveOld al abrir la versión nueva) y el
// nuevo toma su lugar. Si algo falla, exe queda como estaba.
func Install(zipPath, exe, goarch string) error {
	tmp := exe + newSuffix
	if err := extractExe(zipPath, ExeName(goarch), tmp, goarch); err != nil {
		os.Remove(tmp)
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("LanChat no tiene permiso para escribir en su carpeta (%s); actualízalo a mano desde %s",
				filepath.Dir(exe), ReleasesURL)
		}
		return err
	}
	old := exe + oldSuffix
	os.Remove(old) // de una actualización anterior que no llegó a borrarlo
	if err := rename(exe, old); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("no se pudo apartar el ejecutable actual: %w", err)
	}
	if err := rename(tmp, exe); err != nil {
		rename(old, exe)
		os.Remove(tmp)
		return fmt.Errorf("no se pudo poner el ejecutable nuevo: %w", err)
	}
	return nil
}

// Rollback deshace Install: vuelve a poner el ejecutable anterior (si la
// versión nueva no pudo arrancar).
func Rollback(exe string) error {
	if _, err := os.Stat(exe + oldSuffix); err != nil {
		return err
	}
	os.Remove(exe)
	return rename(exe+oldSuffix, exe)
}

// rename reintenta unos segundos: el antivirus suele revisar un .exe recién
// escrito y mientras tanto Windows no deja cambiarle el nombre.
func rename(from, to string) error {
	var err error
	for range 12 {
		if err = os.Rename(from, to); err == nil || errors.Is(err, fs.ErrNotExist) {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return err
}

// RemoveOld borra el ejecutable que dejó la actualización anterior. La
// versión anterior puede tardar un momento en soltarlo al cerrarse.
func RemoveOld(exe string) {
	old := exe + oldSuffix
	for range 20 {
		err := os.Remove(old)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// extractExe copia de zipPath el ejecutable name a dst y comprueba que sea un
// .exe de la arquitectura goarch.
func extractExe(zipPath, name, dst, goarch string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("el zip descargado no se puede abrir: %w", err)
	}
	defer zr.Close()
	var entry *zip.File
	for _, f := range zr.File {
		if strings.EqualFold(path.Base(f.Name), name) && !f.FileInfo().IsDir() {
			entry = f
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("el zip de la versión nueva no trae %s", name)
	}
	if entry.UncompressedSize64 > maxExe {
		return fmt.Errorf("%s del zip es demasiado grande", name)
	}
	src, err := entry.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(src, maxExe))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return checkExe(dst, goarch)
}

// checkExe comprueba que path sea un ejecutable de Windows para goarch: así
// nunca se pone uno de 64 bits en un Windows de 32.
func checkExe(path, goarch string) error {
	f, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("el ejecutable nuevo no es válido: %w", err)
	}
	defer f.Close()
	if want, ok := peMachine[goarch]; ok && f.Machine != want {
		return fmt.Errorf("el ejecutable nuevo es para otra arquitectura (0x%x, se esperaba 0x%x)", f.Machine, want)
	}
	return nil
}
