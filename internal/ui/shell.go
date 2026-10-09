package ui

import "errors"

// Shell reemplaza las funciones del escritorio donde no lo hay (Android): la
// app abre los archivos recibidos y los enlaces, y no hay selectores de
// Windows ni carpetas que mostrar. Ver Server.Shell.
type Shell struct {
	OpenFile func(path string) error
	OpenURL  func(url string) error
}

var (
	errNoFolders = errors.New("en este equipo no se pueden abrir carpetas; abre cada archivo con «Abrir»")
	errNoPicker  = errors.New("el selector de Windows no existe en este equipo; usa 📎")
	errFixedDir  = errors.New("en este equipo la carpeta de archivos recibidos no se puede cambiar")
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

func (s *Server) pickFolder() (string, error) {
	if s.Shell != nil {
		return "", errNoPicker
	}
	return pickFolder()
}
