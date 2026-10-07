// Package ui es la interfaz gráfica: un servidor web local (solo 127.0.0.1)
// que muestra la página embebida en web/, más la ventana, la bandeja del
// sistema y las notificaciones (ver gui.go).
//
// Seguridad: cualquier proceso o página web del equipo puede intentar hablar
// con 127.0.0.1, así que toda petición necesita el token aleatorio de esta
// sesión (cookie o encabezado Authorization), el encabezado Host debe ser
// exactamente 127.0.0.1:puerto (contra DNS rebinding) y los POST deben ser
// JSON del mismo origen (contra CSRF).
package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/icon"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/transfer"
)

const (
	// cookiePrefix + puerto: las cookies no distinguen puertos, y dos instancias
	// en 127.0.0.1 (otro usuario de Windows, pruebas) se pisarían la sesión.
	cookiePrefix = "lanchat_session_"
	// maxRequestBytes acota el JSON de la página: el mensaje más los demás campos.
	maxRequestBytes = 2 * protocol.MaxMessageBytes
	// historyPage es cuántos mensajes se cargan cada vez en una conversación.
	historyPage    = 50
	maxHistoryPage = 200
	// sseKeepAlive evita que algo intermedio cierre la conexión de eventos por inactividad.
	sseKeepAlive = 25 * time.Second
	// requestTimeout limita lo que tarda la respuesta a una acción de la página.
	requestTimeout = 10 * time.Second
	// shutdownGrace es lo que se espera a las peticiones en curso al salir.
	shutdownGrace = time.Second
)

//go:embed web
var webFiles embed.FS

// Backend es lo que la interfaz necesita de la aplicación; lo implementa *app.App.
type Backend interface {
	Self() app.Self
	Contacts(ctx context.Context) ([]app.Contact, error)
	Contact(ctx context.Context, id string) (app.Contact, bool, error)
	TotalUnread(ctx context.Context) (int, error)
	MarkRead(ctx context.Context, peerID string) (bool, error)
	Send(ctx context.Context, peerID, body string) (store.Message, error)
	SendMany(ctx context.Context, peerIDs []string, body string) ([]store.Message, error)
	History(ctx context.Context, peerID, beforeID string, limit int) ([]store.Message, error)
	SetName(name string) error
	SetStatus(status, text string) error
	SetAutoAway(enabled bool) error
	SetReadReceipts(enabled bool) error
	SetAlias(ctx context.Context, peerID, alias string) error
	ManualPeers() []string
	SetManualPeers(peers []string) error

	OfferFiles(ctx context.Context, peerID string, paths []string) (store.Message, error)
	Upload(ctx context.Context, peerID string, next func() (string, io.Reader, error)) (store.Message, error)
	AcceptTransfer(ctx context.Context, id string) error
	RejectTransfer(ctx context.Context, id string) error
	CancelTransfer(ctx context.Context, id string) error
	Transfer(ctx context.Context, id string) (store.Transfer, bool, error)
	TransfersByID(ctx context.Context, ids []string) (map[string]store.Transfer, error)
	DownloadDir() string
	SetDownloadDir(dir string) error
}

type Server struct {
	b     Backend
	log   *slog.Logger
	ln    net.Listener
	srv   *http.Server
	token string
	host  string // "127.0.0.1:puerto"
	hub   *hub

	// OnOpen se llama cuando otra instancia pide mostrar la ventana.
	OnOpen func()
	// OnUnreadChanged recibe el total de mensajes sin leer cada vez que cambia.
	OnUnreadChanged func(total int)
	// AutostartArgs se agregan al inicio con Windows (p. ej. -dir).
	AutostartArgs []string

	mu sync.Mutex
	// focused y viewing los informa la página: si la ventana tiene el foco y
	// qué conversación está abierta. Deciden si hace falta notificar.
	focused bool
	viewing string
}

// Listen abre el servidor en un puerto libre de 127.0.0.1 con un token nuevo.
// OnOpen y OnUnreadChanged deben asignarse antes de llamar a Serve.
func Listen(b Backend, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var t [32]byte
	rand.Read(t[:])
	s := &Server{
		b:     b,
		log:   log,
		ln:    ln,
		token: hex.EncodeToString(t[:]),
		host:  ln.Addr().String(),
		hub:   newHub(),
	}
	s.srv = &http.Server{Handler: s.routes(), ReadHeaderTimeout: requestTimeout}
	return s, nil
}

// Port es el puerto local de la interfaz.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Token es el secreto de esta sesión.
func (s *Server) Token() string { return s.token }

// LaunchURL es la dirección con la que se abre la ventana; el token de la URL
// se cambia por una cookie en la primera visita.
func (s *Server) LaunchURL() string { return "http://" + s.host + "/?t=" + s.token }

