//go:build mage

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	mobilePkg = "./mobile"
	// aarPath es donde la app de Android (carpeta android/) espera la librería.
	aarPath = "android/app/libs/lanchat.aar"
	// androidTargets: teléfonos de 64 y 32 bits. Sin x86_64: ahí la libc de
	// modernc.org/sqlite usa syscalls que Android prohíbe (docs/ANDROID.md).
	androidTargets = "android/arm64,android/arm"
	// apkBuilt es lo que deja "gradlew assembleDebug"; Apk lo copia a dist.
	apkBuilt = "android/app/build/outputs/apk/debug/app-debug.apk"
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
	if _, err := exec.LookPath("javac"); err != nil {
		return errors.New("falta javac en el PATH: agrega %JAVA_HOME%\\bin (el JDK de Android Studio, ver docs/ANDROID.md)")
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

// Apk compila dist/LanChat-<versión>.apk, para instalar a mano en el teléfono
// ("instalar apps desconocidas"). Va firmado con la clave de depuración de
// Android: sirve para pruebas; la versión publicada usará una clave propia
// (docs/ANDROID.md, punto 6).
func Apk() error {
	mg.Deps(Android)
	v := appVersion()
	gradlew := "gradlew"
	if runtime.GOOS == "windows" {
		gradlew = "gradlew.bat"
	}
	gradlew, err := filepath.Abs(filepath.Join("android", gradlew))
	if err != nil {
		return err
	}
	cmd := exec.Command(gradlew, "assembleDebug", "--console=plain",
		"-Planchat.versionName="+v, fmt.Sprintf("-Planchat.versionCode=%d", androidVersionCode(v)))
	cmd.Dir = "android"
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return err
	}
	out := filepath.Join(distDir, fmt.Sprintf("%s-%s.apk", productName, v))
	if err := sh.Copy(out, apkBuilt); err != nil {
		return err
	}
	fmt.Printf("%s (versión %s)\n", out, v)
	return nil
}

// androidVersionCode convierte la versión de git en el número que Android
// compara para permitir una actualización (debe crecer siempre):
// 1.2.3-45-gabc → 1_02_003_045. Sin etiqueta de versión queda en 1 (el mínimo).
func androidVersionCode(v string) int {
	n := numericVersion(v)
	return max(1, int(n[0])*10_000_000+int(n[1])*100_000+int(n[2])*1_000+min(int(n[3]), 999))
}
