// Comando lanchat: mensajería en la LAN. Por defecto abre la ventana y queda
// en la bandeja del sistema; con -console funciona desde la consola.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/AEROGU/lanchat/internal/config"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/ui"
	"github.com/AEROGU/lanchat/internal/version"
)

const (
	logFileName = "lanchat.log"
	// maxLogSize: al superarlo, el registro anterior pasa a lanchat.log.1.
	maxLogSize = 1 << 20
)

func main() {
	dir := flag.String("dir", "", `carpeta de datos (por defecto %APPDATA%\LanChat)`)
	debug := flag.Bool("debug", false, "registro detallado en la consola en vez de lanchat.log")
	consoleMode := flag.Bool("console", false, "usar desde la consola, sin ventana")
	hidden := flag.Bool("hidden", false, "arrancar solo en la bandeja, sin abrir la ventana")
	showVersion := flag.Bool("version", false, "mostrar la versión y salir")
	flag.Parse()

	if *showVersion {
		fmt.Printf("LanChat %s (protocolo v%d)\n", version.App, protocol.Version)
		return
	}
	if *dir == "" {
		d, err := config.DefaultDir()
		if err != nil {
			ui.ShowError("LanChat no encontró la carpeta de datos: " + err.Error())
			os.Exit(1)
		}
		*dir = d
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}

	if *consoleMode {
		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
		if err := runConsole(*dir, log); err != nil {
			log.Error(err.Error())
			os.Exit(1)
		}
		return
	}

	out := io.Writer(os.Stderr)
	if !*debug {
		f, err := openLog(*dir)
		if err == nil {
			defer f.Close()
			out = f
		}
	}
	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level}))
	log.Info("iniciando", "version", version.App)
	if err := ui.RunGUI(ui.Options{Dir: *dir, Hidden: *hidden, Log: log}); err != nil {
		log.Error(err.Error())
		ui.ShowError("LanChat no pudo iniciar:\n\n" + err.Error())
		os.Exit(1)
	}
}

// openLog abre lanchat.log para agregar; si creció demasiado, lo rota.
func openLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, logFileName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogSize {
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
