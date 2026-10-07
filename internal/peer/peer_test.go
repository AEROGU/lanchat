package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/AEROGU/lanchat/internal/identity"
)

func TestMutualTLSAndPinning(t *testing.T) {
	serverID, _ := identity.Load(t.TempDir(), "servidor")
	clientID, _ := identity.Load(t.TempDir(), "cliente")
	otherID, _ := identity.Load(t.TempDir(), "otro")

	srv, err := Listen("127.0.0.1:0", serverID)
	if err != nil {
		t.Fatal(err)
	}
	srv.Handle("GET /quien", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, ClientFingerprint(r))
	}))
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	url := fmt.Sprintf("https://127.0.0.1:%d/quien", srv.Port())

	get := func(ctx context.Context) (string, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := NewClient(clientID).Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil
	}

	// Con la huella correcta: conecta y el servidor ve la huella del cliente.
	got, err := get(WithFingerprint(context.Background(), serverID.Fingerprint))
	if err != nil || got != clientID.Fingerprint {
		t.Fatalf("huella vista por el servidor = %q, %v", got, err)
	}
	// Con otra huella: no conecta.
	if _, err := get(WithFingerprint(context.Background(), otherID.Fingerprint)); !errors.Is(err, ErrIdentityMismatch) {
		t.Errorf("con huella equivocada: %v", err)
	}
	// Sin huella: no conecta.
	if _, err := get(context.Background()); !errors.Is(err, errNoFingerprint) {
		t.Errorf("sin huella: %v", err)
	}
	// Sin TLS: el servidor no responde HTTP plano.
	if resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/quien", srv.Port())); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Error("no debía aceptar HTTP sin cifrar")
		}
	}
}
