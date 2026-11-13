package ui

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/AEROGU/lanchat/internal/update"
	"github.com/AEROGU/lanchat/internal/version"
)

// Actualización de LanChat: se busca la última versión publicada en GitHub al
// abrir, una vez al día y cuando aparece otro equipo con una versión más
// nueva (si el usuario no lo desactivó), o con el botón de Ajustes. Siempre se
// pregunta antes de instalar. Lo que cambia por plataforma (qué archivo se
// descarga y cómo se instala) lo aporta UpdatePlatform.

const (
	// updateFirstCheck: la primera búsqueda espera a que la red esté lista.
	updateFirstCheck = 30 * time.Second
	// updateInterval: cada cuánto se vuelve a buscar con LanChat abierto.
	updateInterval = 24 * time.Hour
	// updatePeerInterval: lo mínimo entre dos búsquedas que provoca otro
	// equipo; con muchos equipos actualizados no se consulta a GitHub cada vez.
	updatePeerInterval = 10 * time.Minute
)

// Fases de la actualización (updateJSON.Phase).
const (
	phaseIdle        = "idle"        // no se ha buscado
	phaseChecking    = "checking"    // buscando
	phaseCurrent     = "current"     // es la última versión
	phaseAvailable   = "available"   // hay una versión nueva
	phaseDownloading = "downloading" // descargando
	phaseInstalling  = "installing"  // instalando (en Android, esperando al usuario)
	phaseError       = "error"
)

// UpdatePlatform es lo que cada plataforma aporta a la actualización.
type UpdatePlatform struct {
	// Asset es el archivo de la Release de version que se descarga.
	Asset func(version string) string
	// Dir es la carpeta exclusiva para la descarga; se vacía al abrir.
	Dir string
	// Install instala el archivo descargado y verificado. En Windows
	// reemplaza el .exe y reinicia LanChat; en Android abre el instalador del
	// sistema, que el usuario confirma.
	Install func(path, version string) error
	// Notify avisa, si no hay ninguna ventana abierta, que hay una versión
	// nueva (al tocar el aviso se abre LanChat, que pregunta).
	Notify func(title, body string)
}

type updater struct {
	client update.Client

	mu      sync.Mutex
	phase   string
	latest  string
	percent int
	errText string
	// ask: la página debe preguntar si se instala latest.
	ask bool
	// later es la versión que el usuario dejó para después: no se vuelve a
	// preguntar por ella hasta reabrir LanChat (o con el botón).
	later string
	// lastCheck: la última búsqueda; peerSeen, las versiones anunciadas por
	// otros equipos que ya provocaron una.
	lastCheck time.Time
	peerSeen  map[string]bool
}

type updateJSON struct {
	// Supported: esta compilación puede actualizarse sola; si no, Reason dice
	// por qué (vacío = la plataforma no lo permite).
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Auto      bool   `json:"auto"`
	Phase     string `json:"phase"`
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Percent   int    `json:"percent"`
	Error     string `json:"error,omitempty"`
	Ask       bool   `json:"ask"`
}

// updatesSupported indica si esta instancia puede actualizarse sola y, si
// es por la versión, por qué no.
func (s *Server) updatesSupported() (bool, string) {
	switch {
	case s.Updates == nil:
		return false, ""
	case !update.Updatable(version.App):
		return false, "Compilación de desarrollo (" + version.App + "): no se actualiza sola."
	}
	return true, ""
}

func (s *Server) updateState() updateJSON {
	ok, reason := s.updatesSupported()
	u := &s.upd
	u.mu.Lock()
	defer u.mu.Unlock()
	return updateJSON{
		Supported: ok, Reason: reason, Auto: s.b.Self().AutoUpdate,
		Phase: cmp.Or(u.phase, phaseIdle), Current: version.App, Latest: u.latest, Percent: u.percent,
		Error: u.errText, Ask: u.ask,
	}
}

func (s *Server) publishUpdate() { s.hub.broadcast("update", s.updateState()) }

// StartUpdates busca versiones nuevas al abrir y luego una vez al día, si el
// usuario no lo desactivó, hasta que ctx termine. Vacía antes la carpeta de
// descargas (lo que quedó de una actualización anterior).
func (s *Server) StartUpdates(ctx context.Context) {
	if ok, _ := s.updatesSupported(); !ok {
		return
	}
	os.RemoveAll(s.Updates.Dir)
	go func() {
		t := time.NewTimer(updateFirstCheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if s.b.Self().AutoUpdate {
				s.checkUpdate(ctx, false)
			}
			t.Reset(updateInterval)
		}
	}()
}