func (s *Server) Serve() error {
	if err := s.srv.Serve(s.ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown cierra las conexiones de eventos (nunca quedan inactivas), espera
// hasta shutdownGrace a las demás peticiones y luego corta lo que quede.
// El corte hace falta porque Edge abre conexiones por adelantado que nunca
// reciben una petición, y net/http esperaría 5 s por ellas.
func (s *Server) Shutdown(ctx context.Context) error {
	s.hub.closeAll()
	ctx, cancel := context.WithTimeout(ctx, shutdownGrace)
	defer cancel()
	err := s.srv.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return s.srv.Close()
	}
	return err
}

// HasWindow indica si hay alguna ventana conectada.
func (s *Server) HasWindow() bool { return s.hub.count() > 0 }

// Focus pide a las ventanas abiertas que pasen al frente.
func (s *Server) Focus() { s.hub.broadcast("focus", struct{}{}) }

// ShouldNotify indica si un mensaje de peerID merece notificación: no hace
// falta si el usuario está viendo esa conversación con la ventana enfocada.
func (s *Server) ShouldNotify(peerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.focused || s.viewing != peerID || s.hub.count() == 0
}

// Publish reenvía a las ventanas un evento de app.Events.
func (s *Server) Publish(ctx context.Context, ev any) {
	switch e := ev.(type) {
	case discovery.Event:
		s.publishContact(ctx, e.Peer.ID)
	case chat.Event:
		s.hub.broadcast("message", s.messageJSON(ctx, e.Message))
		if e.Type == chat.MessageReceived {
			s.publishContact(ctx, e.Message.PeerID)
			s.unreadChanged(ctx)
		}
	case app.SelfChanged:
		s.hub.broadcast("self", toSelfJSON(s.b.Self()))
	case transfer.Event:
		if e.Type == transfer.TransferProgress {
			s.hub.broadcast("progress", toProgressJSON(e.Progress))
		} else {
			s.hub.broadcast("transfer", toTransferJSON(e.Transfer))
		}
	}
}

func (s *Server) publishContact(ctx context.Context, id string) {
	c, ok, err := s.b.Contact(ctx, id)
	if err != nil || !ok {
		s.log.Debug("contacto para la interfaz", "id", id, "ok", ok, "err", err)
		return
	}
	s.hub.broadcast("contact", toContactJSON(c))
}

func (s *Server) unreadChanged(ctx context.Context) {
	total, err := s.b.TotalUnread(ctx)
	if err != nil {
		if ctx.Err() == nil { // al cerrar es normal que falle
			s.log.Error("contando no leídos", "err", err)
		}
		return
	}
	if s.OnUnreadChanged != nil {
		s.OnUnreadChanged(total)
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFiles, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(icon.PNG(faviconSize, false))
	})
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/messages", s.handleHistory)
	mux.HandleFunc("POST /api/messages", s.handleSend)
	mux.HandleFunc("POST /api/messages/many", s.handleSendMany)
	mux.HandleFunc("POST /api/read", s.handleRead)
	mux.HandleFunc("POST /api/name", s.handleName)
	mux.HandleFunc("POST /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/read-receipts", s.handleReadReceipts)
	mux.HandleFunc("POST /api/alias", s.handleAlias)
	mux.HandleFunc("POST /api/manual-peers", s.handleManualPeers)
	mux.HandleFunc("POST /api/presence", s.handlePresence)
	mux.HandleFunc("POST /api/open", s.handleOpen)
	mux.HandleFunc("POST /api/files/pick", s.handlePickFiles)
	mux.HandleFunc("POST "+uploadPath, s.handleUpload)
	mux.HandleFunc("POST /api/transfers/{action}", s.handleTransferAction)
	mux.HandleFunc("POST /api/files/open", s.handleOpenFile)
	mux.HandleFunc("POST /api/download-dir", s.handleDownloadDir)
	mux.HandleFunc("POST /api/download-dir/open", s.handleOpenDownloadDir)
	mux.HandleFunc("GET /api/system", s.handleSystem)
	mux.HandleFunc("POST /api/system/autostart", s.handleAutostart)
	mux.HandleFunc("POST /api/system/firewall", s.handleFirewall)
	return s.guard(mux)
}

