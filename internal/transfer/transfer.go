// Package transfer envía y recibe archivos ofrecidos por chat.
//
// Flujo:
//  1. El remitente ofrece archivos: se envía un mensaje de chat con la oferta
//     (nombres, tamaños y un token secreto). Nada se transfiere todavía.
//  2. El destinatario acepta y descarga cada archivo del remitente con
//     GET protocol.RouteFile (admite Range para reanudar desde un .part).
//  3. Al terminar cada archivo compara su SHA-256 con el que manda el
//     remitente al final (trailer) y lo confirma con RouteFileDone. Desde
//     ese momento el remitente responde 410 a cualquier otra descarga: para
//     recibirlo de nuevo hay que volver a enviarlo.
//
// Las ofertas caducan a las 24 h. Rechazar o cancelar se avisa al otro equipo
// con RouteTransferState (si no está conectado, lo descubrirá con un 410).
package transfer

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/identity"
	"github.com/AEROGU/lanchat/internal/peer"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/thumb"
)

const (
	// offerTTL es cuánto tiempo puede descargarse una oferta.
	offerTTL = 24 * time.Hour
	// maintenanceInterval: cada cuánto se marcan las ofertas caducadas.
	maintenanceInterval = time.Minute
	// progressInterval limita la frecuencia de los eventos de progreso.
	progressInterval = 250 * time.Millisecond
	// stallTimeout: una descarga sin datos durante este tiempo se da por caída.
	stallTimeout = 30 * time.Second
	// notifyTimeout limita los avisos cortos al otro equipo.
	notifyTimeout  = 10 * time.Second
	copyBufferSize = 256 << 10
	partSuffix     = ".lanchat-part"
	// downloadSubdir se crea dentro de Descargas.
	downloadSubdir = "LanChat"
	eventBuffer    = 256
	maxErrorBody   = 1 << 10
	// stopWait: cuánto se espera a que una transferencia cancelada suelte sus archivos.
	stopWait = 5 * time.Second
	// maxStoredError recorta el error que se guarda y se muestra.
	maxStoredError = 500
)

// Directory dice dónde está cada equipo; lo implementa discovery.Service.
type Directory interface {
	Peer(id string) (discovery.Peer, bool)
}

// OfferSender envía la oferta como mensaje de chat; lo implementa chat.Service.
type OfferSender interface {
	SendOffer(ctx context.Context, peerID, body string, t store.Transfer) (store.Message, error)
}

type Config struct {
	// StagingDir guarda copias de los archivos arrastrados a la ventana (el
	// navegador no da su ruta original). Se borran al terminar la oferta.
	StagingDir string
	// DownloadDir devuelve la carpeta elegida por el usuario ("" = Descargas\LanChat).
	DownloadDir func() string
	// Identity es la identidad TLS de este equipo para hablar con los demás.
	Identity *identity.Identity
}

type EventType int

const (
	// TransferChanged: cambió el estado o algún archivo de la transferencia.
	TransferChanged EventType = iota
	// TransferProgress: avance de una descarga o envío en curso.
	TransferProgress
)

type Progress struct {
	ID    string
	Done  int64
	Total int64
	// Rate en bytes por segundo.
	Rate float64
}

type Event struct {
	Type     EventType
	Transfer store.Transfer // en TransferChanged
	Progress Progress       // en TransferProgress
}

type Service struct {
	cfg    Config
	store  *store.Store
	dir    Directory
	sender OfferSender
	client *http.Client
	log    *slog.Logger
	events chan Event

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	closed bool
	// active son las descargas (entrantes) y envíos (salientes) en curso.
	active map[string]*activeOp
}

func New(cfg Config, st *store.Store, dir Directory, sender OfferSender, log *slog.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		cfg:    cfg,
		store:  st,
		dir:    dir,
		sender: sender,
		client: peer.NewStreamClient(cfg.Identity),
		log:    log,
		events: make(chan Event, eventBuffer),
		ctx:    ctx,
		cancel: cancel,
		active: map[string]*activeOp{},
	}
}

