package ui

import (
	"fmt"
	"runtime"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	comdlg32                 = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileNameW     = comdlg32.NewProc("GetOpenFileNameW")
	procCommDlgExtendedError = comdlg32.NewProc("CommDlgExtendedError")
	pickerMu                 sync.Mutex
)

const (
	ofnHideReadOnly     = 0x00000004
	ofnNoChangeDir      = 0x00000008
	ofnAllowMultiSelect = 0x00000200
	ofnPathMustExist    = 0x00000800
	ofnFileMustExist    = 0x00001000
	ofnExplorer         = 0x00080000
	ofnDontAddToRecent  = 0x02000000
	fnerrBufferTooSmall = 0x3003
	// pickerBufferChars alcanza para cientos de archivos elegidos a la vez.
	pickerBufferChars = 1 << 16
)

// openFileName es OPENFILENAMEW de comdlg32.
type openFileName struct {
	structSize    uint32
	owner         windows.HWND
	instance      windows.Handle
	filter        *uint16
	customFilter  *uint16
	maxCustFilter uint32
	filterIndex   uint32
	file          *uint16
	maxFile       uint32
	fileTitle     *uint16
	maxFileTitle  uint32
	initialDir    *uint16
	title         *uint16
	flags         uint32
	fileOffset    uint16
	fileExtension uint16
	defExt        *uint16
	custData      uintptr
	hook          uintptr
	templateName  *uint16
	reserved      uintptr
	reserved2     uint32
	flagsEx       uint32
}

// pickFiles muestra el selector de archivos de Windows sobre la ventana de
// LanChat. Devuelve nil si el usuario cancela.
// beginDialog prepara el hilo para un diálogo de Windows (que necesita COM)
// y evita abrir dos a la vez. El hilo queda bloqueado a la goroutine y Go lo
// descarta al terminar, junto con su estado COM. Llamar a la función
// devuelta al cerrar el diálogo.
func beginDialog() (func(), error) {
	if !pickerMu.TryLock() {
		return nil, errPickerBusy
	}
	runtime.LockOSThread()
	windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	return pickerMu.Unlock, nil
}

func pickFiles() ([]string, error) {
	done, err := beginDialog()
	if err != nil {
		return nil, err
	}
	defer done()

	buf := make([]uint16, pickerBufferChars)
	title, _ := windows.UTF16PtrFromString("Elegir archivos para enviar")
	filter, _ := windows.UTF16FromString("Todos los archivos\x00*.*\x00")
	ofn := openFileName{
		owner:   findWindow(),
		filter:  &filter[0],
		file:    &buf[0],
		maxFile: uint32(len(buf)),
		title:   title,
		flags: ofnExplorer | ofnAllowMultiSelect | ofnFileMustExist | ofnPathMustExist |
			ofnNoChangeDir | ofnHideReadOnly | ofnDontAddToRecent,
	}
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	if ok, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn))); ok == 0 {
		code, _, _ := procCommDlgExtendedError.Call()
		switch code {
		case 0:
			return nil, nil
		case fnerrBufferTooSmall:
			return nil, fmt.Errorf("son demasiados archivos a la vez; envíalos en varias tandas")
		}
		return nil, fmt.Errorf("selector de archivos: error %#x", code)
	}
	return splitMultiSelect(decodeUntilDoubleNull(buf)), nil
}

// decodeUntilDoubleNull convierte el búfer a texto conservando los NUL que
// separan los nombres (UTF16ToString cortaría en el primero).
func decodeUntilDoubleNull(buf []uint16) string {
	end := 0
	for end+1 < len(buf) && (buf[end] != 0 || buf[end+1] != 0) {
		end++
	}
	return string(utf16.Decode(buf[:end]))
}

func openPath(path string) error {
	return shellExecute("open", path, "")
}

// revealPath abre el Explorador con el archivo seleccionado.
func revealPath(path string) error {
	return shellExecute("open", "explorer.exe", `/select,"`+path+`"`)
}

func shellExecute(verb, file, args string) error {
	v, err := windows.UTF16PtrFromString(verb)
	if err != nil {
		return err
	}
	f, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	var a *uint16
	if args != "" {
		if a, err = windows.UTF16PtrFromString(args); err != nil {
			return err
		}
	}
	return windows.ShellExecute(0, v, f, a, nil, windows.SW_SHOWNORMAL)
}

var (
	shell32                   = windows.NewLazySystemDLL("shell32.dll")
	procSHBrowseForFolderW    = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListEx = shell32.NewProc("SHGetPathFromIDListEx")
	ole32                     = windows.NewLazySystemDLL("ole32.dll")
	procCoTaskMemFree         = ole32.NewProc("CoTaskMemFree")
)

const (
	bifReturnOnlyFSDirs  = 0x0001
	bifNewDialogStyle    = 0x0040
	bifNoNewFolderButton = 0x0200
	// maxPathChars alcanza para rutas largas de Windows.
	maxPathChars = 32768
)

// browseInfo es BROWSEINFOW de shell32.
type browseInfo struct {
	owner       windows.HWND
	root        uintptr
	displayName *uint16
	title       *uint16
	flags       uint32
	callback    uintptr
	param       uintptr
	image       int32
}

// pickFolder muestra el selector de carpetas de Windows sobre la ventana de
// LanChat. Devuelve "" si el usuario cancela.
func pickFolder() (string, error) {
	done, err := beginDialog()
	if err != nil {
		return "", err
	}
	defer done()

	display := make([]uint16, windows.MAX_PATH)
	title, _ := windows.UTF16PtrFromString("Elegir la carpeta para enviar")
	bi := browseInfo{
		owner:       findWindow(),
		displayName: &display[0],
		title:       title,
		flags:       bifReturnOnlyFSDirs | bifNewDialogStyle | bifNoNewFolderButton,
	}
	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "", nil
	}
	defer procCoTaskMemFree.Call(pidl)
	buf := make([]uint16, maxPathChars)
	if ok, _, _ := procSHGetPathFromIDListEx.Call(pidl, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0); ok == 0 {
		return "", fmt.Errorf("esa carpeta no está en el disco (¿es una biblioteca o un equipo de red?)")
	}
	return windows.UTF16ToString(buf), nil
}
