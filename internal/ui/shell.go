package ui

import "github.com/AEROGU/lanchat/internal/i18n"

// Shell reemplaza las funciones del escritorio donde no lo hay (Android): la
// app abre los archivos recibidos y los enlaces, y no hay selectores de
// Windows ni carpetas que mostrar. Ver Server.Shell.
type Shell struct {
	OpenFile func(path string) error
	OpenURL  func(url string) error
	// ShareFiles pasa archivos recibidos al "Compartir" del sistema.
	ShareFiles func(paths []string) error
	// ShareDir: donde se preparan los .zip de las carpetas para compartir
	// (la app debe poder pasárselos a otras apps).
	ShareDir string
}

var (
	errNoFolders = i18n.E("shell.noFolders")
	errNoPicker  = i18n.E("shell.noPicker")
	errFixedDir  = i18n.E("shell.fixedDir")
)

func (s *Server) openFile(path string) error {
	if s.Shell != nil {
		return s.Shell.OpenFile(path)
	}
	return openPath(path)
}

func (s *Server) revealFile(path string) error {
	if s.Shell != nil {
		return errNoFolders
	}
	return revealPath(path)
}

func (s *Server) openFolder(dir string) error {
	if s.Shell != nil {
		return errNoFolders
	}
	return openPath(dir)
}

func (s *Server) openURL(url string) error {
	if s.Shell != nil {
		return s.Shell.OpenURL(url)
	}
	return openPath(url)
}

func (s *Server) pickFiles() ([]string, error) {
	if s.Shell != nil {
		return nil, errNoPicker
	}
	return pickFiles()
}

// folderDialog es el selector de carpetas (las pruebas lo reemplazan).
var folderDialog = pickFolder

func (s *Server) pickFolder(title string, newFolder bool) (string, error) {
	if s.Shell != nil {
		return "", errNoPicker
	}
	return folderDialog(title, newFolder)
}
