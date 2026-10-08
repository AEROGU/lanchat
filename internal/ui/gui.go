//go:build !android

// La ventana (Edge), la bandeja y las notificaciones de escritorio. En
// Android las reemplaza la app nativa (ver mobile/).

package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/config"
	"github.com/AEROGU/lanchat/internal/icon"
	"github.com/AEROGU/lanchat/internal/platform"
)

const (
	iconFileName = "icon.png"
	// toastSize: ícono de las notificaciones.
	toastSize      = 128
	edgeProfileDir = "edge"
	// reopenGuard evita abrir dos ventanas si el usuario hace doble clic en la
	// bandeja antes de que la primera termine de cargar.
	reopenGuard = 5 * time.Second
	// shutdownTimeout: espera máxima a las peticiones en curso al salir.
	shutdownTimeout = 5 * time.Second
)

type Options struct {
	// Dir es la carpeta de datos; vacío = %APPDATA%\LanChat.
	Dir string
	// CustomDir: Dir se eligió con -dir (pruebas, otra copia). Entonces no se
	// activa ni se actualiza solo el inicio con Windows, para no reemplazar el
	// de la instalación normal.
	CustomDir bool
	// Hidden arranca solo en la bandeja, sin abrir la ventana (inicio con Windows).
	Hidden bool
	Log    *slog.Logger
}

type gui struct {
	app        *app.App
	srv        *Server
	notifier   *notifier
	log        *slog.Logger
	profileDir string

	mu       sync.Mutex
	lastOpen time.Time
	ready    bool // la bandeja ya existe
	unread   int
}

// RunGUI arranca LanChat con ventana, bandeja y notificaciones, y bloquea
// hasta que el usuario elige "Salir". Si ya hay una instancia abierta, le pide
// que muestre su ventana y termina.
func RunGUI(o Options) error {
	if o.Dir == "" {
		d, err := config.DefaultDir()
		if err != nil {
			return err
		}
		o.Dir = d
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return err
	}
	if running, err := signalRunning(o.Dir); err != nil {
		o.Log.Warn("buscando otra instancia", "err", err)
	} else if running {
		o.Log.Info("LanChat ya estaba abierto; se mostró su ventana")
		return nil
	}

	a, err := app.New(app.Options{Dir: o.Dir, Log: o.Log, IdleTime: platform.IdleTime})
	if err != nil {
		return err
	}
	srv, err := Listen(a, o.Log)
	if err != nil {
		return err
	}
	iconPath := filepath.Join(o.Dir, iconFileName)
	if err := os.WriteFile(iconPath, icon.PNG(toastSize, false), 0o600); err != nil {
		o.Log.Warn("guardando icono", "err", err)
	}

	if o.CustomDir {
		srv.AutostartArgs = []string{"-dir", o.Dir}
	} else {
		setupAutostart(a, o.Log)
	}

	g := &gui{app: a, srv: srv, log: o.Log, profileDir: filepath.Join(o.Dir, edgeProfileDir)}
	srv.OnOpen = g.show
	srv.OnUnreadChanged = g.setUnread
	g.notifier = newNotifier(iconPath, g.show, o.Log)
	if total, err := a.TotalUnread(context.Background()); err == nil {
		g.unread = total
	}

	if err := writeInstance(o.Dir, srv.Port(), srv.Token()); err != nil {
		o.Log.Warn("registrando instancia", "err", err)
	}
	defer removeInstance(o.Dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	runErr := make(chan error, 1)
	go func() {
		err := a.Run(ctx)
		if ctx.Err() == nil { // terminó sin que el usuario pidiera salir
			systray.Quit()
		}
		runErr <- err
	}()

	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range a.Events() {
			srv.Publish(ctx, ev)
			g.maybeNotify(ctx, ev)
		}
	}()

	systray.Run(func() { g.onTrayReady(o.Hidden) }, nil)

	cancel()
	err = <-runErr
	<-eventsDone
	sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer scancel()
	return errors.Join(err, srv.Shutdown(sctx), <-serveErr)
}

func (g *gui) onTrayReady(hidden bool) {
	systray.SetTitle("LanChat")
	systray.SetOnTapped(g.show)
	open := systray.AddMenuItem("Abrir LanChat", "Mostrar la ventana")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Salir", "Cerrar LanChat en este equipo")
	go func() {
		for {
			select {
			case <-open.ClickedCh:
				g.show()
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	g.mu.Lock()
	g.ready = true
	unread := g.unread
	g.mu.Unlock()
	g.applyTray(unread)

	if !hidden {
		g.show()
	}
}

// show trae al frente la ventana o la abre si no hay ninguna.
func (g *gui) show() {
	if g.srv.HasWindow() {
		if !focusWindow() {
			g.srv.Focus()
		}
		return
	}
	g.mu.Lock()
	if time.Since(g.lastOpen) < reopenGuard {
		g.mu.Unlock()
		return
	}
	g.lastOpen = time.Now()
	g.mu.Unlock()
	if err := openWindow(g.srv.LaunchURL(), g.profileDir); err != nil {
		g.log.Error("abriendo la ventana", "err", err)
	}
}

func (g *gui) setUnread(total int) {
	g.mu.Lock()
	g.unread = total
	ready := g.ready
	g.mu.Unlock()
	if ready {
		g.applyTray(total)
	}
}

func (g *gui) applyTray(unread int) {
	systray.SetIcon(icon.ICO(unread > 0))
	if unread > 0 {
		systray.SetTooltip(fmt.Sprintf("LanChat — %d sin leer", unread))
	} else {
		systray.SetTooltip("LanChat")
	}
}

func (g *gui) maybeNotify(ctx context.Context, ev any) {
	if title, body, ok := g.srv.Notification(ctx, ev); ok {
		go g.notifier.notify(title, body)
	}
}

// setupAutostart activa el inicio con Windows en la primera ejecución y, si
// está activo, actualiza la ruta por si el programa se movió de carpeta.
func setupAutostart(a *app.App, log *slog.Logger) {
	exe, err := platform.Executable()
	if err != nil {
		return
	}
	first, err := a.FirstRun()
	if err != nil {
		log.Warn("guardando la primera ejecución", "err", err)
	}
	if first || platform.AutostartEnabled() {
		if err := platform.SetAutostart(true, exe); err != nil && !errors.Is(err, platform.ErrUnsupported) {
			log.Warn("configurando el inicio con Windows", "err", err)
		}
	}
}
