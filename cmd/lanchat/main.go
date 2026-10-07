// Comando lanchat. Por ahora (Hito 1) solo muestra en consola los equipos que
// aparecen y desaparecen de la red.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/AEROGU/lanchat/internal/config"
	"github.com/AEROGU/lanchat/internal/discovery"
)

func main() {
	dir := flag.String("dir", "", `carpeta de configuración (por defecto %APPDATA%\LanChat)`)
	debug := flag.Bool("debug", false, "mostrar mensajes de depuración")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(*dir, log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(dir string, log *slog.Logger) error {
	if dir == "" {
		d, err := config.DefaultDir()
		if err != nil {
			return err
		}
		dir = d
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}
	host, err := os.Hostname()
	if err != nil {
		log.Warn("no se pudo leer el hostname; se mostrará la IP", "err", err)
	}

	svc, err := discovery.New(discovery.Config{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Hostname:    host,
		UDPPort:     cfg.UDPPort,
		HTTPPort:    cfg.HTTPPort,
		ManualPeers: cfg.ManualPeers,
	}, log)
	if err != nil {
		return err
	}

	log.Info("LanChat iniciado", "id", cfg.ID, "host", host, "udp", cfg.UDPPort, "config", dir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	printed := make(chan struct{})
	go func() {
		defer close(printed)
		for ev := range svc.Events() {
			fmt.Printf("[%-7s] %s\n", ev.Type, label(ev.Peer))
		}
	}()

	err = svc.Run(ctx)
	<-printed
	return err
}

// label: "Nombre (HOSTNAME · IP)"; sin nombre ni hostname se usa la IP.
func label(p discovery.Peer) string {
	ip := p.IP.String()
	switch {
	case p.Name != "" && p.Hostname != "":
		return fmt.Sprintf("%s (%s · %s)", p.Name, p.Hostname, ip)
	case p.Name != "":
		return fmt.Sprintf("%s (%s)", p.Name, ip)
	case p.Hostname != "":
		return fmt.Sprintf("%s (%s)", p.Hostname, ip)
	}
	return ip
}
