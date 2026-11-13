package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/update"
	"github.com/AEROGU/lanchat/internal/version"
)

// fakeReleases imita GitHub con la versión 0.20.0 publicada; latest cuenta
// las búsquedas.
func fakeReleases(t *testing.T, latest *atomic.Int32) string {
	t.Helper()
	zip := []byte("contenido del zip 0.20.0")
	h := sha256.Sum256(zip)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			latest.Add(1)
			http.Redirect(w, r, "/AEROGU/lanchat/releases/tag/v0.20.0", http.StatusFound)
		case "/releases/download/v0.20.0/" + update.ChecksumsName:
			fmt.Fprintf(w, "%s  LanChat-0.20.0.zip\n", hex.EncodeToString(h[:]))
		case "/releases/download/v0.20.0/LanChat-0.20.0.zip":
			w.Write(zip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/releases"
}

type fakeUpdatePlatform struct {
	mu        sync.Mutex
	installed string // contenido del archivo que recibió Install
	notified  []string
	fail      error
}

func setVersion(t *testing.T, v string) {
	old := version.App
	version.App = v
	t.Cleanup(func() { version.App = old })
}

func startUpdateServer(t *testing.T, fake *fakeUpdatePlatform, latest *atomic.Int32) (*Server, *fakeBackend, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "descargas")
	releases := fakeReleases(t, latest)
	s, b := startServer(t, func(s *Server) {
		s.upd.client.Base = releases
		s.Updates = &UpdatePlatform{
			Asset: update.ZipName,
			Dir:   dir,
			Install: func(path, _ string) error {
				data, _ := os.ReadFile(path)
				fake.mu.Lock()
				defer fake.mu.Unlock()
				fake.installed = string(data)
				return fake.fail
			},
			Notify: func(title, body string) {
				fake.mu.Lock()
				defer fake.mu.Unlock()
				fake.notified = append(fake.notified, title)
			},
		}
	})
	return s, b, dir
}

func getUpdate(t *testing.T, c *http.Client, s *Server) updateJSON {
	t.Helper()
	resp, err := c.Get(base(s) + "/api/update")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var u updateJSON
	json.NewDecoder(resp.Body).Decode(&u)
	return u
}

// waitUpdate espera a que la actualización llegue a la fase phase.
func waitUpdate(t *testing.T, c *http.Client, s *Server, phase string) updateJSON {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		u := getUpdate(t, c, s)
		if u.Phase == phase {
			return u
		}
		if time.Now().After(deadline) {
			t.Fatalf("no se llegó a %q: %+v", phase, u)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUpdateCheckLaterAndInstall(t *testing.T) {
	setVersion(t, "0.19.0")
	fake := &fakeUpdatePlatform{}
	var latest atomic.Int32
	s, _, dir := startUpdateServer(t, fake, &latest)
	c := loggedClient(t, s)

	if u := getUpdate(t, c, s); !u.Supported || u.Phase != phaseIdle || u.Current != "0.19.0" {
		t.Fatalf("estado inicial: %+v", u)
	}
	resp, err := postJSON(c, base(s)+"/api/update/check", "{}")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("buscar: %v %v", resp.StatusCode, err)
	}
	resp.Body.Close()
	u := waitUpdate(t, c, s, phaseAvailable)
	if u.Latest != "0.20.0" || !u.Ask {
		t.Fatalf("debía preguntar por 0.20.0: %+v", u)
	}

	// "Ahora no": ya no pregunta, pero se puede instalar.
	resp, _ = postJSON(c, base(s)+"/api/update/later", "{}")
	resp.Body.Close()
	if u := getUpdate(t, c, s); u.Ask || u.Phase != phaseAvailable {
		t.Fatalf("después de «Ahora no»: %+v", u)
	}

	resp, _ = postJSON(c, base(s)+"/api/update/install", "{}")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("instalar: %d", resp.StatusCode)
	}
	waitUpdate(t, c, s, phaseInstalling)
	fake.mu.Lock()
	installed := fake.installed
	fake.mu.Unlock()
	if installed != "contenido del zip 0.20.0" {
		t.Fatalf("Install recibió %q", installed)
	}

	// Si la instalación falla, se informa y la descarga se borra.
	fake.mu.Lock()
	fake.fail = errors.New("sin permiso")
	fake.mu.Unlock()
	resp, _ = postJSON(c, base(s)+"/api/update/install", "{}")
	resp.Body.Close()
	u = waitUpdate(t, c, s, phaseError)
	if !strings.Contains(u.Error, "sin permiso") {
		t.Fatalf("error: %+v", u)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("la descarga fallida no se borró")
	}
}

func TestUpdatePeerTriggersOneCheck(t *testing.T) {
	setVersion(t, "0.19.0")
	fake := &fakeUpdatePlatform{}
	var latest atomic.Int32
	s, b, _ := startUpdateServer(t, fake, &latest)
	c := loggedClient(t, s)
	ctx := context.Background()

	// Con la búsqueda automática desactivada, otro equipo no la provoca.
	s.peerVersion(ctx, "0.20.0")
	time.Sleep(100 * time.Millisecond)
	if latest.Load() != 0 {
		t.Fatal("buscó con la búsqueda automática desactivada")
	}

	b.SetAutoUpdate(true)
	s.peerVersion(ctx, "0.18.0") // más vieja: nada
	s.peerVersion(ctx, "0.20.0")
	u := waitUpdate(t, c, s, phaseAvailable)
	if !u.Ask || u.Latest != "0.20.0" {
		t.Fatalf("debía preguntar: %+v", u)
	}
	// Muchos equipos con la misma versión: una sola búsqueda.
	for range 5 {
		s.peerVersion(ctx, "0.20.0")
	}
	time.Sleep(100 * time.Millisecond)
	if n := latest.Load(); n != 1 {
		t.Fatalf("se buscó %d veces", n)
	}
	// Sin ventana abierta, se avisa con una notificación.
	fake.mu.Lock()
	notified := fake.notified
	fake.mu.Unlock()
	if len(notified) != 1 || !strings.Contains(notified[0], "0.20.0") {
		t.Fatalf("avisos: %v", notified)
	}
}

func TestUpdateUnsupported(t *testing.T) {
	setVersion(t, "0.19.0-dirty")
	var latest atomic.Int32
	s, _, _ := startUpdateServer(t, &fakeUpdatePlatform{}, &latest)
	c := loggedClient(t, s)
	if u := getUpdate(t, c, s); u.Supported || !strings.Contains(u.Reason, "desarrollo") {
		t.Fatalf("una compilación de desarrollo no se actualiza: %+v", u)
	}
	resp, _ := postJSON(c, base(s)+"/api/update/check", "{}")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || latest.Load() != 0 {
		t.Fatalf("buscar en desarrollo: %d, %d búsquedas", resp.StatusCode, latest.Load())
	}

	// Sin UpdatePlatform (consola, otro sistema), no se muestra nada.
	setVersion(t, "0.19.0")
	s2, _ := startServer(t)
	if u := getUpdate(t, loggedClient(t, s2), s2); u.Supported || u.Reason != "" {
		t.Fatalf("sin plataforma: %+v", u)
	}
}
