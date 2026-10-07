//go:build mage

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/magefile/mage/sh"
)

const noticesName = "THIRD_PARTY_NOTICES.txt"

var licenseFileRe = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice)(\.(txt|md))?$`)

// Notices genera dist/THIRD_PARTY_NOTICES.txt con las licencias de Go y de
// cada librería incluida en el .exe (las licencias BSD, MIT y Apache exigen
// acompañar el binario con sus avisos).
func Notices() error {
	env := map[string]string{"CGO_ENABLED": "0", "GOOS": "windows", "GOARCH": "amd64"}
	list, err := sh.OutputWith(env, "go", "list", "-deps",
		"-f", "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}", mainPkg)
	if err != nil {
		return err
	}
	goroot, err := sh.Output("go", "env", "GOROOT")
	if err != nil {
		return err
	}
	goVersion, _ := sh.Output("go", "env", "GOVERSION")

	type mod struct{ path, version, dir string }
	mods := []mod{{"Go (biblioteca estándar)", goVersion, goroot}}
	seen := map[string]bool{}
	for _, line := range strings.Split(list, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\t")
		if len(parts) != 3 || parts[0] == module || seen[parts[0]] {
			continue
		}
		seen[parts[0]] = true
		mods = append(mods, mod{parts[0], parts[1], parts[2]})
	}
	sort.Slice(mods[1:], func(i, j int) bool { return mods[1+i].path < mods[1+j].path })

	var b strings.Builder
	b.WriteString("LanChat incluye el siguiente software de terceros. Sus licencias\n" +
		"se reproducen a continuación. LanChat se distribuye bajo GPL v3 (LICENSE.txt).\n")
	for _, m := range mods {
		entries, err := os.ReadDir(m.dir)
		if err != nil {
			return fmt.Errorf("%s: %w", m.path, err)
		}
		found := false
		for _, e := range entries {
			if e.IsDir() || !licenseFileRe.MatchString(e.Name()) {
				continue
			}
			text, err := os.ReadFile(filepath.Join(m.dir, e.Name()))
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "\n\n==== %s %s (%s) ====\n\n%s", m.path, m.version, e.Name(), strings.TrimSpace(string(text)))
			found = true
		}
		if !found {
			return fmt.Errorf("%s no tiene archivo de licencia: revisar antes de distribuir", m.path)
		}
		fmt.Printf("%-45s %s\n", m.path, licenseKind(m.dir))
	}
	b.WriteString("\n")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(distDir, noticesName), []byte(b.String()), 0o644)
}

// licenseKind adivina el tipo de licencia para revisar la compatibilidad.
func licenseKind(dir string) string {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !licenseFileRe.MatchString(e.Name()) {
			continue
		}
		text, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		s := string(text)
		switch {
		case strings.Contains(s, "Apache License"):
			return "Apache 2.0"
		case strings.Contains(s, "Permission is hereby granted, free of charge"):
			return "MIT"
		case strings.Contains(s, "Redistribution and use in source and binary forms"):
			return "BSD"
		case strings.Contains(s, "Permission to use, copy, modify, and/or distribute"):
			return "ISC / 0BSD"
		case strings.Contains(s, "GNU GENERAL PUBLIC LICENSE"):
			return "GPL"
		case strings.Contains(s, "Mozilla Public License"):
			return "MPL"
		}
	}
	return "¿? (revisar)"
}