// Register agrega las rutas de transferencia al servidor entre equipos.
func (s *Service) Register(srv *peer.Server) {
	srv.Handle("GET "+protocol.RouteFile, http.HandlerFunc(s.handleFile))
	srv.Handle("POST "+protocol.RouteFileDone, http.HandlerFunc(s.handleFileDone))
	srv.Handle("POST "+protocol.RouteTransferState, http.HandlerFunc(s.handleState))
}

// Events debe leerse hasta que se cierre (en Close).
func (s *Service) Events() <-chan Event { return s.events }

// Start pone al día las transferencias que quedaron a medias y lanza el
// mantenimiento periódico.
func (s *Service) Start() {
	ctx := s.ctx
	interrupted, err := s.store.TransfersInState(ctx, false, store.TransferDownloading)
	if err != nil {
		s.log.Error("buscando descargas interrumpidas", "err", err)
	}
	for _, t := range interrupted {
		s.setState(ctx, t.ID, store.TransferFailed, "Se interrumpió al cerrar LanChat. Puedes reintentar.")
	}
	s.maintain()
	s.cleanOrphanStaging()
	s.goSafe(func() {
		tick := time.NewTicker(maintenanceInterval)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
				s.maintain()
			}
		}
	})
}

// Stop corta las descargas y envíos en curso. Llamarlo antes de apagar el
// servidor HTTP, para que este no espere a los envíos largos.
func (s *Service) Stop() { s.cancel() }

// Close espera a que terminen las tareas y cierra Events. Llamarlo después de
// apagar el servidor HTTP.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	close(s.events)
}

// ---------- Remitente ----------

// Offer ofrece archivos a peerID (ver ExpandPaths para carpetas).
func (s *Service) Offer(ctx context.Context, peerID string, items []Item) (store.Message, error) {
	if len(items) == 0 {
		return store.Message{}, errors.New("no hay archivos para enviar")
	}
	if len(items) > protocol.MaxOfferFiles {
		return store.Message{}, errTooManyFiles
	}
	t := store.Transfer{
		State:     store.TransferOffered,
		Token:     newToken(),
		ExpiresAt: time.Now().Add(offerTTL),
	}
	for i, it := range items {
		fi, err := os.Stat(it.Path)
		if err != nil {
			return store.Message{}, err
		}
		if !fi.Mode().IsRegular() {
			return store.Message{}, fmt.Errorf("%s no es un archivo", fi.Name())
		}
		if err := errors.Join(protocol.ValidateFileName(fi.Name()), protocol.ValidateRelDir(it.Dir)); err != nil {
			return store.Message{}, fmt.Errorf("%s: %w", fi.Name(), err)
		}
		t.Files = append(t.Files, store.TransferFile{
			Index: i, Name: fi.Name(), Size: fi.Size(), ModTime: fi.ModTime(), Path: it.Path, Dir: it.Dir,
		})
	}
	addThumbs(t.Files)
	return s.sender.SendOffer(ctx, peerID, offerSummary(t.Files, t.TotalSize()), t)
}

// NewStaging crea una carpeta temporal para los archivos arrastrados a la ventana.
func (s *Service) NewStaging() (string, error) {
	dir := filepath.Join(s.cfg.StagingDir, newToken()[:16])
	return dir, os.MkdirAll(dir, 0o700)
}

// SaveStaged copia un archivo arrastrado a la carpeta temporal dir, dentro de
// su subcarpeta relDir ("" o "Proyecto/planos").
func (s *Service) SaveStaged(dir, relDir, name string, r io.Reader) (string, error) {
	if err := errors.Join(protocol.ValidateFileName(name), protocol.ValidateRelDir(relDir)); err != nil {
		return "", err
	}
	target := filepath.Join(dir, filepath.FromSlash(relDir))
	if err := os.MkdirAll(target, 0o700); err != nil {
		return "", err
	}
	path, err := uniquePath(target, safeFileName(name))
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.CopyBuffer(f, r, make([]byte, copyBufferSize)); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}

