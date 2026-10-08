//go:build mage

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/magefile/mage/sh"
)

const (
	mobilePkg = "./mobile"
	// aarPath es donde la app de Android (carpeta android/) espera la librería.
	aarPath = "android/app/libs/lanchat.aar"
	// androidTargets: teléfonos de 64 y 32 bits y el emulador x86_64.
	androidTargets = "android/arm64,android/arm,android/amd64"
	// androidAPI es la versión mínima de Android (24 = Android 7.0).
	androidAPI = "24"
	// javaPkg antecede al paquete Go: la clase queda io.github.aerogu.lanchat.mobile.Mobile.
	javaPkg = "io.github.aerogu.lanchat"
)

// Portable comprueba que el núcleo y el paquete mobile compilen para Android
// (no necesita el SDK); lo corre check para no romper la app sin notarlo.
func Portable() error {
	env := map[string]string{"GOOS": "android", "GOARCH": "arm64", "CGO_ENABLED": "0"}
	return sh.RunWithV(env, "go", "vet", "./internal/...", mobilePkg)
}

// Android compila android/app/libs/lanchat.aar con gomobile, para la app de
// Android. Requiere el SDK y el NDK de Android y gomobile (ver docs/ANDROID.md).
func Android() error {
	if _, err := exec.LookPath("gomobile"); err != nil {
		return errors.New("falta gomobile: go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init (ver docs/ANDROID.md)")
	}
	if os.Getenv("ANDROID_HOME") == "" && os.Getenv("ANDROID_SDK_ROOT") == "" {
		return errors.New("falta ANDROID_HOME: la ruta del SDK de Android (en Android Studio: Settings > Android SDK)")
	}
	if err := os.MkdirAll(filepath.Dir(aarPath), 0o755); err != nil {
		return err
	}
	v := appVersion()
	ldflags := fmt.Sprintf("-s -w -X %s/internal/version.App=%s", module, v)
	if err := sh.RunV("gomobile", "bind", "-target="+androidTargets, "-androidapi", androidAPI,
		"-javapkg", javaPkg, "-ldflags", ldflags, "-o", aarPath, mobilePkg); err != nil {
		return err
	}
	fmt.Printf("%s (versión %s)\n", aarPath, v)
	return nil
}
