package ui

import (
	"runtime"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/AEROGU/lanchat/internal/i18n"
	"golang.org/x/sys/windows"
)

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalAlloc              = kernel32.NewProc("GlobalAlloc")
	procGlobalLock               = kernel32.NewProc("GlobalLock")
	procGlobalUnlock             = kernel32.NewProc("GlobalUnlock")
	procGlobalFree               = kernel32.NewProc("GlobalFree")
	procRtlMoveMemory            = kernel32.NewProc("RtlMoveMemory")
	procOpenClipboard            = user32.NewProc("OpenClipboard")
	procCloseClipboard           = user32.NewProc("CloseClipboard")
	procEmptyClipboard           = user32.NewProc("EmptyClipboard")
	procSetClipboardData         = user32.NewProc("SetClipboardData")
	procRegisterClipboardFormatW = user32.NewProc("RegisterClipboardFormatW")
)

const (
	cfHDrop        = 15
	gmemMoveable   = 0x0002
	dropEffectCopy = 1
	// dropFilesSize es sizeof(DROPFILES): pFiles, pt.x, pt.y, fNC, fWide.
	dropFilesSize = 20
)

var errClipboard = i18n.E("shell.clipboard")

// copyFiles pone los archivos en el portapapeles de Windows como lo hace el
// Explorador al copiar (CF_HDROP), para pegarlos con Ctrl+V en otra app o en
// una carpeta.
func copyFiles(paths []string) error {
	if len(paths) == 0 {
		return errClipboard
	}
	drop := hdropData(paths)
	effect := make([]byte, 4)
	effect[0] = dropEffectCopy // "Preferred DropEffect": copiar, no mover

	// El portapapeles se abre y se cierra en el mismo hilo.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !openClipboard() {
		return errClipboard
	}
	defer procCloseClipboard.Call()
	if r, _, _ := procEmptyClipboard.Call(); r == 0 {
		return errClipboard
	}
	if err := setClipboard(cfHDrop, drop); err != nil {
		return err
	}
	name, _ := windows.UTF16PtrFromString("Preferred DropEffect")
	if f, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name))); f != 0 {
		setClipboard(f, effect) // opcional: sin él se pega igual, como copia
	}
	return nil
}

// openClipboard reintenta un momento: otra app puede tenerlo abierto.
func openClipboard() bool {
	for range 10 {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// hdropData arma DROPFILES seguido de las rutas en UTF-16, cada una
// terminada en NUL, y un NUL final.
func hdropData(paths []string) []byte {
	var chars []uint16
	for _, p := range paths {
		chars = append(chars, utf16.Encode([]rune(p))...)
		chars = append(chars, 0)
	}
	chars = append(chars, 0)
	buf := make([]byte, dropFilesSize+2*len(chars))
	buf[0] = dropFilesSize // pFiles: las rutas empiezan tras la estructura
	buf[16] = 1            // fWide: rutas en UTF-16
	for i, c := range chars {
		buf[dropFilesSize+2*i] = byte(c)
		buf[dropFilesSize+2*i+1] = byte(c >> 8)
	}
	return buf
}

// setClipboard copia data a memoria global y se la entrega al portapapeles,
// que desde entonces es su dueño.
func setClipboard(format uintptr, data []byte) error {
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if h == 0 {
		return errClipboard
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errClipboard
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(format, h); r == 0 {
		procGlobalFree.Call(h)
		return errClipboard
	}
	return nil
}