// DiscardStaging borra una carpeta temporal que no llegó a ofrecerse.
func (s *Service) DiscardStaging(dir string) {
	if s.stagingRoot(dir) == filepath.Clean(dir) {
		os.RemoveAll(dir)
	}
}

// cleanStaging borra las copias temporales de una oferta que ya terminó.
func (s *Service) cleanStaging(t store.Transfer) {
	for _, f := range t.Files {
		if root := s.stagingRoot(f.Path); root != "" {
			os.RemoveAll(root)
		}
	}
}

// handleFile entrega un archivo ofrecido. Calcula el SHA-256 del archivo
// completo (también la parte que se salta al reanudar) y lo manda en el
// trailer protocol.HashTrailer.
func (s *Service) handleFile(w http.ResponseWriter, r *http.Request) {
	t, f, ok := s.authorizeFile(w, r)
	if !ok {
		return
	}
	file, err := os.Open(f.Path)
	if err != nil {
		http.Error(w, "el archivo ya no existe en el equipo del remitente", http.StatusConflict)
		return
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil || fi.Size() != f.Size || !fi.ModTime().Equal(f.ModTime) {
		http.Error(w, "el archivo cambió desde que se ofreció; pide que lo vuelvan a enviar", http.StatusConflict)
		return
	}
	offset, err := parseRange(r.Header.Get("Range"), f.Size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
		return
	}

	if t.State == store.TransferOffered {
		s.setState(r.Context(), t.ID, store.TransferDownloading, "")
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel) // al cerrar LanChat
	defer stop()
	// Una petición nueva del mismo archivo reemplaza a una que quedó colgada.
	s.stopActive(t.ID)
	op, ok := s.startActive(t.ID, cancel)
	if !ok {
		http.Error(w, "descarga en curso", http.StatusConflict)
		return
	}
	defer func() {
		file.Close() // antes de avisar que terminó: quien espera puede querer borrarlo
		s.endActive(t.ID, op)
	}()

	h := sha256.New()
	if _, err := io.CopyN(h, file, offset); err != nil {
		http.Error(w, "leyendo el archivo", http.StatusInternalServerError)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Trailer", protocol.HashTrailer)
	status := http.StatusOK
	if offset > 0 {
		hd.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, f.Size-1, f.Size))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)

	prog := s.newProgress(t, doneBefore(t, f.Index)+offset)
	src := &ctxReader{ctx: ctx, r: file}
	dst := io.MultiWriter(w, h, prog)
	if _, err := io.CopyBuffer(dst, src, make([]byte, copyBufferSize)); err != nil {
		s.log.Debug("envío interrumpido", "transfer", t.ID, "file", f.Index, "err", err)
		return
	}
	prog.flush()
	hd.Set(protocol.HashTrailer, hex.EncodeToString(h.Sum(nil)))
}

