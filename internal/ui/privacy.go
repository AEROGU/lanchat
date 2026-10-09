package ui

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	name := s.exportName()
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

// handleSaveExport guarda la copia directamente en la carpeta de archivos
// recibidos (Android: Descargas/LanChat): la WebView no descarga archivos, y
// así no queda ninguna copia temporal. Solo sin escritorio (Server.Shell).
func (s *Server) handleSaveExport(w http.ResponseWriter, r *http.Request) {
	if s.Shell == nil {
		s.fail(w, http.StatusNotFound, errors.New("en este equipo la copia se descarga"))
		return
	}
	dir := s.b.DownloadDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	path, err := freePath(dir, s.exportName())
	if err == nil {
		err = s.b.ExportData(r.Context(), path)
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": filepath.Base(path), "dir": dir})
}

// exportName: LanChat-<equipo>-<fecha>.db, sin caracteres que no admita un
// nombre de archivo (en Android el nombre del teléfono lo escribe el usuario).
func (s *Server) exportName() string {
	host := strings.Map(func(r rune) rune {
		if r < ' ' || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, s.b.Self().Hostname)
	return "LanChat-" + host + "-" + time.Now().Format("2006-01-02") + ".db"
}

// freePath devuelve dir/name, o "nombre (n).ext" si ya existe.
func freePath(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for n := 1; n < 1000; n++ {
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return p, nil
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, n, ext))
	}
	return "", fmt.Errorf("demasiadas copias en %s", dir)
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
