package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/peer"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/thumb"
)

// Fotos de perfil. Cada equipo anuncia el identificador de la suya
// (protocol.PhotoID) junto con su nombre; quien vea uno distinto del que
// tiene guardado la descarga de protocol.RoutePhoto por la conexión TLS, y
// si deja de anunciarla, la borra. Sin foto, la interfaz muestra las
// iniciales sobre un color.

// photoRetry: tras fallar una descarga, cuánto se espera antes de volver a
// intentar con la misma foto (un equipo que entrega otra cosa no se pide en
// cada anuncio).
const photoRetry = time.Minute

// PhotoChanged es un evento: cambió la foto guardada de un contacto.
type PhotoChanged struct{ PeerID string }

// photos lleva el identificador de cada foto guardada (también la propia) y
// las descargas en curso o fallidas.
type photos struct {
	mu       sync.Mutex
	ids      map[string]string    // equipo -> foto guardada
	fetching map[string]string    // equipo -> foto que se está descargando
	failed   map[string]failedGet // equipo -> última descarga fallida
}

type failedGet struct {
	id    string
	until time.Time
}

func newPhotos(ids map[string]string) *photos {
	return &photos{ids: ids, fetching: map[string]string{}, failed: map[string]failedGet{}}
}

func (p *photos) get(peerID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ids[peerID]
}

func (p *photos) set(peerID, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id == "" {
		delete(p.ids, peerID)
	} else {
		p.ids[peerID] = id
	}
}

// reset olvida todas las fotos (se borraron los datos).
func (p *photos) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids = map[string]string{}
	p.failed = map[string]failedGet{}
}

// SetPhoto pone como foto de perfil la imagen que lee r (JPG, PNG, GIF o
// WebP): se recorta el cuadrado del centro, se reduce y se anuncia.
func (a *App) SetPhoto(ctx context.Context, r io.ReadSeeker) error {
	data, err := thumb.Avatar(r)
	if err != nil {
		return fmt.Errorf("no se pudo usar la imagen como foto de perfil (usa una JPG, PNG, GIF o WebP): %w", err)
	}
	id := protocol.PhotoID(data)
	self := a.Self().ID
	if err := a.store.SetPhoto(ctx, self, id, data); err != nil {
		return err
	}
	a.photos.set(self, id)
	a.disc.SetPhoto(id)
	return nil
}

// RemovePhoto quita la foto de perfil: los demás vuelven a ver las iniciales.
func (a *App) RemovePhoto(ctx context.Context) error {
	self := a.Self().ID
	if err := a.store.DeletePhoto(ctx, self); err != nil {
		return err
	}
	a.photos.set(self, "")
	a.disc.SetPhoto("")
	return nil
}

// Photo devuelve la foto guardada de un equipo (también la propia) y su
// identificador; ok es false si no tiene.
func (a *App) Photo(ctx context.Context, peerID string) (id string, data []byte, ok bool, err error) {
	return a.store.Photo(ctx, peerID)
}

// handlePhoto entrega la foto propia a otro equipo. Como el nombre, que va
// en los anuncios, la puede pedir cualquier equipo LanChat de la red.
func (a *App) handlePhoto(w http.ResponseWriter, r *http.Request) {
	_, data, ok, err := a.store.Photo(r.Context(), a.Self().ID)
	switch {
	case err != nil:
		http.Error(w, "error interno", http.StatusInternalServerError)
	case !ok:
		http.Error(w, "sin foto", http.StatusNotFound)
	default:
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(data)
	}
}

// syncPhoto pone al día la foto guardada de p según la que anuncia: la
// descarga en segundo plano si es otra (wg permite esperarla al cerrar) o la
// borra si ya no anuncia ninguna.
func (a *App) syncPhoto(ctx context.Context, wg *sync.WaitGroup, p discovery.Peer) {
	if !p.Online || !p.Compatible() {
		return
	}
	ph := a.photos
	ph.mu.Lock()
	have := ph.ids[p.ID]
	f, failed := ph.failed[p.ID]
	switch {
	case p.Photo == have || ph.fetching[p.ID] == p.Photo && p.Photo != "":
		ph.mu.Unlock()
		return
	case failed && f.id == p.Photo && time.Now().Before(f.until):
		ph.mu.Unlock()
		return
	case p.Photo == "":
		delete(ph.ids, p.ID)
		ph.mu.Unlock()
		if err := a.store.DeletePhoto(ctx, p.ID); err != nil {
			a.log.Error("borrando foto de perfil", "peer", p.ID, "err", err)
		}
		a.events <- PhotoChanged{p.ID}
		return
	}
	ph.fetching[p.ID] = p.Photo
	ph.mu.Unlock()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := a.fetchPhoto(ctx, p)
		ph.mu.Lock()
		if ph.fetching[p.ID] == p.Photo {
			delete(ph.fetching, p.ID)
		}
		if err != nil {
			ph.failed[p.ID] = failedGet{p.Photo, time.Now().Add(photoRetry)}
		} else {
			delete(ph.failed, p.ID)
		}
		ph.mu.Unlock()
		if err != nil {
			a.log.Debug("descargando foto de perfil", "peer", p.ID, "err", err)
			return
		}
		a.events <- PhotoChanged{p.ID}
	}()
}

// fetchPhoto descarga y guarda la foto que anuncia p.
func (a *App) fetchPhoto(ctx context.Context, p discovery.Peer) error {
	pctx, err := peer.ContextFor(ctx, a.store, p.ID)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, "https://"+p.HTTPAddr().String()+protocol.RoutePhoto, nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, thumb.MaxBytes+1))
	switch {
	case err != nil:
		return err
	case resp.StatusCode != http.StatusOK:
		return errors.New(resp.Status)
	case !thumb.ValidAvatar(data):
		return errors.New("la foto recibida no es válida")
	case protocol.PhotoID(data) != p.Photo:
		// Pudo cambiarla justo ahora: su próximo anuncio traerá la nueva.
		return errors.New("la foto recibida no es la anunciada")
	}
	if err := a.store.SetPhoto(ctx, p.ID, p.Photo, data); err != nil {
		return err
	}
	a.photos.set(p.ID, p.Photo)
	return nil
}
