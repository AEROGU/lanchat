// Package app une descubrimiento, almacenamiento, servidor entre equipos y chat.
// Es lo único que usa la interfaz (consola ahora, web en el Hito 3).
package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/config"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/peer"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

const (
	dbFileName = "lanchat.db"
	// shutdownTimeout: espera máxima a que terminen las peticiones en curso al cerrar.
	shutdownTimeout = 5 * time.Second
	eventBuffer     = 256
)

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

	st, err := store.Open(filepath.Join(o.Dir, dbFileName))
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
		events: make(chan any, eventBuffer),
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
	discRunErr := <-discErr

	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := a.srv.Shutdown(sctx)
	a.chat.Close()
	fwd.Wait()
	close(a.events)

	return errors.Join(discRunErr, shutdownErr, <-serveErr, a.store.Close())
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
	// AppVersion solo se conoce si el equipo se vio desde que arrancó LanChat.
	AppVersion string
	Online     bool
	LastSeen   time.Time
	// Unread es la cantidad de mensajes suyos sin leer.
	Unread int
}

func contactFromStore(r store.Peer) Contact {
	return Contact{ID: r.ID, Name: r.Name, Hostname: r.Hostname, IP: r.IP, Alias: r.Alias, LastSeen: r.LastSeen}
}

// overlay reemplaza los datos guardados con los que el equipo anuncia ahora;
// el alias es local y se conserva.
func (c *Contact) overlay(p discovery.Peer) {
	c.ID, c.Name, c.Hostname, c.IP = p.ID, p.Name, p.Hostname, p.IP.String()
	c.AppVersion, c.Online, c.LastSeen = p.AppVersion, p.Online, p.LastSeen
}

// DisplayName: alias local > nombre elegido por el otro > hostname > IP.
func (c Contact) DisplayName() string {
	return cmp.Or(c.Alias, c.Name, c.Hostname, c.IP, c.ID)
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
	unread, err := a.store.UnreadCounts(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(recs))
	out := make([]Contact, 0, len(recs))
	for _, r := range recs {
		byID[r.ID] = len(out)
		out = append(out, contactFromStore(r))
	}
	for _, p := range a.disc.Peers() {
		i, ok := byID[p.ID]
		if !ok {
			i = len(out)
			out = append(out, Contact{})
		}
		out[i].overlay(p)
	}
	for i := range out {
		out[i].Unread = unread[out[i].ID]
	}

	slices.SortStableFunc(out, func(x, y Contact) int {
		if x.Online != y.Online {
			if x.Online {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(x.DisplayName()), strings.ToLower(y.DisplayName()))
	})
	return out, nil
}

// Contact busca un contacto por ID.
func (a *App) Contact(ctx context.Context, id string) (Contact, bool, error) {
	r, ok, err := a.store.Peer(ctx, id)
	if err != nil {
		return Contact{}, false, err
	}
	c := contactFromStore(r)
	if p, live := a.disc.Peer(id); live {
		c.overlay(p)
		ok = true
	}
	unread, err := a.store.UnreadCounts(ctx)
	if err != nil {
		return Contact{}, false, err
	}
	c.Unread = unread[id]
	return c, ok, nil
}

// TotalUnread es la cantidad de mensajes sin leer de todos los contactos.
func (a *App) TotalUnread(ctx context.Context) (int, error) {
	unread, err := a.store.UnreadCounts(ctx)
	total := 0
	for _, n := range unread {
		total += n
	}
	return total, err
}

// MarkRead marca como leídos los mensajes de peerID; changed indica si había
// alguno sin leer.
func (a *App) MarkRead(ctx context.Context, peerID string) (changed bool, err error) {
	return a.store.MarkRead(ctx, peerID)
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

// History devuelve hasta limit mensajes anteriores al mensaje beforeID
// ("" = los últimos), en orden cronológico.
func (a *App) History(ctx context.Context, peerID, beforeID string, limit int) ([]store.Message, error) {
	return a.store.History(ctx, peerID, beforeID, limit)
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

// ManualPeers devuelve los equipos de otras subredes configurados a mano.
func (a *App) ManualPeers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.cfg.ManualPeers)
}

// SetManualPeers reemplaza los equipos manuales (se ignoran líneas vacías y
// repetidas), los aplica sin reiniciar y los guarda en config.json.
func (a *App) SetManualPeers(peers []string) error {
	var clean []string
	for _, p := range peers {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(clean, p) {
			clean = append(clean, p)
		}
	}
	if clean == nil {
		clean = []string{}
	}
	if err := a.disc.SetManualPeers(clean); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.ManualPeers = clean
	return a.cfg.Save(a.dir)
}

func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	return s, protocol.ValidateName(s)
}
