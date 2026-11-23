package chat

import (
	"net/http"

	"github.com/AEROGU/lanchat/internal/i18n"
	"github.com/AEROGU/lanchat/internal/peer"
)

// checkSender comprueba que quien hizo la petición (por su identidad TLS) es
// el equipo from. Si from no tiene huella fijada, se aceptará la presentada
// (confianza en el primer uso). Si no coincide, ya respondió 403.
func (s *Service) checkSender(w http.ResponseWriter, r *http.Request, from string) (fp string, ok bool) {
	fp = peer.ClientFingerprint(r)
	if fp == "" {
		http.Error(w, i18n.T("chat.tlsRequired"), http.StatusForbidden)
		return "", false
	}
	known, found, err := s.store.Peer(r.Context(), from)
	if err != nil {
		http.Error(w, i18n.T("err.internal"), http.StatusInternalServerError)
		return "", false
	}
	if found && known.Fingerprint != "" && known.Fingerprint != fp {
		s.log.Warn("mensaje rechazado: la identidad no coincide con la del remitente", "from", from)
		http.Error(w, i18n.T("chat.identityMismatch"), http.StatusForbidden)
		return "", false
	}
	return fp, true
}
