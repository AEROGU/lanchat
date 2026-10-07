// Package peer es el servidor HTTPS que atiende a los demás equipos LanChat
// (mensajes y archivos) y el cliente para hablarles.
//
// Toda la comunicación usa TLS 1.3 mutuo con la identidad propia de cada
// instalación (ver internal/identity). No hay autoridad certificadora: el
// servidor acepta cualquier certificado de cliente y los handlers comparan su
// huella (ClientFingerprint) con la fijada para el remitente; el cliente solo
// completa la conexión si la huella del servidor es la esperada
// (WithFingerprint).
package peer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/AEROGU/lanchat/internal/identity"
)

const (
	dialTimeout       = 3 * time.Second
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 60 * time.Second
	// requestTimeout limita las peticiones cortas (mensajes, avisos, listas).
	requestTimeout = 10 * time.Second
	// responseHeaderTimeout: espera máxima a que empiece una descarga.
	responseHeaderTimeout = 30 * time.Second
)

// ErrIdentityMismatch: el otro equipo no presentó la identidad fijada para él.
var ErrIdentityMismatch = errors.New("la identidad del equipo no coincide con la conocida")

// errNoFingerprint: se intentó conectar sin saber qué identidad esperar.
var errNoFingerprint = errors.New("no se conoce la identidad del equipo")

type Server struct {
	ln  net.Listener
	srv *http.Server
	mux *http.ServeMux
}

// Listen abre el puerto TCP en todas las interfaces (0 = uno libre, para
// pruebas) y atiende con TLS usando la identidad id.
func Listen(addr string, id *identity.Identity) (*Server, error) {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("no se pudo abrir el puerto TCP %s (¿LanChat ya está abierto?): %w", addr, err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{id.Cert},
		MinVersion:   tls.VersionTLS13,
		// Cualquier certificado: quién es se decide comparando su huella.
		ClientAuth: tls.RequireAnyClientCert,
	}
	mux := http.NewServeMux()
	return &Server{
		ln:  tls.NewListener(ln, cfg),
		mux: mux,
		srv: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
			IdleTimeout:       idleTimeout,
		},
	}, nil
}

// Port es el puerto TCP en el que escucha.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Handle registra una ruta, p. ej. "POST /v1/msg".
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// Serve atiende peticiones hasta que se llame a Shutdown.
func (s *Server) Serve() error {
	if err := s.srv.Serve(s.ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close cierra el puerto aunque Serve no se haya llamado.
func (s *Server) Close() error {
	s.ln.Close()
	return s.srv.Close()
}

// Shutdown deja de aceptar peticiones y espera a que terminen las que están en curso.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

// ClientFingerprint es la huella de la identidad que presentó quien hizo la
// petición ("" si no hay TLS, lo que no debería pasar en este servidor).
func ClientFingerprint(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return identity.Fingerprint(r.TLS.PeerCertificates[0])
}

type fingerprintKey struct{}

// WithFingerprint indica qué huella debe presentar el equipo al que se
// conectará la petición hecha con ctx. Sin ella, el cliente no se conecta.
func WithFingerprint(ctx context.Context, fp string) context.Context {
	return context.WithValue(ctx, fingerprintKey{}, fp)
}

// NewClient devuelve un cliente HTTPS con tiempos de espera pensados para una
// LAN. Su límite total no sirve para descargas: para eso, NewStreamClient.
func NewClient(id *identity.Identity) *http.Client {
	return &http.Client{Timeout: requestTimeout, Transport: transport(id)}
}

// NewStreamClient es para descargas largas: sin límite de tiempo total (cada
// descarga controla por su cuenta si se traba), pero sí para conectar y para
// recibir la respuesta.
func NewStreamClient(id *identity.Identity) *http.Client {
	t := transport(id)
	t.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{Transport: t}
}

// transport conecta con TLS y comprueba la huella esperada en cada conexión.
// Sin conexiones reutilizadas: una conexión verificada para un equipo no debe
// servir para otro que tomó su IP.
func transport(id *identity.Identity) *http.Transport {
	return &http.Transport{
		DisableKeepAlives: true,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			want, _ := ctx.Value(fingerprintKey{}).(string)
			if want == "" {
				return nil, errNoFingerprint
			}
			d := tls.Dialer{
				NetDialer: &net.Dialer{Timeout: dialTimeout},
				Config: &tls.Config{
					Certificates: []tls.Certificate{id.Cert},
					MinVersion:   tls.VersionTLS13,
					// No hay autoridad que firme: se verifica la huella abajo.
					InsecureSkipVerify: true,
					VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
						if len(raw) == 0 {
							return ErrIdentityMismatch
						}
						cert, err := x509.ParseCertificate(raw[0])
						if err != nil || identity.Fingerprint(cert) != want {
							return ErrIdentityMismatch
						}
						return nil
					},
				},
			}
			return d.DialContext(ctx, network, addr)
		},
	}
}

// Pins da la huella fijada de cada equipo; lo implementa store.Store.
type Pins interface {
	PinnedFingerprint(ctx context.Context, peerID string) (string, error)
}

// ContextFor prepara ctx para conectar con peerID: la conexión solo se
// completa si el equipo presenta la huella fijada para él.
func ContextFor(ctx context.Context, pins Pins, peerID string) (context.Context, error) {
	fp, err := pins.PinnedFingerprint(ctx, peerID)
	if err != nil {
		return nil, err
	}
	return WithFingerprint(ctx, fp), nil
}