// handleFileDone: el destinatario confirma que el archivo llegó íntegro.
func (s *Service) handleFileDone(w http.ResponseWriter, r *http.Request) {
	t, f, ok := s.authorizeFile(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if err := s.store.MarkFileDone(ctx, t.ID, f.Index, ""); err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	t, _, err := s.store.Transfer(ctx, t.ID)
	if err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	if allDone(t) {
		s.cleanStaging(t)
		s.setState(ctx, t.ID, store.TransferCompleted, "")
	} else {
		s.emitChanged(t)
	}
	w.WriteHeader(http.StatusNoContent)
}

// authorizeFile valida token, estado, caducidad e índice; si algo falla ya
// respondió al cliente.
func (s *Service) authorizeFile(w http.ResponseWriter, r *http.Request) (store.Transfer, store.TransferFile, bool) {
	t, ok := s.authorize(w, r, true)
	if !ok {
		return t, store.TransferFile{}, false
	}
	if t.State.Final() {
		http.Error(w, goneMessage(t.State), http.StatusGone)
		return t, store.TransferFile{}, false
	}
	if !time.Now().Before(t.ExpiresAt) {
		s.expire(r.Context(), t)
		http.Error(w, goneMessage(store.TransferExpired), http.StatusGone)
		return t, store.TransferFile{}, false
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || idx < 0 || idx >= len(t.Files) {
		http.Error(w, "archivo inexistente", http.StatusNotFound)
		return t, store.TransferFile{}, false
	}
	f := t.Files[idx]
	if f.Done {
		http.Error(w, "este archivo ya se descargó; para recibirlo otra vez pide que lo reenvíen", http.StatusGone)
		return t, f, false
	}
	return t, f, true
}

// authorize busca la transferencia y comprueba el token. outgoingOnly limita
// a ofertas hechas por este equipo.
func (s *Service) authorize(w http.ResponseWriter, r *http.Request, outgoingOnly bool) (store.Transfer, bool) {
	t, ok, err := s.store.Transfer(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return t, false
	}
	if !ok || (outgoingOnly && !t.Outgoing) {
		http.Error(w, "la oferta ya no está disponible", http.StatusNotFound)
		return t, false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(protocol.TokenHeader)), []byte(t.Token)) != 1 {
		http.Error(w, "token inválido", http.StatusForbidden)
		return t, false
	}
	// Además del token, quien pide debe ser el otro equipo de la transferencia.
	if pinned, err := s.store.PinnedFingerprint(r.Context(), t.PeerID); err != nil || pinned != peer.ClientFingerprint(r) {
		http.Error(w, "la identidad no corresponde a esta transferencia", http.StatusForbidden)
		return t, false
	}
	return t, true
}

func goneMessage(st store.TransferState) string {
	switch st {
	case store.TransferCompleted:
		return "ya se descargó; para recibirlo otra vez pide que lo reenvíen"
	case store.TransferCanceled:
		return "el remitente canceló el envío"
	case store.TransferRejected:
		return "la oferta fue rechazada"
	}
	return "la oferta caducó; pide que la vuelvan a enviar"
}

// handleState recibe un rechazo o cancelación del otro equipo.
func (s *Service) handleState(w http.ResponseWriter, r *http.Request) {
	t, ok := s.authorize(w, r, false)
	if !ok {
		return
	}
	var req struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxErrorBody)).Decode(&req); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}
	var st store.TransferState
	switch {
	case req.State == protocol.StateRejected && t.Outgoing:
		st = store.TransferRejected
	case req.State == protocol.StateCanceled:
		st = store.TransferCanceled
	default:
		http.Error(w, "estado inválido", http.StatusBadRequest)
		return
	}
	if !t.State.Final() {
		s.stopActive(t.ID)
		s.discardLocal(t)
		s.setState(r.Context(), t.ID, st, "")
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Destinatario ----------

// Accept empieza (o reintenta) la descarga de una oferta recibida.
func (s *Service) Accept(ctx context.Context, id string) error {
	t, err := s.incoming(ctx, id)
	if err != nil {
		return err
	}
	if t.State != store.TransferOffered && t.State != store.TransferFailed {
		return fmt.Errorf("la oferta está %s", stateText(t.State))
	}
	if !time.Now().Before(t.ExpiresAt) {
		s.setState(ctx, id, store.TransferExpired, "")
		return errors.New("la oferta caducó; pide que la vuelvan a enviar")
	}
	dir := t.Dir
	if dir == "" {
		if dir, err = s.downloadDir(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("no se pudo crear la carpeta %s: %w", dir, err)
	}
	if free := freeSpace(dir); free >= 0 && free < remaining(t, dir) {
		return fmt.Errorf("no hay espacio suficiente en %s (faltan %s)", dir, humanSize(remaining(t, dir)-free))
	}

	dctx, cancel := context.WithCancel(s.ctx)
	op, ok := s.startActive(id, cancel)
	if !ok {
		cancel()
		return errors.New("ya se está descargando")
	}
	if err := s.prepareDirs(ctx, &t, dir); err != nil {
		s.endActive(id, op)
		cancel()
		return err
	}
	s.setState(ctx, id, store.TransferDownloading, "")
	s.goSafe(func() {
		defer s.endActive(id, op)
		defer cancel()
		s.download(dctx, t)
	})
	return nil
}

// Reject rechaza una oferta recibida y avisa al remitente.
func (s *Service) Reject(ctx context.Context, id string) error {
	t, err := s.incoming(ctx, id)
	if err != nil {
		return err
	}
	if t.State != store.TransferOffered && t.State != store.TransferFailed {
		return fmt.Errorf("la oferta está %s", stateText(t.State))
	}
	s.discardLocal(t)
	s.setState(ctx, id, store.TransferRejected, "")
	s.notifyState(t, protocol.StateRejected)
	return nil
}

// Cancel cancela una transferencia (enviada o recibida) y avisa al otro equipo.
func (s *Service) Cancel(ctx context.Context, id string) error {
	t, ok, err := s.store.Transfer(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("transferencia inexistente")
	}
	if t.State.Final() {
		return fmt.Errorf("la transferencia ya está %s", stateText(t.State))
	}
	s.stopActive(id)
	s.discardLocal(t)
	s.setState(ctx, id, store.TransferCanceled, "")
	s.notifyState(t, protocol.StateCanceled)
	return nil
}

// CancelAll cancela las transferencias sin terminar con peerID ("" = con
// todos), avisando al otro equipo; se usa antes de borrar conversaciones.
func (s *Service) CancelAll(ctx context.Context, peerID string) error {
	var errs []error
	for _, outgoing := range []bool{true, false} {
		ts, err := s.store.TransfersInState(ctx, outgoing,
			store.TransferOffered, store.TransferDownloading, store.TransferFailed)
		if err != nil {
			return err
		}
		for _, t := range ts {
			if peerID == "" || t.PeerID == peerID {
				errs = append(errs, s.Cancel(ctx, t.ID))
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Service) incoming(ctx context.Context, id string) (store.Transfer, error) {
	t, ok, err := s.store.Transfer(ctx, id)
	if err != nil {
		return t, err
	}
	if !ok || t.Outgoing {
		return t, errors.New("oferta inexistente")
	}
	return t, nil
}

func (s *Service) downloadDir() (string, error) {
	if d := cmp.Or(s.cfg.DownloadDir(), DefaultDownloadDir()); d != "" {
		return d, nil
	}
	return "", errors.New("no se encontró la carpeta Descargas; elige una carpeta en Ajustes")
}

// DefaultDownloadDir es la carpeta que se usa si el usuario no eligió otra
// ("" si no se encuentra la carpeta Descargas).
func DefaultDownloadDir() string {
	d, err := defaultDownloadDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, downloadSubdir)
}

// goneError: el remitente ya no ofrece el archivo (410/404).
type goneError struct {
	status int
	msg    string
}

func (e goneError) Error() string { return e.msg }

var errStalled = errors.New("la descarga se detuvo: no llegan datos del remitente")

func (s *Service) download(ctx context.Context, t store.Transfer) {
	prog := s.newProgress(t, 0)
	thumbs := 0 // miniaturas que ya hay (las manda el remitente)
	for _, f := range t.Files {
		if f.HasThumb {
			thumbs++
		}
	}
	for _, f := range t.Files {
		if f.Done {
			prog.add(f.Size)
			continue
		}
		p, ok := s.dir.Peer(t.PeerID)
		if !ok || !p.Online {
			s.fail(t, errors.New("el remitente no está conectado; reintenta cuando se conecte"))
			return
		}
		path, err := s.downloadFile(ctx, p, t, f, prog)
		if err != nil {
			if ctx.Err() != nil {
				return // cancelada por el usuario o al cerrar LanChat
			}
			s.fail(t, err)
			return
		}
		if err := s.store.MarkFileDone(s.ctx, t.ID, f.Index, path); err != nil {
			s.fail(t, err)
			return
		}
		// Si el remitente no mandó miniatura (versión anterior), se genera aquí.
		if !f.HasThumb && thumbs < thumb.MaxPerOffer && thumb.Supported(f.Name) {
			if b := safeThumb(path); b != nil && s.store.SetThumb(s.ctx, t.ID, f.Index, b) == nil {
				thumbs++
			}
		}
		s.notify(t, protocol.PathFileDone(t.ID, f.Index), nil)
		if tr, ok, err := s.store.Transfer(s.ctx, t.ID); err == nil && ok {
			s.emitChanged(tr)
		}
	}
	prog.flush()
	s.setState(s.ctx, t.ID, store.TransferCompleted, "")
}

func (s *Service) fail(t store.Transfer, err error) {
	// Si el remitente canceló, su aviso pudo llegar antes que este error.
	if cur, ok, _ := s.store.Transfer(s.ctx, t.ID); ok && cur.State.Final() {
		return
	}
	var gone goneError
	if errors.As(err, &gone) {
		st := store.TransferExpired
		if strings.Contains(gone.msg, "cancel") {
			st = store.TransferCanceled
		}
		s.discardLocal(t)
		s.setState(s.ctx, t.ID, st, gone.msg)
		return
	}
	msg := err.Error()
	if len(msg) > maxStoredError {
		msg = truncateUTF8(msg, maxStoredError)
	}
	s.setState(s.ctx, t.ID, store.TransferFailed, msg)
}

func partPath(t store.Transfer, f store.TransferFile) string {
	idPrefix := t.ID
	if len(idPrefix) > 8 {
		idPrefix = idPrefix[:8]
	}
	name := truncateName(safeFileName(f.Name), maxSavedNameBytes-len(partSuffix)-12)
	return filepath.Join(fileDir(t, f), fmt.Sprintf("%s.%s-%d%s", name, idPrefix, f.Index, partSuffix))
}

// downloadFile descarga un archivo a su .part (reanudando si existe), verifica
// tamaño y SHA-256, y lo renombra a su nombre final sin pisar otros archivos.
func (s *Service) downloadFile(ctx context.Context, p discovery.Peer, t store.Transfer, f store.TransferFile, prog *progress) (string, error) {
	if err := os.MkdirAll(fileDir(t, f), 0o700); err != nil {
		return "", err
	}
	part := partPath(t, f)
	out, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()

	offset, err := out.Seek(0, io.SeekEnd)
	if err != nil {
		return "", err
	}
	if offset > f.Size {
		if err := out.Truncate(0); err != nil {
			return "", err
		}
		offset = 0
	}
	h := sha256.New()
	if offset > 0 {
		if _, err := out.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		if _, err := io.CopyN(h, out, offset); err != nil {
			return "", err
		}
	}

	fctx, fcancel := context.WithCancelCause(ctx)
	defer fcancel(nil)
	pctx, err := peer.ContextFor(fctx, s.store, t.PeerID)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(pctx, http.MethodGet,
		"https://"+p.HTTPAddr().String()+protocol.PathFile(t.ID, f.Index), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(protocol.TokenHeader, t.Token)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("no se pudo conectar con el remitente: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if offset > 0 { // el remitente no reanudó: se empieza de cero
			if err := out.Truncate(0); err != nil {
				return "", err
			}
			offset = 0
			h.Reset()
		}
	case http.StatusPartialContent:
		if start, ok := contentRangeStart(resp.Header.Get("Content-Range")); !ok || start != offset {
			return "", errors.New("el remitente respondió un rango inesperado")
		}
	case http.StatusGone, http.StatusNotFound:
		return "", goneError{resp.StatusCode, readError(resp)}
	default:
		return "", fmt.Errorf("el remitente respondió: %s", readError(resp))
	}
	if _, err := out.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	prog.add(offset)

	stall := time.AfterFunc(stallTimeout, func() { fcancel(errStalled) })
	defer stall.Stop()
	body := &activityReader{r: resp.Body, onRead: func() { stall.Reset(stallTimeout) }}
	n, err := io.CopyBuffer(io.MultiWriter(out, h, prog), body, make([]byte, copyBufferSize))
	if err != nil {
		if errors.Is(context.Cause(fctx), errStalled) {
			return "", errStalled
		}
		return "", fmt.Errorf("se cortó la descarga: %w", err)
	}
	if offset+n != f.Size {
		return "", fmt.Errorf("llegaron %s de %s", humanSize(offset+n), humanSize(f.Size))
	}
	if want := resp.Trailer.Get(protocol.HashTrailer); !strings.EqualFold(want, hex.EncodeToString(h.Sum(nil))) {
		out.Close()
		os.Remove(part)
		return "", errors.New("el archivo llegó dañado (no coincide el SHA-256); reintenta")
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	final, err := uniquePath(fileDir(t, f), safeFileName(f.Name))
	if err != nil {
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	if err := markFromNetwork(final); err != nil {
		s.log.Debug("marcando archivo descargado", "path", final, "err", err)
	}
	return final, nil
}

// discardLocal borra lo que dejó una transferencia que no terminará: los
// .part del destinatario o las copias temporales del remitente.
func (s *Service) discardLocal(t store.Transfer) {
	if t.Outgoing {
		s.cleanStaging(t)
		return
	}
	if t.Dir == "" {
		return
	}
	for _, f := range t.Files {
		if !f.Done {
			os.Remove(partPath(t, f))
		}
	}
}

// remaining estima cuántos bytes faltan por descargar.
func remaining(t store.Transfer, dir string) int64 {
	var n int64
	for _, f := range t.Files {
		if f.Done {
			continue
		}
		n += f.Size
		t.Dir = dir
		if fi, err := os.Stat(partPath(t, f)); err == nil {
			n -= min(fi.Size(), f.Size)
		}
	}
	return n
}

// ---------- Avisos al otro equipo ----------

func (s *Service) notifyState(t store.Transfer, state string) {
	body, _ := json.Marshal(map[string]string{"state": state})
	s.goSafe(func() { s.notify(t, protocol.PathTransferState(t.ID), body) })
}

// notify hace un POST corto al otro equipo; si no está, el aviso se pierde
// (lo descubrirá con un 410 o al caducar la oferta).
func (s *Service) notify(t store.Transfer, path string, body []byte) {
	p, ok := s.dir.Peer(t.PeerID)
	if !ok || !p.Online {
		return
	}
	if body == nil {
		body = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(s.ctx, notifyTimeout)
	defer cancel()
	pctx, err := peer.ContextFor(ctx, s.store, t.PeerID)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(pctx, http.MethodPost, "https://"+p.HTTPAddr().String()+path, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(protocol.TokenHeader, t.Token)
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Debug("avisando al otro equipo", "transfer", t.ID, "path", path, "err", err)
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	resp.Body.Close()
}

// ---------- Mantenimiento ----------

// maintain marca como caducadas las ofertas vencidas.
func (s *Service) maintain() {
	for _, outgoing := range []bool{true, false} {
		ts, err := s.store.TransfersInState(s.ctx, outgoing,
			store.TransferOffered, store.TransferDownloading, store.TransferFailed)
		if err != nil {
			if s.ctx.Err() == nil {
				s.log.Error("revisando caducidad", "err", err)
			}
			return
		}
		now := time.Now()
		for _, t := range ts {
			if !now.Before(t.ExpiresAt) && !s.isActive(t.ID) {
				s.expire(s.ctx, t)
			}
		}
	}
}

func (s *Service) expire(ctx context.Context, t store.Transfer) {
	s.discardLocal(t)
	s.setState(ctx, t.ID, store.TransferExpired, "")
}

// cleanOrphanStaging borra copias temporales que no pertenecen a ninguna
// oferta activa (p. ej. si LanChat se cerró mientras se copiaban).
func (s *Service) cleanOrphanStaging() {
	entries, err := os.ReadDir(s.cfg.StagingDir)
	if err != nil {
		return
	}
	ts, err := s.store.TransfersInState(s.ctx, true, store.TransferOffered, store.TransferDownloading)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, t := range ts {
		for _, f := range t.Files {
			if root := s.stagingRoot(f.Path); root != "" {
				used[root] = true
			}
		}
	}
	for _, e := range entries {
		dir := filepath.Join(s.cfg.StagingDir, e.Name())
		if e.IsDir() && !used[dir] {
			os.RemoveAll(dir)
		}
	}
}

// ---------- Estado y eventos ----------

func (s *Service) setState(ctx context.Context, id string, st store.TransferState, errMsg string) {
	if err := s.store.SetTransferState(ctx, id, st, errMsg); err != nil {
		if s.ctx.Err() == nil {
			s.log.Error("guardando estado de transferencia", "id", id, "err", err)
		}
		return
	}
	if t, ok, err := s.store.Transfer(ctx, id); err == nil && ok {
		s.emitChanged(t)
	}
}

func (s *Service) emitChanged(t store.Transfer) { s.emit(Event{Type: TransferChanged, Transfer: t}) }

func (s *Service) emit(ev Event) {
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *Service) goSafe(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f()
	}()
}

// activeOp es una descarga o un envío en curso.
type activeOp struct {
	cancel context.CancelFunc
	done   chan struct{} // se cierra al terminar
}

// startActive registra una operación; devuelve false si ya había una para id.
func (s *Service) startActive(id string, cancel context.CancelFunc) (*activeOp, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.active[id]; busy {
		return nil, false
	}
	op := &activeOp{cancel: cancel, done: make(chan struct{})}
	s.active[id] = op
	return op, true
}

func (s *Service) endActive(id string, op *activeOp) {
	s.mu.Lock()
	if s.active[id] == op {
		delete(s.active, id)
	}
	s.mu.Unlock()
	close(op.done)
}

func (s *Service) isActive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active[id]
	return ok
}

// stopActive corta la operación en curso de id y espera a que termine: en
// Windows un archivo abierto no se puede borrar.
func (s *Service) stopActive(id string) {
	s.mu.Lock()
	op := s.active[id]
	s.mu.Unlock()
	if op == nil {
		return
	}
	op.cancel()
	select {
	case <-op.done:
	case <-time.After(stopWait):
		s.log.Warn("la transferencia tardó en detenerse", "id", id)
	}
}

// ---------- Utilidades ----------

func newToken() string {
	var b [32]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func allDone(t store.Transfer) bool {
	for _, f := range t.Files {
		if !f.Done {
			return false
		}
	}
	return true
}

// doneBefore suma los archivos ya terminados antes de idx (para el progreso).
func doneBefore(t store.Transfer, idx int) int64 {
	var n int64
	for _, f := range t.Files {
		if f.Done && f.Index != idx {
			n += f.Size
		}
	}
	return n
}

func stateText(st store.TransferState) string {
	switch st {
	case store.TransferOffered:
		return "esperando respuesta"
	case store.TransferDownloading:
		return "en curso"
	case store.TransferCompleted:
		return "completada"
	case store.TransferRejected:
		return "rechazada"
	case store.TransferCanceled:
		return "cancelada"
	case store.TransferExpired:
		return "caducada"
	case store.TransferFailed:
		return "con error"
	}
	return st.String()
}

// parseRange interpreta "bytes=N-" (la única forma que usa LanChat).
func parseRange(h string, size int64) (int64, error) {
	if h == "" {
		return 0, nil
	}
	spec, ok := strings.CutPrefix(h, "bytes=")
	start, rest, _ := strings.Cut(spec, "-")
	n, err := strconv.ParseInt(start, 10, 64)
	if !ok || rest != "" || err != nil || n < 0 || n > size {
		return 0, fmt.Errorf("rango no soportado: %q", h)
	}
	return n, nil
}

func contentRangeStart(h string) (int64, bool) {
	spec, ok := strings.CutPrefix(h, "bytes ")
	start, _, found := strings.Cut(spec, "-")
	n, err := strconv.ParseInt(start, 10, 64)
	return n, ok && found && err == nil
}

func readError(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if msg := strings.TrimSpace(string(b)); msg != "" {
		return msg
	}
	return resp.Status
}

// ctxReader deja de leer cuando se cancela ctx.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// activityReader avisa cada vez que llegan datos (para detectar descargas trabadas).
type activityReader struct {
	r      io.Reader
	onRead func()
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.onRead()
	}
	return n, err
}