// guard aplica las reglas de seguridad descritas en el comentario del paquete.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.host {
			http.Error(w, "host no permitido", http.StatusMisdirectedRequest)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")

		// Primera visita: el token llega en la URL y se cambia por una cookie.
		if r.Method == http.MethodGet && r.URL.Path == "/" && r.URL.Query().Has("t") {
			if !s.validToken(r.URL.Query().Get("t")) {
				http.Error(w, "enlace caducado: abre LanChat desde el icono de la bandeja", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: s.cookieName(), Value: s.token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if !s.authorized(r) {
			http.Error(w, "no autorizado: abre LanChat desde el icono de la bandeja", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+s.host {
				http.Error(w, "origen no permitido", http.StatusForbidden)
				return
			}
			// Solo la subida de archivos arrastrados acepta multipart (y sin límite de tamaño).
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			switch {
			case r.URL.Path == uploadPath && mt == "multipart/form-data":
			case mt == "application/json":
				r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
			default:
				http.Error(w, "se esperaba JSON", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validToken(t string) bool {
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

func (s *Server) authorized(r *http.Request) bool {
	if c, err := r.Cookie(s.cookieName()); err == nil && s.validToken(c.Value) {
		return true
	}
	t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && s.validToken(t)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	contacts, err := s.b.Contacts(r.Context())
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	out := stateJSON{
		Self:        toSelfJSON(s.b.Self()),
		Contacts:    make([]contactJSON, len(contacts)),
		ManualPeers: s.b.ManualPeers(),
		DownloadDir: s.b.DownloadDir(),
		Limits: limitsJSON{MaxName: protocol.MaxNameLen, MaxMessageBytes: protocol.MaxMessageBytes,
			MaxStatusText: protocol.MaxStatusTextLen},
	}
	for i, c := range contacts {
		out.Contacts[i] = toContactJSON(c)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleEvents mantiene abierto un flujo Server-Sent Events hacia la página.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming no soportado", http.StatusInternalServerError)
		return
	}
	ch, ok := s.hub.subscribe()
	if !ok {
		http.Error(w, "cerrando", http.StatusServiceUnavailable)
		return
	}
	defer func() {
		if s.hub.unsubscribe(ch) == 0 {
			s.setPresence(false, "")
		}
	}()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	ping := time.NewTicker(sseKeepAlive)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := historyPage
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 {
		limit = min(l, maxHistoryPage)
	}
	msgs, err := s.b.History(r.Context(), q.Get("peer"), q.Get("before"), limit)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.messagesJSON(r.Context(), msgs)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer string `json:"peer"`
		Body string `json:"body"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	m, err := s.b.Send(r.Context(), req.Peer, req.Body)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.messageJSON(r.Context(), m))
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer string `json:"peer"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	changed, err := s.b.MarkRead(r.Context(), req.Peer)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if changed {
		s.publishContact(r.Context(), req.Peer)
		s.unreadChanged(r.Context())
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.b.SetName(req.Name); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, toSelfJSON(s.b.Self()))
}

func (s *Server) handleAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer  string `json:"peer"`
		Alias string `json:"alias"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.b.SetAlias(r.Context(), req.Peer, req.Alias); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	s.publishContact(r.Context(), req.Peer)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleManualPeers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peers []string `json:"peers"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.b.SetManualPeers(req.Peers); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"manualPeers": s.b.ManualPeers()})
}

func (s *Server) handlePresence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Focused bool   `json:"focused"`
		Viewing string `json:"viewing"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	s.setPresence(req.Focused, req.Viewing)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setPresence(focused bool, viewing string) {
	s.mu.Lock()
	s.focused, s.viewing = focused, viewing
	s.mu.Unlock()
}

// handleOpen lo usa una segunda instancia para pedir que se muestre la ventana.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if s.OnOpen != nil {
		go s.OnOpen()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("petición inválida: %w", err))
		return false
	}
	return true
}

func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	if status >= 500 {
		s.log.Error("interfaz", "err", err)
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) cookieName() string { return cookiePrefix + strconv.Itoa(s.Port()) }

// handleStatus cambia el estado, su texto y el ausente automático.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status   string `json:"status"`
		Text     string `json:"text"`
		AutoAway bool   `json:"autoAway"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	err := s.b.SetStatus(req.Status, req.Text)
	if err == nil {
		err = s.b.SetAutoAway(req.AutoAway)
	}
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, toSelfJSON(s.b.Self()))
}

// handleSendMany envía un "Mensaje a varios". Si falla con algunos contactos,
// responde igual los que sí se enviaron junto con el error.
func (s *Server) handleSendMany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peers []string `json:"peers"`
		Body  string   `json:"body"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	msgs, err := s.b.SendMany(r.Context(), req.Peers, req.Body)
	if len(msgs) == 0 && err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	out := struct {
		Messages []messageJSON `json:"messages"`
		Error    string        `json:"error,omitempty"`
	}{Messages: make([]messageJSON, len(msgs))}
	for i, m := range msgs {
		out.Messages[i] = toMessageJSON(m)
	}
	if err != nil {
		out.Error = err.Error()
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleReadReceipts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.b.SetReadReceipts(req.Enabled); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, toSelfJSON(s.b.Self()))
}
