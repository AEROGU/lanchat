package ui

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// wipeConfirmation es lo que el usuario debe escribir para borrar todos sus datos.
const wipeConfirmation = "BORRAR"

// handleExport descarga una copia de la base de datos.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	tmp, err := os.MkdirTemp("", "lanchat-export-")
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, "lanchat.db")
	if err := s.b.ExportData(r.Context(), path); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close() // antes que RemoveAll: en Windows no se borra un archivo abierto

	name := "LanChat-" + s.b.Self().Hostname + "-" + time.Now().Format("2006-01-02") + ".db"
	h := w.Header()
	h.Set("Content-Type", "application/vnd.sqlite3")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	h.Set("Cache-Control", "no-store")
	if fi, err := f.Stat(); err == nil {
		http.ServeContent(w, r, name, fi.ModTime(), f)
		return
	}
	io.Copy(w, f)
}

// handleDeleteConversation borra la conversación con un contacto.
func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer string `json:"peer"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.b.DeleteConversation(r.Context(), req.Peer); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	s.hub.broadcast("cleared", map[string]string{"peer": req.Peer})
	s.publishContact(r.Context(), req.Peer)
	s.unreadChanged(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// deleteRoom borra los mensajes de una sala (y la sala, si este equipo ya salió).
func (s *Server) deleteRoom(r *http.Request, id string) error {
	ctx := r.Context()
	if err := s.b.DeleteRoomConversation(ctx, id); err != nil {
		return err
	}
	_, exists, err := s.b.Room(ctx, id)
	if err != nil {
		return err
	}
	s.hub.broadcast("cleared", map[string]any{"room": id, "removed": !exists})
	if exists {
		s.publishRoom(ctx, id)
	}
	s.unreadChanged(ctx)
	return nil
}

// handleWipe borra todos los datos del usuario.
func (s *Server) handleWipe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.Confirm != wipeConfirmation {
		s.fail(w, http.StatusBadRequest, errors.New(`escribe "`+wipeConfirmation+`" para confirmar`))
		return
	}
	if err := s.b.WipeData(r.Context()); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	s.setPresence(false, "")
	s.hub.broadcast("reload", struct{}{})
	s.unreadChanged(r.Context())
	w.WriteHeader(http.StatusNoContent)
}
