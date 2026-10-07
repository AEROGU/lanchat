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
	"github.com/AEROGU/lanchat/internal/protocol"
)

const (
	appDirName = "LanChat"
	fileName   = "config.json"
)

type Config struct {
	// ID identifica a esta instalación aunque cambie la IP o el hostname.
	ID string `json:"id"`
	// Name es el nombre que el usuario eligió para que lo vean los demás (opcional).
	Name     string `json:"name"`
	UDPPort  int    `json:"udp_port"`
	HTTPPort int    `json:"http_port"`
	// ManualPeers son equipos de otras subredes: "ip", "ip:puerto" o "hostname[:puerto]".
	ManualPeers []string `json:"manual_peers"`
	// DownloadDir es donde se guardan los archivos recibidos ("" = Descargas\LanChat).
	DownloadDir string `json:"download_dir"`
	// SetupDone: ya se aplicaron los ajustes de la primera ejecución (p. ej.
	// activar el inicio con Windows); después manda lo que elija el usuario.
	SetupDone bool `json:"setup_done"`
	// Status y StatusText son el estado elegido por el usuario (protocol.Status*).
	Status     string `json:"status"`
	StatusText string `json:"status_text"`
	// DisableAutoAway desactiva el paso a Ausente por inactividad.
	DisableAutoAway bool `json:"disable_auto_away"`
	// NoReadReceipts: no avisar a los demás cuando se leen sus mensajes.
	NoReadReceipts bool `json:"no_read_receipts"`
}

// DefaultDir devuelve %APPDATA%\LanChat (o su equivalente en otros sistemas).
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appDirName), nil
}

// Load lee la configuración de dir. Si no existe la crea con valores por defecto
// y un ID nuevo.
func Load(dir string) (*Config, error) {
	path := filepath.Join(dir, fileName)
	c := &Config{}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("%s inválido: %w", path, err)
		}
	}

	changed := false
	if c.ID == "" {
		c.ID = ids.New()
		changed = true
	}
	if c.UDPPort == 0 {
		c.UDPPort = protocol.DefaultUDPPort
		changed = true
	}
	if c.HTTPPort == 0 {
		c.HTTPPort = protocol.DefaultHTTPPort
		changed = true
	}
	if c.Status != protocol.NormalizeStatus(c.Status) {
		c.Status = protocol.StatusAvailable
		changed = true
	}
	if c.ManualPeers == nil {
		c.ManualPeers = []string{}
		changed = true
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if changed {
		if err := c.Save(dir); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Config) validate() error {
	if err := protocol.ValidateID(c.ID); err != nil {
		return err
	}
	if err := protocol.ValidateName(c.Name); err != nil {
		return err
	}
	if err := protocol.ValidateStatusText(c.StatusText); err != nil {
		return err
	}
	for _, p := range []int{c.UDPPort, c.HTTPPort} {
		if p < 1 || p > 65535 {
			return fmt.Errorf("puerto %d fuera de rango", p)
		}
	}
	return nil
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
