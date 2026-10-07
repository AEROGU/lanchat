package ui

import (
	"regexp"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Traer al frente la ventana ya abierta. JavaScript (window.focus) no puede
// hacerlo; desde Windows sí, porque el clic en la bandeja lo recibió este
// proceso y eso le permite cambiar la ventana activa.

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procGetWindowTextW      = user32.NewProc("GetWindowTextW")
	procIsIconic            = user32.NewProc("IsIconic")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")

	// windowTitle coincide con el <title> de la página: "LanChat" o "(3) LanChat".
	windowTitle = regexp.MustCompile(`^(\(\d+\) )?LanChat$`)

	// Windows limita cuántos callbacks puede crear un proceso: se crea uno
	// solo y su resultado se pasa por enumFound, protegido por enumMu.
	enumMu    sync.Mutex
	enumFound windows.HWND
	enumCB    = windows.NewCallback(enumWindow)
)

const (
	swRestore = 9
	// edgeWindowClass es la clase de ventana de Edge/Chromium.
	edgeWindowClass = "Chrome_WidgetWin_1"
)

func enumWindow(h windows.HWND, _ uintptr) uintptr {
	const keepGoing, stop = 1, 0
	if !windows.IsWindowVisible(h) {
		return keepGoing
	}
	var cls [64]uint16
	if n, _ := windows.GetClassName(h, &cls[0], int32(len(cls))); windows.UTF16ToString(cls[:n]) != edgeWindowClass {
		return keepGoing
	}
	var title [128]uint16
	n, _, _ := procGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
	if !windowTitle.MatchString(windows.UTF16ToString(title[:n])) {
		return keepGoing
	}
	enumFound = h
	return stop
}

// focusWindow trae al frente (y restaura si está minimizada) la ventana de
// LanChat. Devuelve false si no la encuentra.
func focusWindow() bool {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumFound = 0
	windows.EnumWindows(enumCB, nil) // devuelve error cuando el callback se detiene: es lo esperado
	if enumFound == 0 {
		return false
	}
	if minimized, _, _ := procIsIconic.Call(uintptr(enumFound)); minimized != 0 {
		procShowWindow.Call(uintptr(enumFound), swRestore)
	}
	procSetForegroundWindow.Call(uintptr(enumFound))
	return true
}
