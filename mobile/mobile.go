// Package mobile es la API del núcleo de LanChat para la app de Android. Se
// compila como librería con gomobile ("go tool mage android") y la usa el
// servicio en primer plano de la app; la interfaz es la misma página web del
// escritorio, que la app muestra en una WebView. Ver docs/ANDROID.md.
//
// gomobile solo admite tipos sencillos en la API: string, int, bool, error e
// interfaces que implementa Kotlin (Host).
package mobile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/ui"
	"github.com/AEROGU/lanchat/internal/version"
)

const (
	logFileName = "lanchat.log"
	// maxLogSize: al superarlo, el registro anterior pasa a lanchat.log.1.
	maxLogSize = 1 << 20
	// stopTimeout: espera máxima a las peticiones en curso al detener.
	stopTimeout = 5 * time.Second
)

// Host es lo que la app de Android ofrece al núcleo; lo implementa Kotlin.
// Sus métodos se llaman desde hilos de Go: no deben bloquear.
type Host interface {
	// Notify muestra una notificación de mensaje nuevo; chat es la
	// conversación (ui.Notice.Chat), para abrirla al tocar la notificación.
	Notify(title, body, chat string)
	// UnreadChanged informa el total de mensajes sin leer.
	UnreadChanged(total int)
	// OpenFile abre un archivo recibido con la app que corresponda.
	OpenFile(path string) error
	// OpenURL abre un enlace en el navegador (la página del proyecto).
	OpenURL(url string) error
	// IdleSeconds: cuánto lleva la pantalla apagada, en segundos (0 si está
	// encendida). Es la inactividad del ausente automático.
	IdleSeconds() int
}

var (
	mu      sync.Mutex
	running *instance

	// networks son las redes que informó la app con SetNetworks.
	networks atomic.Pointer[[]netip.Prefix]
)

type instance struct {
	app     *app.App
	cancel  context.CancelFunc
	srv     *ui.Server
	done    chan error // resultado de app.Run
	events  chan struct{}
	logFile *os.File
}

// Start arranca LanChat y devuelve la dirección de la interfaz que debe
// cargar la WebView (http://127.0.0.1:puerto/?t=token).
//
//   - dataDir: carpeta privada de la app (Context.getFilesDir()).
//   - deviceName: nombre del equipo que verán los demás (p. ej. "Galaxy A54");
//     Android no tiene un hostname útil.
//   - downloadDir: carpeta para los archivos recibidos (ruta absoluta; ""
//     conserva la elegida antes).
func Start(dataDir, deviceName, downloadDir string, host Host) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if running != nil {
		return "", errors.New("LanChat ya está iniciado")
	}
	if dataDir == "" || host == nil {
		return "", errors.New("faltan la carpeta de datos o el Host")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	logFile, err := openLog(dataDir)
	if err != nil {
		return "", err
	}
	log := slog.New(slog.NewTextHandler(logFile, nil))
	log.Info("iniciando", "version", version.App, "plataforma", "android", "redes", currentNetworks())

	a, err := app.New(app.Options{
		Dir: dataDir, Hostname: deviceName, Log: log, LocalNets: currentNetworks,
		IdleTime: func() (time.Duration, error) { return time.Duration(host.IdleSeconds()) * time.Second, nil },
	})
	if err != nil {
		logFile.Close()
		return "", err
	}
	if downloadDir != "" {
		if err := a.SetDownloadDir(downloadDir); err != nil {
			log.Warn("carpeta de descargas", "dir", downloadDir, "err", err)
		}
	}
	srv, err := ui.Listen(a, log)
	if err != nil {
		logFile.Close()
		return "", err
	}
	srv.OnUnreadChanged = host.UnreadChanged
	srv.Shell = &ui.Shell{OpenFile: host.OpenFile, OpenURL: host.OpenURL}

	ctx, cancel := context.WithCancel(context.Background())
	in := &instance{app: a, cancel: cancel, srv: srv, done: make(chan error, 1), events: make(chan struct{}), logFile: logFile}
	go srv.Serve()
	go func() { in.done <- a.Run(ctx) }()
	go func() {
		defer close(in.events)
		for ev := range a.Events() {
			srv.Publish(ctx, ev)
			if n, ok := srv.Notification(ctx, ev); ok {
				host.Notify(n.Title, n.Body, n.Chat)
			}
		}
	}()
	if total, err := a.TotalUnread(ctx); err == nil {
		host.UnreadChanged(total)
	}
	running = in
	return srv.LaunchURL(), nil
}

// Stop detiene LanChat: se despide de la red y cierra la base de datos.
func Stop() error {
	mu.Lock()
	in := running
	running = nil
	mu.Unlock()
	if in == nil {
		return nil
	}
	in.cancel()
	err := <-in.done
	<-in.events
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	return errors.Join(err, in.srv.Shutdown(ctx), in.logFile.Close())
}

// SetNetworks informa las redes IPv4 del teléfono, separadas por espacios
// (p. ej. "192.168.1.20/24"); las de IPv6 se ignoran. Android no deja a Go
// leer las interfaces de red: la app las toma de ConnectivityManager y las
// vuelve a enviar cada vez que cambian. Puede llamarse antes de Start.
func SetNetworks(cidrs string) error {
	nets := []netip.Prefix{}
	for _, f := range strings.Fields(cidrs) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return fmt.Errorf("red inválida %q: %w", f, err)
		}
		if p.Addr().Is4() {
			nets = append(nets, p)
		}
	}
	networks.Store(&nets)
	mu.Lock()
	in := running
	mu.Unlock()
	if in != nil {
		in.app.NetworksChanged()
	}
	return nil
}

func currentNetworks() []netip.Prefix {
	if p := networks.Load(); p != nil {
		return *p
	}
	return nil
}

// Running indica si LanChat está iniciado.
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return running != nil
}

// Version es la versión de LanChat y del protocolo, p. ej. "0.12.0 (protocolo v2)".
func Version() string {
	return fmt.Sprintf("%s (protocolo v%d)", version.App, protocol.Version)
}

// openLog abre lanchat.log para agregar; si creció demasiado, lo rota.
func openLog(dir string) (*os.File, error) {
	path := filepath.Join(dir, logFileName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogSize {
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
