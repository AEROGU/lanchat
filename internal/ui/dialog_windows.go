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

// Confirm pregunta Sí/No con un cuadro de Windows; "No" es la respuesta
// predeterminada.
func Confirm(msg string) bool {
	text, _ := windows.UTF16PtrFromString(msg)
	title, _ := windows.UTF16PtrFromString("LanChat")
	ret, _ := windows.MessageBox(0, text, title, windows.MB_YESNO|windows.MB_ICONWARNING|windows.MB_DEFBUTTON2)
	return ret == idYes
}

// idYes es lo que devuelve MessageBox al elegir "Sí" (IDYES).
const idYes = 6
