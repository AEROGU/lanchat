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
	// AskAutostart: la página debe preguntar si se activa el inicio con
	// Windows; AutostartMoved: porque estaba activado para otra ubicación.
	AskAutostart   bool `json:"askAutostart"`
	AutostartMoved bool `json:"autostartMoved"`
	// Firewall: existe la regla del Firewall de Windows para este ejecutable.
	Firewall bool `json:"firewall"`
}

func (s *Server) systemState() systemJSON {
	out := systemJSON{Supported: runtime.GOOS == "windows"}
	exe, err := platform.Executable()
	if err != nil {
		return out
	}
	state := platform.Autostart(exe)
	out.Autostart = state == platform.AutostartOn
	out.AutostartMoved = state == platform.AutostartMoved
	out.AskAutostart = out.Supported && askAutostart(s.AutostartPrompt, state, s.b.SetupDone())
	out.Firewall = platform.FirewallAllowed(exe)
	return out
}

// askAutostart decide si hay que preguntar por el inicio con Windows: la
// primera vez, o si quedó activado para otra ubicación del programa. LanChat
// nunca lo activa ni lo actualiza por su cuenta.
func askAutostart(prompt bool, state platform.AutostartState, setupDone bool) bool {
	switch {
	case !prompt:
		return false
	case state == platform.AutostartMoved:
		return true
	}
	return state == platform.AutostartOff && !setupDone
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.systemState())
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
	// El usuario ya eligió: no se le vuelve a preguntar.
	if err := s.b.MarkSetupDone(); err != nil {
		s.log.Warn("guardando la respuesta del inicio con Windows", "err", err)
	}
	writeJSON(w, http.StatusOK, s.systemState())
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
	writeJSON(w, http.StatusOK, s.systemState())
}
