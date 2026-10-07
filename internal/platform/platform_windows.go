package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ---------- Inicio con Windows ----------

// runKey es donde Windows busca los programas que arranca al iniciar sesión
// (del usuario actual: no requiere administrador).
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// autostartCommand arma la línea de comandos del inicio automático.
func autostartCommand(exe string, args []string) string {
	cmd := syscall.EscapeArg(exe) + " -" + HiddenFlag
	for _, a := range args {
		cmd += " " + syscall.EscapeArg(a)
	}
	return cmd
}

// AutostartEnabled indica si LanChat arranca al iniciar sesión.
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(appName)
	return err == nil
}

// SetAutostart activa o desactiva el inicio automático de exe (oculto en la
// bandeja); args se agregan a la línea de comandos.
func SetAutostart(enabled bool, exe string, args ...string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enabled {
		return k.SetStringValue(appName, autostartCommand(exe, args))
	}
	if err := k.DeleteValue(appName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// ---------- Firewall ----------

// netshTimeout limita cada llamada a netsh.
const netshTimeout = 30 * time.Second

// FirewallAllowed indica si existe la regla de LanChat para exe. Funciona sin
// ser administrador. Se compara la ruta para detectar si el programa se movió.
func FirewallAllowed(exe string) bool {
	out, err := netsh(`advfirewall firewall show rule name="` + appName + `" verbose`)
	return err == nil && strings.Contains(strings.ToLower(out), strings.ToLower(exe))
}

// AllowFirewall crea (o reemplaza) la regla que deja entrar el tráfico de exe
// en redes privadas y de dominio. Requiere administrador.
func AllowFirewall(exe string) error {
	if strings.ContainsRune(exe, '"') {
		return fmt.Errorf("ruta no válida: %s", exe)
	}
	RemoveFirewall() // si no existía, no pasa nada
	_, err := netsh(`advfirewall firewall add rule name="` + appName + `" dir=in action=allow enable=yes` +
		` profile=private,domain program="` + exe + `" description="Mensajería LanChat en la red local"`)
	return err
}

// RemoveFirewall borra la regla de LanChat. Requiere administrador.
func RemoveFirewall() error {
	_, err := netsh(`advfirewall firewall delete rule name="` + appName + `"`)
	return err
}

// netsh ejecuta netsh sin abrir ventana. args va tal cual en la línea de
// comandos porque netsh exige comillas en lugares que Go no pondría.
func netsh(args string) (string, error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	path := sys + `\netsh.exe`
	ctx, cancel := context.WithTimeout(context.Background(), netshTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `"` + path + `" ` + args,
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("netsh: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ---------- Administrador ----------

// IsAdmin indica si el proceso corre con permisos de administrador.
func IsAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	errorCancelled        = 1223
	// elevatedTimeout: espera máxima al proceso elevado (incluye el aviso de UAC).
	elevatedTimeout = 5 * time.Minute
)

// shellExecuteInfo es SHELLEXECUTEINFOW de shell32.
type shellExecuteInfo struct {
	size        uint32
	mask        uint32
	hwnd        windows.HWND
	verb        *uint16
	file        *uint16
	parameters  *uint16
	directory   *uint16
	show        int32
	instApp     windows.Handle
	idList      uintptr
	class       *uint16
	keyClass    windows.Handle
	hotKey      uint32
	iconMonitor windows.Handle
	process     windows.Handle
}

// RunElevated ejecuta exe con args como administrador (muestra el aviso de
// UAC), espera a que termine y devuelve su código de salida.
func RunElevated(exe, args string) (int, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return -1, err
	}
	params, err := windows.UTF16PtrFromString(args)
	if err != nil {
		return -1, err
	}
	info := shellExecuteInfo{
		mask:       seeMaskNoCloseProcess | seeMaskNoAsync,
		verb:       verb,
		file:       file,
		parameters: params,
		show:       windows.SW_HIDE,
	}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		if errno, isErrno := callErr.(syscall.Errno); isErrno && errno == errorCancelled {
			return -1, ErrCanceled
		}
		return -1, callErr
	}
	defer windows.CloseHandle(info.process)
	if ev, err := windows.WaitForSingleObject(info.process, uint32(elevatedTimeout.Milliseconds())); err != nil || ev != windows.WAIT_OBJECT_0 {
		return -1, errors.New("el proceso de administrador no terminó a tiempo")
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return -1, err
	}
	return int(code), nil
}
