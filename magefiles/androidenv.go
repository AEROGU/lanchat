//go:build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// findAndroidTools completa, solo para este proceso y los que lance (gomobile
// y Gradle), las variables que Android Studio no crea al instalarse:
// ANDROID_HOME, ANDROID_NDK_HOME y JAVA_HOME (más su bin en el PATH). Busca
// en las rutas donde Android Studio instala el SDK y su JDK; una variable ya
// definida se respeta.
func findAndroidTools() {
	home, _ := os.UserHomeDir()
	if os.Getenv("ANDROID_HOME") == "" && os.Getenv("ANDROID_SDK_ROOT") == "" {
		setFound("ANDROID_HOME", firstDir(
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Android", "Sdk"), // Windows
			filepath.Join(home, "Android", "Sdk"),                      // Linux
			filepath.Join(home, "Library", "Android", "sdk"),           // macOS
		))
	}
	sdk := cmpOr(os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_SDK_ROOT"))
	if os.Getenv("ANDROID_NDK_HOME") == "" && sdk != "" {
		setFound("ANDROID_NDK_HOME", newestNDK(filepath.Join(sdk, "ndk")))
	}
	if _, err := exec.LookPath("javac"); err != nil && os.Getenv("JAVA_HOME") == "" {
		jbr := firstDir(
			filepath.Join(os.Getenv("ProgramFiles"), "Android", "Android Studio", "jbr"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Android Studio", "jbr"),
			"/opt/android-studio/jbr",
			"/Applications/Android Studio.app/Contents/jbr/Contents/Home",
		)
		if setFound("JAVA_HOME", jbr) {
			os.Setenv("PATH", filepath.Join(jbr, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
}

// setFound define la variable si se encontró la ruta, y lo avisa.
func setFound(name, path string) bool {
	if path == "" {
		return false
	}
	os.Setenv(name, path)
	fmt.Printf("%s no estaba definida; se usa %s\n", name, path)
	return true
}

func firstDir(paths ...string) string {
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}

// newestNDK devuelve la versión más nueva instalada en dir ("30.0.16248370").
func newestNDK(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	if len(versions) == 0 {
		return ""
	}
	slices.SortFunc(versions, compareVersions)
	return filepath.Join(dir, versions[len(versions)-1])
}

// compareVersions compara "27.1.12297006" con "30.0.16248370" número por número.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

func cmpOr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
