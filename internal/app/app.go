// Package app une descubrimiento, almacenamiento, servidor entre equipos y chat.
// Es lo único que usa la interfaz (consola ahora, web en el Hito 3).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/config"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/peer"
	"github.com/AEROGU/lanchat/internal/store"
)

const maxNameLen = 64

type Options struct {
	// Dir es la carpeta de datos; vacío = %APPDATA%\LanChat.
	Dir string
	// Hostname vacío = el del sistema.
	Hostname string
	Log      *slog.Logger
	// HTTPAddr y Tune permiten a las pruebas usar loopback y puertos libres.
	HTTPAddr string
	Tune     func(*discovery.Config)
}

type App struct {
	dir   string
	host  string
	log   *slog.Logger
	store *store.Store
	srv   *peer.Server
	disc  *discovery.Service
	chat  *chat.Service

	// events lleva discovery.Event y chat.Event.
	events chan any

	mu  sync.Mutex
	cfg *config.Config
}

func New(o Options) (a *App, err error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Dir == "" {
		if o.Dir, err = config.DefaultDir(); err != nil {
			return nil, err
		}
	}
	cfg, err := config.Load(o.Dir)
	if err != nil {
		return nil, err
	}
	if o.Hostname == "" {
		if o.Hostname, err = os.Hostname(); err != nil {
			o.Log.Warn("no se pudo leer el hostname; se mostrará la IP", "err", err)
		}
	}

	var closers []func()
	defer func() {
		if err != nil {
			for i := len(closers) - 1; i >= 0; i-- {
				closers[i]()
			}
		}
	}()

	st, err := store.Open(filepath.Join(o.Dir, "lanchat.db"))
	if err != nil {
		return nil, err
	}
	closers = append(closers, func() { st.Close() })

	addr := o.HTTPAddr
	if addr == "" {
		addr = fmt.Sprintf(":%d", cfg.HTTPPort)
	}
	srv, err := peer.Listen(addr)
	if err != nil {
		return nil, err
	}
	closers = append(closers, func() { srv.Close() })

	dcfg := discovery.Config{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Hostname:    o.Hostname,
		UDPPort:     cfg.UDPPort,
		HTTPPort:    srv.Port(),
		ManualPeers: cfg.ManualPeers,
	}
	if o.Tune != nil {
		o.Tune(&dcfg)
	}
	disc, err := discovery.New(dcfg, o.Log)
	if err != nil {
		return nil, err
	}

	ch := chat.New(chat.Identity{ID: cfg.ID, Hostname: o.Hostname, Name: disc.Name},
		st, disc, peer.NewClient(), o.Log)
	ch.Register(srv)

	return &App{
		dir:    o.Dir,
		host:   o.Hostname,
		log:    o.Log,
		store:  st,
		srv:    srv,
		disc:   disc,
		chat:   ch,
		events: make(chan any, 256),
		cfg:    cfg,
	}, nil
}

// Events entrega discovery.Event y chat.Event. Debe leerse hasta que se cierre.
func (a *App) Events() <-chan any { return a.events }

// Run funciona hasta que ctx se cancele; luego se despide de la red y cierra todo.
func (a *App) Run(ctx context.Context) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.srv.Serve() }()

	a.chat.Start()
	var fwd sync.WaitGroup
	fwd.Add(1)
	go func() {
		defer fwd.Done()
		for ev := range a.chat.Events() {
			a.events <- ev
		}
	}()

	discErr := make(chan error, 1)
	go func() { discErr <- a.disc.Run(ctx) }()
	for ev := range a.disc.Events() {
		a.onPeer(ev)
		a.events <- ev
	}
	err := <-discErr

	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.srv.Shutdown(sctx)
	a.chat.Close()
	fwd.Wait()
	close(a.events)
	a.store.Close()

	return errors.Join(err, <-serveErr)
}

func (a *App) onPeer(ev discovery.Event) {
	p := ev.Peer
	if err := a.store.UpsertPeer(context.Background(), store.Peer{
		ID: p.ID, Name: p.Name, Hostname: p.Hostname, IP: p.IP.String(), LastSeen: p.LastSeen,
	}); err != nil {
		a.log.Error("guardando contacto", "peer", p.ID, "err", err)
	}
	if ev.Type == discovery.PeerOnline {
		a.chat.Flush(p.ID)
	}
}

