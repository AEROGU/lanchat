package ui

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/AEROGU/lanchat/internal/i18n"
)

// photoPath recibe la imagen para la foto de perfil (multipart/form-data, en
// el campo "photo").
const photoPath = "/api/photo"

// maxPhotoUpload acota la imagen que se sube como foto de perfil: luego se
// reduce a thumb.AvatarSide, pero llega completa (una foto del teléfono).
const maxPhotoUpload = 30 << 20

// handlePhoto entrega la foto de perfil guardada de un equipo (también la
// propia). La página pide ?id=…&v=<identificador>: la dirección cambia con
// la foto, así que se puede guardar en caché.
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	_, data, ok, err := s.b.Photo(r.Context(), r.URL.Query().Get("id"))
	if err != nil || !ok {
		http.Error(w, i18n.T("app.noPhoto"), http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(data)
}

// handleSetPhoto pone como foto de perfil la imagen subida.
func (s *Server) handleSetPhoto(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoUpload)
	f, _, err := r.FormFile("photo")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			err = i18n.E("photo.tooBig", "max", maxPhotoUpload>>20)
		}
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.b.SetPhoto(r.Context(), bytes.NewReader(data)); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	s.selfChanged(w)
}

// handleRemovePhoto quita la foto de perfil.
func (s *Server) handleRemovePhoto(w http.ResponseWriter, r *http.Request) {
	if err := s.b.RemovePhoto(r.Context()); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	s.selfChanged(w)
}

// selfChanged responde con este equipo y avisa a las demás ventanas.
func (s *Server) selfChanged(w http.ResponseWriter) {
	self := toSelfJSON(s.b.Self())
	s.hub.broadcast("self", self)
	writeJSON(w, http.StatusOK, self)
}
