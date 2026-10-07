package ui

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"

	"github.com/AEROGU/lanchat/internal/platform"
)

// systemJSON describe la integración con Windows que se muestra en Ajustes.
type systemJSON struct {
	Supported bool `json:"supported"`
	// Autostart: LanChat arranca al iniciar sesión.
	Autostart bool `json:"autostart"`
	// Firewall: existe la regla del Firewall de Windows para este ejecutable.
	Firewall bool `json:"firewall"`
}

func systemState() systemJSON {
	exe, err := platform.Executable()
	return systemJSON{
		Supported: runtime.GOOS == "windows",
		Autostart: platform.AutostartEnabled(),
		Firewall:  err == nil && platform.FirewallAllowed(exe),
	}
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, systemState())
}

func (s *Server) handleAutostart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	exe, err := platform.Executable()
	if err == nil {
		err = platform.SetAutostart(req.Enabled, exe, s.AutostartArgs...)
	}
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, systemState())
}

// handleFirewall crea la regla del firewall relanzando LanChat como
// administrador (Windows muestra el aviso de UAC) y espera el resultado.
func (s *Server) handleFirewall(w http.ResponseWriter, r *http.Request) {
	exe, err := platform.Executable()
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	code, err := platform.RunElevated(exe, "-firewall add -elevated")
	switch {
	case errors.Is(err, platform.ErrCanceled):
		err = errors.New("no se aceptó el permiso de administrador; la regla no se creó")
	case err == nil && code != 0:
		err = fmt.Errorf("no se pudo crear la regla (código %d); revisa lanchat.log", code)
	}
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, systemState())
}
