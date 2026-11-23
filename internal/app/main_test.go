package app

import (
	"os"
	"testing"

	"github.com/AEROGU/lanchat/internal/i18n"
)

// TestMain fija el idioma: varias pruebas comparan textos en español, y
// New tomaría el del sistema (en GitHub Actions, inglés).
func TestMain(m *testing.M) {
	os.Setenv(i18n.EnvOverride, "es")
	os.Exit(m.Run())
}
