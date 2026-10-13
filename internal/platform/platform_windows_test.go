package platform

import "testing"

// Solo consultas: las pruebas no tocan el registro ni el firewall.
func TestFirewallAllowedUnknownProgram(t *testing.T) {
	if FirewallAllowed(`C:\no\existe\lanchat.exe`) {
		t.Error("no debía haber regla para un programa inexistente")
	}
}

func TestAutostartCommand(t *testing.T) {
	got := autostartCommand(`C:\Program Files\LanChat\lanchat.exe`, []string{"-dir", `D:\Datos LanChat`})
	want := `"C:\Program Files\LanChat\lanchat.exe" -tray -dir "D:\Datos LanChat"`
	if got != want {
		t.Errorf("comando = %s\nquería    %s", got, want)
	}
}

// La entrada de inicio es "nuestra" si arranca este ejecutable, también con
// las opciones de versiones anteriores (-hidden).
func TestCommandRuns(t *testing.T) {
	const exe = `C:\LanChat\lanchat.exe`
	cases := map[string]bool{
		`C:\LanChat\lanchat.exe -tray`:                 true,
		`C:\LanChat\lanchat.exe -hidden`:               true,
		`c:\lanchat\LANCHAT.EXE -hidden -dir D:\Datos`: true,
		`C:\LanChat\lanchat.exe`:                       true,
		`C:\Users\Ana\Desktop\lanchat.exe -hidden`:     false,
		`C:\LanChat\lanchat.exe.viejo -tray`:           false,
		`"C:\Program Files\LanChat\lanchat.exe" -tray`: false,
	}
	for command, want := range cases {
		if got := commandRuns(command, exe); got != want {
			t.Errorf("commandRuns(%q) = %v", command, got)
		}
	}
	spaced := `C:\Program Files\LanChat\lanchat.exe`
	if !commandRuns(autostartCommand(spaced, nil), spaced) {
		t.Error("no reconoce su propia entrada cuando la ruta lleva espacios")
	}
}

func TestIdleTime(t *testing.T) {
	d, err := IdleTime()
	if err != nil || d < 0 {
		t.Errorf("IdleTime = %v, %v", d, err)
	}
}
