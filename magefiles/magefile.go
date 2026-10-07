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
	debugExeName = "lanchat-debug.exe"
)

// Build compila dist/lanchat.exe para Windows, sin consola, con la versión de
// git y su ícono.
func Build() error {
	mg.Deps(Resources)
	return build(exeName, "-s -w -H=windowsgui")
}

// Debug compila dist/lanchat-debug.exe: igual pero con consola, para ver el
// registro (-debug) o usar el modo -console.
func Debug() error {
	mg.Deps(Resources)
	return build(debugExeName, "")
}

func build(name, extraLdflags string) error {
	v := appVersion()
	ldflags := strings.TrimSpace(fmt.Sprintf("%s -X %s/internal/version.App=%s", extraLdflags, module, v))
	env := map[string]string{"CGO_ENABLED": "0", "GOOS": "windows", "GOARCH": "amd64"}
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

// Check corre Lint y Test; úsalo antes de cada commit.
func Check() {
	mg.SerialDeps(Lint, Test)
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
