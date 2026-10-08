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
	"os"
	"path/filepath"
	"sync"
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
	// Notify muestra una notificación de mensaje nuevo.
	Notify(title, body string)
	// UnreadChanged informa el total de mensajes sin leer.
	UnreadChanged(total int)
}

var (
	mu      sync.Mutex
	running *instance
)

type instance struct {
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
	log.Info("iniciando", "version", version.App, "plataforma", "android")

	a, err := app.New(app.Options{Dir: dataDir, Hostname: deviceName, Log: log})
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

	ctx, cancel := context.WithCancel(context.Background())
	in := &instance{cancel: cancel, srv: srv, done: make(chan error, 1), events: make(chan struct{}), logFile: logFile}
	go srv.Serve()
	go func() { in.done <- a.Run(ctx) }()
	go func() {
		defer close(in.events)
		for ev := range a.Events() {
			srv.Publish(ctx, ev)
			if title, body, ok := srv.Notification(ctx, ev); ok {
				host.Notify(title, body)
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
