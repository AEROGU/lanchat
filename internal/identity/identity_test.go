package identity

import (
	"strings"
	"testing"
)

func TestLoadCreatesAndKeeps(t *testing.T) {
	dir := t.TempDir()
	a, err := Load(dir, "id-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Fingerprint) != 64 || a.Cert.Leaf == nil {
		t.Fatalf("identidad: %q", a.Fingerprint)
	}
	b, err := Load(dir, "id-1")
	if err != nil || b.Fingerprint != a.Fingerprint {
		t.Errorf("al volver a cargar cambió la huella: %v %v", b, err)
	}
	other, _ := Load(t.TempDir(), "id-2")
	if other.Fingerprint == a.Fingerprint {
		t.Error("dos instalaciones no deben compartir huella")
	}
}

func TestFormat(t *testing.T) {
	got := Format(strings.Repeat("ab12", 16))
	if got != strings.TrimSpace(strings.Repeat("AB12 ", 16)) {
		t.Errorf("Format = %q", got)
	}
}
