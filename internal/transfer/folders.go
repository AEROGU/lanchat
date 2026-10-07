package transfer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

// Item es un archivo a ofrecer: su ruta local y la subcarpeta relativa con
// la que lo verá el destinatario ("" = suelto, "Proyecto/planos").
type Item struct {
	Path string
	Dir  string
}

// errTooManyFiles: la carpeta tiene más archivos de los que admite una oferta.
var errTooManyFiles = fmt.Errorf("son más de %d archivos; comprímelos en un .zip o envíalos en partes", protocol.MaxOfferFiles)

// ExpandPaths convierte archivos y carpetas elegidos en la lista de archivos a
// ofrecer. De cada carpeta se incluyen sus archivos (en cualquier nivel) con
// su ruta relativa; los accesos directos, enlaces y carpetas vacías se omiten.
func ExpandPaths(paths []string) ([]Item, error) {
	var items []Item
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if fi.Mode().IsRegular() {
			items = append(items, Item{Path: p})
			continue
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("%s no es un archivo ni una carpeta", fi.Name())
		}
		root := filepath.Base(p)
		err = filepath.WalkDir(p, func(file string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil // carpetas (se recorren igual), enlaces y especiales
			}
			if len(items) >= protocol.MaxOfferFiles {
				return errTooManyFiles
			}
			rel, err := filepath.Rel(p, filepath.Dir(file))
			if err != nil {
				return err
			}
			dir := root
			if rel != "." {
				dir = path.Join(root, filepath.ToSlash(rel))
			}
			items = append(items, Item{Path: file, Dir: dir})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, errors.New("no hay archivos para enviar (¿carpetas vacías?)")
	}
	return items, nil
}

// rootOf es el primer segmento de una subcarpeta ("Proyecto/planos" → "Proyecto").
func rootOf(dir string) string {
	root, _, _ := strings.Cut(dir, "/")
	return root
}

// localDirs adapta las subcarpetas de una oferta recibida a la carpeta de
// descargas: cada segmento sigue las reglas de Windows y, si la carpeta raíz
// ya existe en dest, se usa "Proyecto (1)" (como con los archivos).
func localDirs(files []store.TransferFile, dest string) (map[int]string, error) {
	roots := map[string]string{} // raíz remota → raíz local
	out := map[int]string{}
	for _, f := range files {
		if f.Dir == "" {
			continue
		}
		segs := strings.Split(f.Dir, "/")
		for i, s := range segs {
			segs[i] = safeFileName(s)
		}
		local, ok := roots[segs[0]]
		if !ok {
			p, err := uniquePath(dest, segs[0])
			if err != nil {
				return nil, err
			}
			local = filepath.Base(p)
			roots[segs[0]] = local
		}
		segs[0] = local
		out[f.Index] = path.Join(segs...)
	}
	return out, nil
}

// fileDir es la carpeta local donde va un archivo recibido.
func fileDir(t store.Transfer, f store.TransferFile) string {
	return filepath.Join(t.Dir, filepath.FromSlash(f.Dir))
}

// stagingRoot es la carpeta temporal (hija directa de StagingDir) que
// contiene path; "" si path no está en StagingDir.
func (s *Service) stagingRoot(p string) string {
	rel, err := filepath.Rel(s.cfg.StagingDir, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return ""
	}
	first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return filepath.Join(s.cfg.StagingDir, first)
}

// prepareDirs fija la carpeta de descargas de una oferta recibida y, la
// primera vez que se acepta, adapta sus subcarpetas (ver localDirs). En los
// reintentos se conservan, para reanudar en el mismo lugar.
func (s *Service) prepareDirs(ctx context.Context, t *store.Transfer, dest string) error {
	if t.Dir == "" {
		dirs, err := localDirs(t.Files, dest)
		if err != nil {
			return err
		}
		if err := s.store.SetFileDirs(ctx, t.ID, dirs); err != nil {
			return err
		}
		for i := range t.Files {
			t.Files[i].Dir = dirs[t.Files[i].Index]
		}
	}
	t.Dir = dest
	return s.store.SetTransferDir(ctx, t.ID, dest)
}
