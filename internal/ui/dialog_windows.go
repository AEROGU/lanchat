package ui

import "golang.org/x/sys/windows"

// ShowError muestra un cuadro de error de Windows; sirve cuando el programa
// no tiene consola donde escribir.
func ShowError(msg string) { messageBox(msg, windows.MB_ICONERROR) }

// ShowInfo muestra un aviso informativo de Windows.
func ShowInfo(msg string) { messageBox(msg, windows.MB_ICONINFORMATION) }

func messageBox(msg string, icon uint32) {
	text, _ := windows.UTF16PtrFromString(msg)
	title, _ := windows.UTF16PtrFromString("LanChat")
	windows.MessageBox(0, text, title, windows.MB_OK|icon)
}
