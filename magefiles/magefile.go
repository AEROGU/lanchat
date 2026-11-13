//go:build mage

// Tareas de compilación de LanChat (ver README.md).
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	module       = "github.com/AEROGU/lanchat"
	mainPkg      = "./cmd/lanchat"
	distDir      = "dist"
	exeName      = "lanchat.exe"
	exe32Name    = "lanchat-32bits.exe"
	debugExeName = "lanchat-debug.exe"
	guiLdflags   = "-s -w -H=windowsgui"
)

// winArch es una arquitectura de Windows para la que se compila LanChat.
type winArch struct {
	goarch    string // GOARCH; también nombra su archivo de recursos
	exe       string // ejecutable en dist
	zipSuffix string // sufijo de su zip en dist
}

var (
	arch64    = winArch{goarch: "amd64", exe: exeName}
	arch32    = winArch{goarch: "386", exe: exe32Name, zipSuffix: "-32bits"}
	winArches = []winArch{arch64, arch32}
)

// Build compila dist/lanchat.exe para Windows de 64 bits, sin consola, con la
// versión de git y su ícono.
func Build() error {
	mg.Deps(Resources)
	return build(arch64, exeName, guiLdflags)
}

// Build32 compila dist/lanchat-32bits.exe, igual que Build pero para Windows
// de 32 bits (Windows 10 o posterior, como pide Go).
func Build32() error {
	mg.Deps(Resources)
	return build(arch32, exe32Name, guiLdflags)
}

// Debug compila dist/lanchat-debug.exe: igual que Build pero con consola,
// para ver el registro (-debug) o usar el modo -console.
func Debug() error {
	mg.Deps(Resources)
	return build(arch64, debugExeName, "")
}

func build(a winArch, name, extraLdflags string) error {
	v := appVersion()
	ldflags := strings.TrimSpace(fmt.Sprintf("%s -X %s/internal/version.App=%s", extraLdflags, module, v))
	env := map[string]string{"CGO_ENABLED": "0", "GOOS": "windows", "GOARCH": a.goarch}
	out := filepath.Join(distDir, name)
	if err := sh.RunWithV(env, "go", "build", "-trimpath", "-ldflags", ldflags, "-o", out, mainPkg); err != nil {
		return err
	}
	fmt.Printf("%s (versión %s)\n", out, v)
	return nil
}

// Test ejecuta todas las pruebas.
func Test() error {
	return sh.RunV("go", "test", "./...")
}

// Race ejecuta las pruebas con el detector de carreras (requiere gcc para cgo).
func Race() error {
	return sh.RunWithV(map[string]string{"CGO_ENABLED": "1"}, "go", "test", "-race", "./...")
}

// Lint revisa formato (gofmt), go vet y staticcheck.
func Lint() error {
	files, err := sh.Output("gofmt", "-l", ".")
	if err != nil {
		return err
	}
	if files != "" {
		return fmt.Errorf("archivos sin gofmt (corre: gofmt -w .):\n%s", files)
	}
	return errors.Join(
		sh.RunV("go", "vet", "./..."),
		sh.RunV("go", "tool", "staticcheck", "./..."),
	)
}

// Vuln busca vulnerabilidades conocidas en el código y sus librerías
// (govulncheck; necesita internet para consultar la base de datos de Go).
func Vuln() error {
	return sh.RunV("go", "tool", "govulncheck", "./...")
}

// Check corre Lint, Portable y Test; úsalo antes de cada commit.
func Check() {
	mg.SerialDeps(Lint, Portable, Test)
}

// Clean borra la carpeta dist.
func Clean() error {
	return os.RemoveAll(distDir)
}

// Version muestra la versión que tendría el ejecutable.
func Version() {
	fmt.Println(appVersion())
}

// appVersion sale de git: "1.2.0" si el commit tiene la etiqueta v1.2.0,
// "1.2.0-3-gabc1234" si hay 3 commits después de la etiqueta, y "-dirty" si
// hay cambios sin commit. Sin etiquetas devuelve el hash corto.
func appVersion() string {
	v, err := sh.Output("git", "describe", "--tags", "--always", "--dirty")
	if err != nil || v == "" {
		return "dev"
	}
	return strings.TrimPrefix(v, "v")
}
