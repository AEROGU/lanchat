package mobile

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/testutil"
)

type fakeHost struct {
	mu     sync.Mutex
	unread []int
}

func (h *fakeHost) Notify(title, body string) {}
func (h *fakeHost) UnreadChanged(total int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unread = append(h.unread, total)
}

// Arranca, sirve la interfaz como la cargaría la WebView, se detiene y vuelve
// a arrancar con los mismos datos.
func TestStartStop(t *testing.T) {
	dir := testutil.TempDir(t)
	// Puertos propios para no chocar con un LanChat abierto en esta PC.
	cfg := `{"udp_port": 50120, "http_port": 50121, "setup_done": true}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	downloads := filepath.Join(testutil.TempDir(t), "Recibidos")
	host := &fakeHost{}

	for round := range 2 {
		url, err := Start(dir, "Galaxy de prueba", downloads, host)
		if err != nil {
			t.Fatalf("vuelta %d: %v", round, err)
		}
		if _, err := Start(dir, "otro", "", host); err == nil {
			t.Error("un segundo Start debía fallar")
		}
		if !Running() {
			t.Error("debía estar iniciado")
		}

		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar, Timeout: 5 * time.Second}
		resp, err := c.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("página: %d", resp.StatusCode)
		}
		resp, err = c.Get(strings.Split(url, "?")[0] + "api/state")
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			Self        struct{ Hostname string }
			DownloadDir string
		}
		json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if st.Self.Hostname != "Galaxy de prueba" || st.DownloadDir != downloads {
			t.Errorf("estado: %+v", st)
		}

		if err := Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if Running() {
			t.Error("debía estar detenido")
		}
	}
	if err := Stop(); err != nil {
		t.Errorf("Stop sin iniciar: %v", err)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.unread) == 0 || host.unread[0] != 0 {
		t.Errorf("UnreadChanged: %v", host.unread)
	}
	if !strings.Contains(Version(), "protocolo v") {
		t.Errorf("Version = %q", Version())
	}
}
