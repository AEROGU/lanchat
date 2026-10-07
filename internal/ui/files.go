package ui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/AEROGU/lanchat/internal/store"
)

// uploadPath recibe archivos arrastrados a la ventana (multipart/form-data):
// el navegador no da su ruta, así que hay que copiarlos.
const uploadPath = "/api/files/upload"

// errPickerBusy: ya hay un selector de archivos abierto.
var errPickerBusy = errors.New("ya hay una ventana para elegir archivos abierta")

// handlePickFiles abre el selector de archivos de Windows y ofrece lo elegido.
func (s *Server) handlePickFiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer string `json:"peer"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	paths, err := pickFiles()
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if len(paths) == 0 { // el usuario canceló
		w.WriteHeader(http.StatusNoContent)
		return
	}
	m, err := s.b.OfferFiles(r.Context(), req.Peer, paths)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.messageJSON(r.Context(), m))
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	next := func() (string, io.Reader, error) {
		for {
			part, err := mr.NextPart()
			if err != nil {
				return "", nil, err // io.EOF al terminar
			}
			if part.FileName() != "" {
				return part.FileName(), part, nil
			}
		}
	}
	m, err := s.b.Upload(r.Context(), r.URL.Query().Get("peer"), next)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.messageJSON(r.Context(), m))
}

func (s *Server) handleTransferAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	actions := map[string]func(context.Context, string) error{
		"accept": s.b.AcceptTransfer,
		"reject": s.b.RejectTransfer,
		"cancel": s.b.CancelTransfer,
	}
	action, ok := actions[r.PathValue("action")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := action(r.Context(), req.ID); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleOpenFile abre un archivo recibido o lo muestra en su carpeta. Solo
// acepta archivos de transferencias completadas, nunca rutas arbitrarias.
func (s *Server) handleOpenFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		Index  int    `json:"index"`
		Reveal bool   `json:"reveal"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	t, ok, err := s.b.Transfer(r.Context(), req.ID)
	if err != nil || !ok || t.Outgoing || req.Index < 0 || req.Index >= len(t.Files) || !t.Files[req.Index].Done {
		s.fail(w, http.StatusBadRequest, errors.New("el archivo no está disponible"))
		return
	}
	path := t.Files[req.Index].Path
	if _, err := os.Stat(path); err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("el archivo ya no está ahí: se movió o se borró"))
		return
	}
	open := openPath
	if req.Reveal {
		open = revealPath
	}
	if err := open(path); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDownloadDir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir string `json:"dir"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.b.SetDownloadDir(req.Dir); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"downloadDir": s.b.DownloadDir()})
}

func (s *Server) handleOpenDownloadDir(w http.ResponseWriter, r *http.Request) {
	dir := s.b.DownloadDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if err := openPath(dir); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// messageJSON agrega la oferta de archivos si el mensaje lleva una.
func (s *Server) messageJSON(ctx context.Context, m store.Message) messageJSON {
	out := toMessageJSON(m)
	if m.Kind == store.KindFiles {
		if t, ok, err := s.b.Transfer(ctx, m.ID); err == nil && ok {
			tj := toTransferJSON(t)
			out.Transfer = &tj
		}
	}
	return out
}

func (s *Server) messagesJSON(ctx context.Context, msgs []store.Message) ([]messageJSON, error) {
	var ids []string
	for _, m := range msgs {
		if m.Kind == store.KindFiles {
			ids = append(ids, m.ID)
		}
	}
	transfers, err := s.b.TransfersByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]messageJSON, len(msgs))
	for i, m := range msgs {
		out[i] = toMessageJSON(m)
		if t, ok := transfers[m.ID]; ok {
			tj := toTransferJSON(t)
			out[i].Transfer = &tj
		}
	}
	return out, nil
}

// splitMultiSelect interpreta el búfer del selector de Windows: una ruta
// completa, o la carpeta seguida de los nombres, separados por \0 y
// terminados en \0\0.
func splitMultiSelect(buf string) []string {
	buf, _, _ = strings.Cut(buf, "\x00\x00")
	parts := strings.Split(buf, "\x00")
	if len(parts) == 0 || parts[0] == "" {
		return nil
	}
	if len(parts) == 1 {
		return parts
	}
	dir := strings.TrimRight(parts[0], `\`)
	out := make([]string, 0, len(parts)-1)
	for _, name := range parts[1:] {
		out = append(out, dir+`\`+name)
	}
	return out
}
