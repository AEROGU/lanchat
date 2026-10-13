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

// approvedKey es donde Windows anota si el usuario (en el Administrador de
// tareas) o un antivirus deshabilitó una entrada de runKey: el primer byte
// impar significa deshabilitada. Sin valor, la entrada está habilitada.
const approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`

// startupDisabled indica si Windows tiene deshabilitada la entrada de inicio.
func startupDisabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(appName)
	return err == nil && len(b) > 0 && b[0]&1 == 1
}

// clearStartupApproved borra la anotación de Windows sobre la entrada.
func clearStartupApproved() {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	k.DeleteValue(appName)
}

// autostartCommand arma la línea de comandos del inicio automático.
func autostartCommand(exe string, args []string) string {
	cmd := syscall.EscapeArg(exe) + " -" + TrayFlag
	for _, a := range args {
		cmd += " " + syscall.EscapeArg(a)
	}
	return cmd
}

// commandRuns indica si la línea de comandos de una entrada de inicio
// arranca exe (con cualquier opción, también las de versiones anteriores).
func commandRuns(command, exe string) bool {
	command, exe = strings.ToLower(command), strings.ToLower(syscall.EscapeArg(exe))
	return command == exe || strings.HasPrefix(command, exe+" ")
}

// Autostart indica si LanChat arranca al iniciar sesión y si la entrada es
// de exe, el ejecutable actual. Solo consulta: el inicio con Windows nunca
// se cambia sin que el usuario lo pida (ver SetAutostart).
func Autostart(exe string) AutostartState {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return AutostartOff
	}
	defer k.Close()
	command, _, err := k.GetStringValue(appName)
	switch {
	case err != nil:
		return AutostartOff
	case !commandRuns(command, exe):
		return AutostartMoved
	case startupDisabled(): // la entrada existe pero Windows no la ejecuta
		return AutostartOff
	}
	return AutostartOn
}

// SetAutostart activa o desactiva el inicio automático de exe (en la
// bandeja); args se agregan a la línea de comandos. Se llama solo cuando el
// usuario lo elige: un programa que se registra por su cuenta para arrancar
// con Windows es justo lo que los antivirus vigilan.
func SetAutostart(enabled bool, exe string, args ...string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	// En ambos casos se borra la anotación de "deshabilitada": si el usuario
	// lo activa aquí debe funcionar, y si lo quita no debe quedar rastro.
	clearStartupApproved()
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

// FirewallRuleExists indica si hay una regla de LanChat, sea del ejecutable
// que sea (para quitarla al desinstalar).
// También cuenta las que Windows crea solo, con otro nombre, cuando el usuario
// pulsa "Permitir acceso" en su aviso: se reconocen por la ruta de exe.
func FirewallRuleExists(exe string) bool {
	if _, err := netsh(`advfirewall firewall show rule name="` + appName + `"`); err == nil {
		return true
	}
	out, err := netsh(`advfirewall firewall show rule name=all verbose`)
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

// RemoveFirewallFor borra todas las reglas de exe, tengan el nombre que
// tengan (las de LanChat y las que creó Windows). Requiere administrador.
func RemoveFirewallFor(exe string) {
	RemoveFirewall() // si no existía, no pasa nada
	if !strings.ContainsRune(exe, '"') {
		netsh(`advfirewall firewall delete rule name=all program="` + exe + `"`)
	}
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

// ---------- Inactividad ----------

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
)

// lastInputInfo es LASTINPUTINFO de user32.
type lastInputInfo struct {
	size uint32
	time uint32 // GetTickCount del último uso de teclado o mouse
}

// IdleTime es cuánto tiempo lleva el usuario sin usar teclado ni mouse.
func IdleTime() (time.Duration, error) {
	info := lastInputInfo{size: uint32(unsafe.Sizeof(lastInputInfo{}))}
	if ok, _, err := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		return 0, err
	}
	now, _, _ := procGetTickCount.Call()
	// Restar en uint32 tolera que el contador dé la vuelta (cada 49 días).
	return time.Duration(uint32(now)-info.time) * time.Millisecond, nil
}
