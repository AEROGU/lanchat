package protocol

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateName(t *testing.T) {
	ok := []string{"", "Juan", "Contabilidad – Ñoño", strings.Repeat("á", MaxNameLen)}
	for _, s := range ok {
		if err := ValidateName(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	bad := []string{strings.Repeat("a", MaxNameLen+1), "a\nb", "\x1b[31mrojo", "a\u0085b", "\xff"}
	for _, s := range bad {
		if ValidateName(s) == nil {
			t.Errorf("%q debía rechazarse", s)
		}
	}
}

func TestValidateMessage(t *testing.T) {
	ok := []string{"hola", "línea 1\nlínea 2\r\n\tcon tab"}
	for _, s := range ok {
		if err := ValidateMessage(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	bad := []string{"", "  \n ", "\x1b]0;título\x07", "a\x00b", "\xff", strings.Repeat("a", MaxMessageBytes+1)}
	for _, s := range bad {
		if ValidateMessage(s) == nil {
			t.Errorf("%.20q debía rechazarse", s)
		}
	}
}

func TestPeerTTLToleratesLostAnnounces(t *testing.T) {
	if PeerTTL <= 3*AnnounceInterval {
		t.Fatalf("PeerTTL (%v) debe superar 3 anuncios (%v)", PeerTTL, 3*AnnounceInterval)
	}
}

func TestStatus(t *testing.T) {
	for in, want := range map[string]string{"away": "away", "busy": "busy", "": "available", "vacaciones": "available"} {
		if got := NormalizeStatus(in); got != want {
			t.Errorf("NormalizeStatus(%q) = %q", in, got)
		}
	}
	if ValidateStatusText("En junta hasta las 12 🕛") != nil || ValidateStatusText("a\nb") == nil ||
		ValidateStatusText(strings.Repeat("x", MaxStatusTextLen+1)) == nil {
		t.Error("ValidateStatusText")
	}
}

func TestValidateRelDir(t *testing.T) {
	for _, ok := range []string{"", "Proyecto", "Proyecto/planos/año 2026"} {
		if err := ValidateRelDir(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"/abs", "a//b", "a/../b", "..", "a\b", "a/", strings.Repeat("a/", MaxDirDepth) + "a"} {
		if ValidateRelDir(bad) == nil {
			t.Errorf("%q debía rechazarse", bad)
		}
	}
}

func TestReplySnippet(t *testing.T) {
	if got := ReplySnippet("  hola\n"); got != "hola" {
		t.Errorf("corto: %q", got)
	}
	long := strings.Repeat("ñ", MaxReplyTextLen+10)
	got := ReplySnippet(long)
	if n := utf8.RuneCountInString(got); n != MaxReplyTextLen || !strings.HasSuffix(got, "…") {
		t.Errorf("largo: %d caracteres, %q", n, got)
	}
	if ValidateReplyText(got) != nil || ValidateReplyText("") != nil {
		t.Error("el recorte y la cita vacía debían ser válidos")
	}
	if ValidateReplyText(long) == nil || ValidateReplyText("a\x1bb") == nil {
		t.Error("debía rechazar una cita demasiado larga o con caracteres de control")
	}
}

func TestPhotoID(t *testing.T) {
	id := PhotoID([]byte("foto"))
	if len(id) != PhotoIDLen || ValidatePhotoID(id) != nil || id == PhotoID([]byte("otra")) {
		t.Fatalf("PhotoID = %q", id)
	}
	if ValidatePhotoID("") != nil {
		t.Error("sin foto debía ser válido")
	}
	for _, s := range []string{"abc", strings.Repeat("A", PhotoIDLen), strings.Repeat("g", PhotoIDLen), id + "0"} {
		if ValidatePhotoID(s) == nil {
			t.Errorf("%q debía rechazarse", s)
		}
	}
}
