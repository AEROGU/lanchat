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
	want := `"C:\Program Files\LanChat\lanchat.exe" -hidden -dir "D:\Datos LanChat"`
	if got != want {
		t.Errorf("comando = %s\nquería    %s", got, want)
	}
}
