//go:build mage

package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// emojiVersion es la versión de Emoji de Unicode de la que sale la lista.
	emojiVersion = "16.0"
	emojiSource  = "https://unicode.org/Public/emoji/" + emojiVersion + "/emoji-test.txt"
	// emojiFile es el módulo que carga el selector de emojis de la página.
	emojiFile = "internal/ui/web/emoji.js"
	// Los tonos de piel (modificadores) multiplican la lista por seis: no se incluyen.
	skinToneFirst, skinToneLast = 0x1F3FB, 0x1F3FF
)

// emojiGroups traduce los grupos de emoji-test.txt, en su orden; el ícono es
// el de la pestaña. "Component" (tonos de piel y peinados sueltos) se omite.
var emojiGroups = []struct{ source, name, icon string }{
	{"Smileys & Emotion", "Caritas y emociones", "😀"},
	{"People & Body", "Personas y gestos", "👋"},
	{"Animals & Nature", "Animales y naturaleza", "🐶"},
	{"Food & Drink", "Comida y bebida", "🍎"},
	{"Travel & Places", "Viajes y lugares", "🚗"},
	{"Activities", "Actividades", "⚽"},
	{"Objects", "Objetos", "💡"},
	{"Symbols", "Símbolos", "❤"},
	{"Flags", "Banderas", "🏁"},
}

// Emoji regenera internal/ui/web/emoji.js, la lista de emojis del selector,
// desde emoji-test.txt de Unicode (necesita internet). Correrlo al subir
// emojiVersion.
func Emoji() error {
	resp, err := (&http.Client{Timeout: time.Minute}).Get(emojiSource)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", emojiSource, resp.Status)
	}

	lists := map[string][]string{}
	group := ""
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if g, ok := strings.CutPrefix(line, "# group: "); ok {
			group = g
			continue
		}
		// "1F468 200D 1F469 ; fully-qualified # 👨‍👩 E2.0 nombre"
		codes, rest, ok := strings.Cut(line, ";")
		if !ok || strings.HasPrefix(line, "#") || !strings.HasPrefix(strings.TrimSpace(rest), "fully-qualified") {
			continue
		}
		literal, skin := "", false
		for _, hex := range strings.Fields(codes) {
			n, err := strconv.ParseInt(hex, 16, 32)
			if err != nil {
				return fmt.Errorf("código inválido en %q: %w", line, err)
			}
			if n >= skinToneFirst && n <= skinToneLast {
				skin = true
			}
			// Como escape de JavaScript: así el archivo no lleva caracteres
			// invisibles (uniones, selectores) y se puede revisar en un diff.
			literal += fmt.Sprintf(`\u{%X}`, n)
		}
		if !skin {
			lists[group] = append(lists[group], literal)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, `// Emojis del selector, por grupo y en el orden de Unicode (Emoji %s), sin
// tonos de piel. Generado por "go tool mage emoji" desde
// %s
// No editar a mano. Datos © Unicode, Inc., bajo la Unicode License v3
// (packaging/licenses/unicode.txt).
//
// Cada emoji va como escape \u{...} para que el archivo no tenga caracteres
// invisibles; la página descarta los que el sistema no sabe dibujar.

export const EMOJI_GROUPS = [
`, emojiVersion, emojiSource)
	total := 0
	for _, g := range emojiGroups {
		list := lists[g.source]
		if len(list) == 0 {
			return fmt.Errorf("el grupo %q no trae emojis: ¿cambió el formato de emoji-test.txt?", g.source)
		}
		total += len(list)
		fmt.Fprintf(&b, "  {\n    name: %q,\n    icon: %q,\n    list: [\n", g.name, g.icon)
		for i := 0; i < len(list); i += emojiPerLine {
			fmt.Fprintf(&b, "      \"%s\",\n", strings.Join(list[i:min(i+emojiPerLine, len(list))], `", "`))
		}
		b.WriteString("    ],\n  },\n")
	}
	b.WriteString("];\n")
	if err := os.WriteFile(emojiFile, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %d emojis en %d grupos (Emoji %s)\n", emojiFile, total, len(emojiGroups), emojiVersion)
	return nil
}

// emojiPerLine: emojis por renglón del archivo generado.
const emojiPerLine = 8
