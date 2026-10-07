// Package config guarda la configuración local de LanChat en config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/AEROGU/lanchat/internal/ids"
)

const (
	DefaultUDPPort  = 50000
	DefaultHTTPPort = 50001
	fileName        = "config.json"
)

type Config struct {
	// ID identifica a esta instalación aunque cambie la IP o el hostname.
	ID string `json:"id"`
	// Name es el nombre que el usuario eligió para que lo vean los demás (opcional).
	Name     string `json:"name"`
	UDPPort  int    `json:"udp_port"`
	HTTPPort int    `json:"http_port"`
	// ManualPeers son equipos de otras subredes: "ip", "ip:puerto" o "hostname".
	ManualPeers []string `json:"manual_peers"`
}

// DefaultDir devuelve %APPDATA%\LanChat (o su equivalente en otros sistemas).
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "LanChat"), nil
}

// Load lee la configuración de dir. Si no existe la crea con valores por defecto
// y un ID nuevo.
func Load(dir string) (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("config.json inválido: %w", err)
		}
	}

	changed := false
	if c.ID == "" {
		c.ID = ids.New()
		changed = true
	}
	if c.UDPPort == 0 {
		c.UDPPort = DefaultUDPPort
		changed = true
	}
	if c.HTTPPort == 0 {
		c.HTTPPort = DefaultHTTPPort
		changed = true
	}
	if c.ManualPeers == nil {
		c.ManualPeers = []string{}
		changed = true
	}
	if changed {
		if err := c.Save(dir); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Save escribe la configuración de forma atómica.
func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, fileName+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, fileName))
}
