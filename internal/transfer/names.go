package transfer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/AEROGU/lanchat/internal/store"
)

const (
	// maxSavedNameBytes deja margen para " (123)" y la extensión .lanchat-part
	// dentro del límite de 255 de Windows.
	maxSavedNameBytes = 200
	defaultFileName   = "archivo"
)

// windowsReserved son nombres que Windows no permite como archivo, con o sin extensión.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// safeFileName adapta el nombre recibido a las reglas de Windows: sin rutas,
// sin caracteres prohibidos, sin nombres reservados ni puntos finales.
func safeFileName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimRight(name, ". "))
	if name == "" {
		name = defaultFileName
	}
	stem, _, _ := strings.Cut(name, ".")
	if windowsReserved[strings.ToUpper(strings.TrimSpace(stem))] {
		name = "_" + name
	}
	return truncateName(name, maxSavedNameBytes)
}

// truncateName recorta a max bytes conservando la extensión y sin cortar
// caracteres UTF-8 a la mitad.
func truncateName(name string, max int) string {
	if len(name) <= max {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > max/2 {
		ext = ""
	}
	return truncateUTF8(name[:len(name)-len(ext)], max-len(ext)) + ext
}

// uniquePath devuelve dir\name, o "name (1).ext", "name (2).ext"… si ya existe.
func uniquePath(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 10000; i++ {
		cand := name
		if i > 0 {
			cand = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		p := filepath.Join(dir, cand)
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return p, nil
		}
	}
	return "", fmt.Errorf("demasiados archivos llamados %q en %s", name, dir)
}

// humanSize muestra un tamaño como "2.3 MB".
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// offerSummary es el texto del mensaje que lleva la oferta; lo ven también
// las versiones que solo muestran texto (y la consola). Cada carpeta cuenta
// como un elemento: "📁 Proyecto (12 archivos, 30.0 MB)".
func offerSummary(files []store.TransferFile, total int64) string {
	const maxListed = 3
	var entries []string
	seen := map[string]bool{}
	for _, f := range files {
		if f.Dir == "" {
			entries = append(entries, f.Name)
		} else if root := rootOf(f.Dir); !seen[root] {
			seen[root] = true
			entries = append(entries, root+"/")
		}
	}
	switch {
	case len(entries) == 1 && len(seen) == 1:
		return fmt.Sprintf("📁 %s (%d archivos, %s)", strings.TrimSuffix(entries[0], "/"), len(files), humanSize(total))
	case len(entries) == 1:
		return fmt.Sprintf("📎 %s (%s)", entries[0], humanSize(total))
	}
	listed := entries
	if len(listed) > maxListed {
		listed = listed[:maxListed]
	}
	s := fmt.Sprintf("📎 %d elementos (%s): %s", len(entries), humanSize(total), strings.Join(listed, ", "))
	if len(entries) > maxListed {
		s += "…"
	}
	return s
}

// truncateUTF8 recorta s a max bytes sin cortar un carácter a la mitad.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
