//go:build mage

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// Default: "go tool mage" sin target muestra la lista con Help.
var Default = Help

// Help muestra los targets con sus descripciones en columna, cortadas al ancho de la consola.
func Help() error {
	// La lista la arma mage (mismo binario con MAGEFILE_LIST): así salen
	// exactamente sus targets y descripciones, y aquí solo se acomodan.
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "MAGEFILE_LIST=1")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("lista de targets: %w", err)
	}

	var header []string
	var names, descs []string
	inTargets := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "Targets:":
			inTargets = true
		case !inTargets:
			header = append(header, line)
		case strings.HasPrefix(line, "  "):
			name, desc, _ := strings.Cut(strings.TrimSpace(line), " ")
			names = append(names, strings.TrimSuffix(name, "*"))
			descs = append(descs, strings.TrimSpace(desc))
		}
		// Se omite la nota "* default target": el default es este mismo.
	}
	if err := sc.Err(); err != nil {
		return err
	}

	nameWidth := 0
	for _, n := range names {
		nameWidth = max(nameWidth, utf8.RuneCountInString(n))
	}
	const indent = 2
	nameWidth += 4
	// Una columna menos que la consola: escribir en la última hace que la
	// consola de Windows salte de línea por su cuenta.
	descWidth := max(consoleWidth()-1-indent-nameWidth, 30)
	pad := strings.Repeat(" ", indent+nameWidth)

	for _, l := range header {
		fmt.Println(l)
	}
	fmt.Println("Targets:")
	for i, n := range names {
		for j, l := range wrap(descs[i], descWidth) {
			if j == 0 {
				fmt.Printf("%s%-*s%s\n", strings.Repeat(" ", indent), nameWidth, n, l)
			} else {
				fmt.Printf("%s%s\n", pad, l)
			}
		}
	}
	fmt.Println("\nMás detalle de un target: go tool mage -h <target>")
	return nil
}

// consoleWidth es el ancho de la consola; sin consola (salida redirigida),
// COLUMNS o 100.
func consoleWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	var w int
	if _, err := fmt.Sscan(os.Getenv("COLUMNS"), &w); err == nil && w > 0 {
		return w
	}
	return 100
}

// wrap corta s en renglones de hasta width caracteres, entre palabras (una
// palabra más larga que width queda sola en su renglón).
func wrap(s string, width int) []string {
	var lines []string
	var cur strings.Builder
	n := 0
	for _, word := range strings.Fields(s) {
		wn := utf8.RuneCountInString(word)
		if n > 0 && n+1+wn > width {
			lines = append(lines, cur.String())
			cur.Reset()
			n = 0
		}
		if n > 0 {
			cur.WriteByte(' ')
			n++
		}
		cur.WriteString(word)
		n += wn
	}
	if n > 0 || len(lines) == 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
