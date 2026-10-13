package ui

import "net/http"

// handleDeleteMessage elimina para todos un mensaje propio. El cambio llega
// a las ventanas como un evento "message" (ver publishChat).
func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	m, err := s.b.DeleteForEveryone(r.Context(), req.ID)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, toMessageJSON(m))
}

// handleReact pone (o quita, con emoji vacío) la reacción de este equipo.
func (s *Server) handleReact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Emoji string `json:"emoji"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	m, err := s.b.React(r.Context(), req.ID, req.Emoji)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, toMessageJSON(m))
}
