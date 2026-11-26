package ui

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/AEROGU/lanchat/internal/i18n"
	"github.com/AEROGU/lanchat/internal/store"
)

// shareMaxAge: los .zip para compartir se borran pasado este tiempo; la otra
// app ya los copió (WhatsApp, el correo…) o los vuelve a pedir al compartir.
const shareMaxAge = 24 * time.Hour

// shareProbe: archivos de una oferta enviada que se revisan para saber si
// sigue en el equipo (cada mensaje dibujado los revisa).
const shareProbe = 10

var errNoShare = i18n.E("shell.noShare")

// handleShareFiles pasa archivos recibidos a otras apps: el "Compartir" de
// Android o el portapapeles de Windows (ver shareFiles), todo de una vez:
// los archivos de Indexes tal cual y cada carpeta raíz de Dirs como un .zip.
// Solo acepta archivos de la oferta (ver ready), nunca rutas arbitrarias.
func (s *Server) handleShareFiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string   `json:"id"`
		Indexes []int    `json:"indexes"`
		Dirs    []string `json:"dirs"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	t, ok, err := s.b.Transfer(r.Context(), req.ID)
	if err != nil || !ok || len(req.Indexes)+len(req.Dirs) == 0 {
		s.fail(w, http.StatusBadRequest, i18n.E("files.unavailable"))
		return
	}
	var paths []string
	for _, i := range req.Indexes {
		if i < 0 || i >= len(t.Files) || !ready(t, t.Files[i]) {
			s.fail(w, http.StatusBadRequest, i18n.E("files.unavailable"))
			return
		}
		if _, err := os.Stat(t.Files[i].Path); err != nil {
			s.fail(w, http.StatusBadRequest, i18n.E("files.moved"))
			return
		}
		paths = append(paths, t.Files[i].Path)
	}
	for _, dir := range req.Dirs {
		zipPath, err := s.zipFolder(r.Context(), t, dir)
		if err != nil {
			s.fail(w, http.StatusBadRequest, err)
			return
		}
		paths = append(paths, zipPath)
	}
	if err := s.shareFiles(paths); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ready: el archivo de la oferta se puede compartir. Uno enviado está en el
// equipo desde el principio (si no se borró); uno recibido, al completarse.
func ready(t store.Transfer, f store.TransferFile) bool { return t.Outgoing || f.Done }

// shareable: la oferta tiene algo que compartir. De una enviada se mira si
// sigue en el equipo, porque las copias temporales (lo arrastrado a la
// ventana y todo lo enviado desde Android) se borran al terminar el envío;
// basta el primer archivo que exista. Las copias se borran todas juntas, así
// que de una enviada solo se revisan los primeros shareProbe.
func shareable(t store.Transfer) bool {
	for i, f := range t.Files {
		if !t.Outgoing && f.Done {
			return true
		}
		if t.Outgoing && i < shareProbe {
			if st, err := os.Stat(f.Path); err == nil && st.Mode().IsRegular() {
				return true
			}
		}
	}
	return false
}

// zipFolder comprime los archivos de la carpeta raíz root de una oferta (ver
// ready) en ShareDir/<id>/<root>.zip y devuelve su ruta. Dentro del zip, las
// rutas empiezan en la carpeta raíz.
func (s *Server) zipFolder(ctx context.Context, t store.Transfer, root string) (string, error) {
	var files []store.TransferFile
	for _, f := range t.Files {
		if r, _, _ := strings.Cut(f.Dir, "/"); r == root && ready(t, f) {
			files = append(files, f)
		}
	}
	if (!t.Outgoing && t.Dir == "") || len(files) == 0 || s.ShareDir == "" {
		return "", i18n.E("files.folderUnavailable")
	}
	for _, f := range files {
		if _, err := os.Stat(f.Path); err != nil {
			return "", i18n.E("files.moved")
		}
	}
	cleanShareDir(s.ShareDir, time.Now())
	dir := filepath.Join(s.ShareDir, safeName(t.ID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, safeName(root)+".zip")
	tmp, err := os.CreateTemp(dir, "*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // tras el Rename ya no existe
	if err := writeZip(ctx, tmp, files); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	now := time.Now()
	os.Chtimes(dir, now, now) // la limpieza cuenta desde el último uso
	return dst, nil
}

func writeZip(ctx context.Context, w io.Writer, files []store.TransferFile) error {
	zw := zip.NewWriter(w)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		h := &zip.FileHeader{
			Name:     path.Join(f.Dir, filepath.Base(f.Path)),
			Method:   zip.Deflate,
			Modified: f.ModTime,
		}
		if compressed(f.Path) {
			h.Method = zip.Store // ya comprimido: deflate solo gastaría batería
		}
		if h.Modified.IsZero() {
			if st, err := os.Stat(f.Path); err == nil {
				h.Modified = st.ModTime()
			}
		}
		out, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		in, err := os.Open(f.Path)
		if err != nil {
			return i18n.E("files.moved")
		}
		_, err = io.Copy(out, in)
		in.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

// compressed: formatos que deflate casi no reduce (fotos, video, audio, zips…).
func compressed(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".avif",
		".mp4", ".mov", ".mkv", ".webm", ".3gp", ".avi",
		".mp3", ".m4a", ".aac", ".ogg", ".opus", ".flac",
		".zip", ".rar", ".7z", ".gz", ".xz", ".bz2", ".apk",
		".docx", ".xlsx", ".pptx", ".odt", ".ods", ".pdf":
		return true
	}
	return false
}

// cleanShareDir borra los .zip para compartir que llevan más de shareMaxAge.
func cleanShareDir(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && now.Sub(info.ModTime()) > shareMaxAge {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// safeName deja un nombre de archivo sin separadores ni "..".
func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "_"
	}
	return name
}
