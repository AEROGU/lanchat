package protocol

import (
	"strings"
	"testing"
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