// peerVersion recibe la versión que anuncia otro equipo: si es más nueva que
// esta, se busca en GitHub (la descarga nunca viene del otro equipo, que
// podría anunciar cualquier cosa).
func (s *Server) peerVersion(ctx context.Context, v string) {
	if ok, _ := s.updatesSupported(); !ok || !update.Newer(v, version.App) || !s.b.Self().AutoUpdate {
		return
	}
	u := &s.upd
	u.mu.Lock()
	if u.peerSeen[v] || time.Since(u.lastCheck) < updatePeerInterval || update.Newer(u.latest, v) || u.latest == v {
		u.mu.Unlock()
		return
	}
	if u.peerSeen == nil {
		u.peerSeen = map[string]bool{}
	}
	u.peerSeen[v] = true
	u.mu.Unlock()
	go s.checkUpdate(ctx, false)
}

// checkUpdate busca la última versión. Una búsqueda automática que falla (sin
// internet) no se muestra; la del botón, sí.
func (s *Server) checkUpdate(ctx context.Context, manual bool) {
	u := &s.upd
	u.mu.Lock()
	switch u.phase {
	case phaseChecking, phaseDownloading:
		u.mu.Unlock()
		return
	}
	prev := u.phase
	u.phase, u.errText = phaseChecking, ""
	u.lastCheck = time.Now()
	u.mu.Unlock()
	if manual {
		s.publishUpdate()
	}

	latest, err := u.client.Latest(ctx)

	u.mu.Lock()
	notify := false
	switch {
	case err != nil && manual:
		u.phase, u.errText = phaseError, "No se pudo buscar: "+err.Error()
	case err != nil:
		s.log.Info("buscando actualizaciones", "err", err)
		u.phase = prev
		if prev == phaseChecking || prev == phaseError {
			u.phase = phaseIdle
		}
	case update.Newer(latest, version.App):
		u.phase, u.latest = phaseAvailable, latest
		u.ask = manual || latest != u.later
		notify = u.ask && !manual
		s.log.Info("hay una versión nueva", "version", latest)
	default:
		u.phase, u.latest = phaseCurrent, latest
	}
	u.mu.Unlock()
	s.publishUpdate()
	if notify && !s.HasWindow() && s.Updates.Notify != nil {
		s.Updates.Notify("LanChat "+latest+" está disponible",
			"Abre LanChat para actualizar (tienes la "+version.App+").")
	}
}

// installUpdate descarga la versión nueva, comprueba su SHA-256 y la instala.
func (s *Server) installUpdate() error {
	u := &s.upd
	u.mu.Lock()
	latest := u.latest
	switch {
	case latest == "" || !update.Newer(latest, version.App):
		u.mu.Unlock()
		return errors.New("no hay una versión nueva que instalar; busca actualizaciones primero")
	case u.phase == phaseChecking || u.phase == phaseDownloading:
		u.mu.Unlock()
		return errors.New("espera a que termine la búsqueda o la descarga en curso")
	}
	u.phase, u.percent, u.errText, u.ask = phaseDownloading, 0, "", false
	u.mu.Unlock()
	s.publishUpdate()

	go func() {
		var shown time.Time
		progress := func(done, total int64) {
			if total <= 0 || time.Since(shown) < 250*time.Millisecond {
				return
			}
			shown = time.Now()
			u.mu.Lock()
			u.percent = int(done * 100 / total)
			u.mu.Unlock()
			s.publishUpdate()
		}
		p := s.Updates
		path, err := u.client.Fetch(context.Background(), latest, p.Asset(latest), p.Dir, progress)
		if err == nil {
			u.mu.Lock()
			u.phase, u.percent = phaseInstalling, 100
			u.mu.Unlock()
			s.publishUpdate()
			s.log.Info("instalando la versión nueva", "version", latest)
			err = p.Install(path, latest)
		}
		if err != nil {
			s.log.Error("actualizando", "version", latest, "err", err)
			os.RemoveAll(p.Dir)
			u.mu.Lock()
			u.phase, u.errText = phaseError, "No se pudo actualizar: "+err.Error()
			u.mu.Unlock()
			s.publishUpdate()
		}
	}()
	return nil
}

// ---------- Rutas ----------

func (s *Server) handleUpdateState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.updateState())
}

// handleUpdateCheck busca en segundo plano; el resultado llega como evento.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if ok, reason := s.updatesSupported(); !ok {
		s.fail(w, http.StatusBadRequest, errors.New(cmp.Or(reason, "este equipo no se puede actualizar solo")))
		return
	}
	go s.checkUpdate(context.Background(), true)
	writeJSON(w, http.StatusOK, s.updateState())
}

func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if ok, reason := s.updatesSupported(); !ok {
		s.fail(w, http.StatusBadRequest, errors.New(cmp.Or(reason, "este equipo no se puede actualizar solo")))
		return
	}
	if err := s.installUpdate(); err != nil {
		s.fail(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, s.updateState())
}

// handleUpdateLater: "Ahora no". No se vuelve a preguntar por esa versión
// hasta reabrir LanChat.
func (s *Server) handleUpdateLater(w http.ResponseWriter, r *http.Request) {
	u := &s.upd
	u.mu.Lock()
	u.later, u.ask = u.latest, false
	u.mu.Unlock()
	s.publishUpdate()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAutoUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.b.SetAutoUpdate(req.Enabled); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.updateState())
}