// Self describe a este equipo.
type Self struct {
	ID       string
	Name     string
	Hostname string
	Dir      string
}

func (a *App) Self() Self {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Self{ID: a.cfg.ID, Name: a.cfg.Name, Hostname: a.host, Dir: a.dir}
}

// Contact es un equipo conocido, conectado o no.
type Contact struct {
	ID       string
	Name     string
	Hostname string
	IP       string
	Alias    string
	Online   bool
	LastSeen time.Time
}

// DisplayName: alias local > nombre elegido por el otro > hostname > IP.
func (c Contact) DisplayName() string {
	for _, s := range []string{c.Alias, c.Name, c.Hostname, c.IP} {
		if s != "" {
			return s
		}
	}
	return c.ID
}

// Detail es "HOSTNAME · IP", siempre visible bajo el nombre.
func (c Contact) Detail() string {
	if c.Hostname == "" {
		return c.IP
	}
	return c.Hostname + " · " + c.IP
}

// Contacts devuelve los contactos: primero los conectados, luego por nombre.
func (a *App) Contacts(ctx context.Context) ([]Contact, error) {
	recs, err := a.store.Peers(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]*Contact{}
	var out []*Contact
	for _, r := range recs {
		c := &Contact{ID: r.ID, Name: r.Name, Hostname: r.Hostname, IP: r.IP, Alias: r.Alias, LastSeen: r.LastSeen}
		byID[r.ID] = c
		out = append(out, c)
	}
	for _, p := range a.disc.Peers() {
		c, ok := byID[p.ID]
		if !ok {
			c = &Contact{ID: p.ID}
			out = append(out, c)
		}
		c.Name, c.Hostname, c.IP = p.Name, p.Hostname, p.IP.String()
		c.Online, c.LastSeen = p.Online, p.LastSeen
	}

	res := make([]Contact, len(out))
	for i, c := range out {
		res[i] = *c
	}
	sort.SliceStable(res, func(i, j int) bool {
		if res[i].Online != res[j].Online {
			return res[i].Online
		}
		return strings.ToLower(res[i].DisplayName()) < strings.ToLower(res[j].DisplayName())
	})
	return res, nil
}

// Contact busca un contacto por ID.
func (a *App) Contact(ctx context.Context, id string) (Contact, bool, error) {
	r, ok, err := a.store.Peer(ctx, id)
	if err != nil {
		return Contact{}, false, err
	}
	c := Contact{ID: r.ID, Name: r.Name, Hostname: r.Hostname, IP: r.IP, Alias: r.Alias, LastSeen: r.LastSeen}
	if p, live := a.disc.Peer(id); live {
		c.ID, c.Name, c.Hostname, c.IP = p.ID, p.Name, p.Hostname, p.IP.String()
		c.Online, c.LastSeen = p.Online, p.LastSeen
		ok = true
	}
	return c, ok, nil
}

// Send envía (o deja en cola) un mensaje para el contacto.
func (a *App) Send(ctx context.Context, peerID, body string) (store.Message, error) {
	if _, ok, err := a.Contact(ctx, peerID); err != nil {
		return store.Message{}, err
	} else if !ok {
		return store.Message{}, fmt.Errorf("contacto %s desconocido", peerID)
	}
	return a.chat.Send(ctx, peerID, body)
}

// History devuelve hasta limit mensajes anteriores a before (cero = los últimos).
func (a *App) History(ctx context.Context, peerID string, before time.Time, limit int) ([]store.Message, error) {
	return a.store.History(ctx, peerID, before, limit)
}

// SetName cambia el nombre con el que los demás ven a este equipo.
func (a *App) SetName(name string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg.Name = name
	err = a.cfg.Save(a.dir)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	a.disc.SetName(name)
	return nil
}

// SetAlias pone (o quita, con "") el nombre local de un contacto.
func (a *App) SetAlias(ctx context.Context, peerID, alias string) error {
	alias, err := cleanName(alias)
	if err != nil {
		return err
	}
	return a.store.SetAlias(ctx, peerID, alias)
}

func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxNameLen {
		return "", fmt.Errorf("máximo %d caracteres", maxNameLen)
	}
	return s, nil
}
