package ui

import (
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/AEROGU/lanchat/internal/i18n"
	"github.com/AEROGU/lanchat/internal/thumb"
)

// Vista previa de imágenes en la conversación: la miniatura sale de la base
// (llega con la oferta) y la imagen completa se lee directo del archivo, sin
// copias ni temporales.

var errNoPreview = i18n.E("preview.none")

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
		http.Error(w, i18n.T("files.moved"), http.StatusNotFound)
		return
	}
	defer file.Close()
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-cache")
	// Un HEIC no lo muestra la página (ni la WebView): va convertido a JPEG.
	if thumb.IsHEIC(file) {
		b, err := thumb.ViewJPEG(file)
		if err != nil {
			http.Error(w, errNoPreview.Error(), http.StatusUnsupportedMediaType)
			return
		}
		h.Set("Content-Type", "image/jpeg")
		w.Write(b)
		return
	}
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
	h.Set("Content-Type", ctype)
	http.ServeContent(w, r, "", fi.ModTime(), file)
}

// previewTarget lee ?id=…&index=… de la petición.
func previewTarget(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	q := r.URL.Query()
	idx, err := strconv.Atoi(q.Get("index"))
	if q.Get("id") == "" || err != nil {
		http.Error(w, i18n.T("preview.missingParams"), http.StatusBadRequest)
		return "", 0, false
	}
	return q.Get("id"), idx, true
}

// imageType reconoce el contenido real del archivo: solo JPEG, PNG, GIF y
// WebP, lo que la página muestra tal cual (el HEIC se convierte antes).
// Deja el archivo al principio.
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
