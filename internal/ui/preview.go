package ui

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/AEROGU/lanchat/internal/thumb"
)

// Vista previa de imágenes en la conversación: la miniatura sale de la base
// (llega con la oferta) y la imagen completa se lee directo del archivo, sin
// copias ni temporales.

var errNoPreview = errors.New("no hay vista previa de este archivo")

// handleThumb entrega la miniatura JPEG de un archivo de una transferencia.
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	id, idx, ok := previewTarget(w, r)
	if !ok {
		return
	}
	b, ok, err := s.b.Thumb(r.Context(), id, idx)
	if err != nil || !ok {
		http.Error(w, errNoPreview.Error(), http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "private, max-age=31536000, immutable") // no cambia
	w.Write(b)
}

// handleView entrega la imagen completa de un archivo de una transferencia:
// uno recibido y terminado, o uno enviado que siga en su lugar. Solo imágenes
// (por extensión y por contenido, nunca SVG), con CSP sandbox.
func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	id, idx, ok := previewTarget(w, r)
	if !ok {
		return
	}
	t, ok, err := s.b.Transfer(r.Context(), id)
	if err != nil || !ok || idx < 0 || idx >= len(t.Files) {
		http.Error(w, errNoPreview.Error(), http.StatusNotFound)
		return
	}
	f := t.Files[idx]
	if (!t.Outgoing && !f.Done) || !thumb.Supported(f.Name) {
		http.Error(w, errNoPreview.Error(), http.StatusNotFound)
		return
	}
	file, err := os.Open(f.Path)
	if err != nil {
		http.Error(w, "el archivo ya no está ahí: se movió o se borró", http.StatusNotFound)
		return
	}
	defer file.Close()
	ctype, ok := imageType(file)
	if !ok {
		http.Error(w, errNoPreview.Error(), http.StatusUnsupportedMediaType)
		return
	}
	fi, err := file.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", fi.ModTime(), file)
}

// previewTarget lee ?id=…&index=… de la petición.
func previewTarget(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	q := r.URL.Query()
	idx, err := strconv.Atoi(q.Get("index"))
	if q.Get("id") == "" || err != nil {
		http.Error(w, "faltan id e index", http.StatusBadRequest)
		return "", 0, false
	}
	return q.Get("id"), idx, true
}

// imageType reconoce el contenido real del archivo: solo JPEG, PNG, GIF y
// WebP (lo que también admite la miniatura). Deja el archivo al principio.
func imageType(f io.ReadSeeker) (string, bool) {
	var head [512]byte
	n, _ := io.ReadFull(f, head[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", false
	}
	ctype := http.DetectContentType(head[:n])
	switch ctype {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return ctype, true
	}
	return "", false
}
