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

// handleEditMessage cambia el texto de un mensaje propio.
func (s *Server) handleEditMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Body string `json:"body"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	m, err := s.b.EditMessage(r.Context(), req.ID, req.Body)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, toMessageJSON(m))
}

// handleForward reenvía mensajes a contactos y salas. Si falló con algunos
// destinos, responde lo que sí se envió y el error.
func (s *Server) handleForward(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs   []string `json:"ids"`
		Peers []string `json:"peers"`
		Rooms []string `json:"rooms"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	msgs, err := s.b.Forward(r.Context(), req.IDs, req.Peers, req.Rooms)
	if len(msgs) == 0 && err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	out := struct {
		Messages []messageJSON `json:"messages"`
		Error    string        `json:"error,omitempty"`
	}{}
	var jerr error
	if out.Messages, jerr = s.messagesJSON(r.Context(), msgs); jerr != nil {
		s.fail(w, http.StatusInternalServerError, jerr)
		return
	}
	if err != nil {
		out.Error = err.Error()
	}
	writeJSON(w, http.StatusCreated, out)
}
