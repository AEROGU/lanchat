package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AEROGU/lanchat/internal/protocol"
)

func TestLoadCreatesDefaultsAndKeepsID(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.UDPPort != protocol.DefaultUDPPort || c.HTTPPort != protocol.DefaultHTTPPort {
		t.Fatalf("valores por defecto incorrectos: %+v", c)
	}

	c.Name = "Recepción"
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != c.ID || again.Name != "Recepción" {
		t.Errorf("no se conservó la configuración: %+v", again)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"json roto":       `{`,
		"puerto inválido": `{"udp_port": 70000}`,
		"nombre largo":    `{"name": "` + strings.Repeat("a", protocol.MaxNameLen+1) + `"}`,
		"id largo":        `{"id": "` + strings.Repeat("a", protocol.MaxIDLen+1) + `"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Error("debía fallar")
			}
		})
	}
}
