//go:build !windows

package i18n

import (
	"os"
	"strings"
)

// systemLanguages devuelve los idiomas del sistema según las variables de entorno de
// POSIX (LANGUAGE, LC_ALL, LC_MESSAGES, LANG). En Android no hay: el idioma
// lo informa la app (mobile.Start).
func systemLanguages() []string {
	var out []string
	if l := os.Getenv("LANGUAGE"); l != "" {
		out = append(out, strings.Split(l, ":")...)
	}
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if l := os.Getenv(v); l != "" && l != "C" && l != "POSIX" {
			out = append(out, l)
		}
	}
	return out
}
