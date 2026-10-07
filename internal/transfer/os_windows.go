package transfer

import (
	"os"

	"golang.org/x/sys/windows"
)

// freeSpace devuelve los bytes libres para el usuario en el disco de dir, o -1
// si no se puede saber.
func freeSpace(dir string) int64 {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return -1
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return -1
	}
	return int64(free)
}

// markFromNetwork agrega la "marca de la Web" (Zone.Identifier), igual que un
// navegador al descargar: Windows y Office avisarán antes de ejecutar
// programas o macros del archivo.
func markFromNetwork(path string) error {
	return os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o600)
}

// defaultDownloadDir es la carpeta Descargas del usuario (aunque la haya movido).
func defaultDownloadDir() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Downloads, 0)
}
