// Package peer es el servidor HTTP que atiende a los demás equipos LanChat
// (mensajes y, más adelante, archivos) y el cliente para hablarles.
package peer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

type Server struct {
	ln  net.Listener
	srv *http.Server
	mux *http.ServeMux
}

// Listen abre el puerto TCP en todas las interfaces (0 = uno libre, para pruebas).
func Listen(addr string) (*Server, error) {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("no se pudo abrir el puerto TCP %s (¿LanChat ya está abierto?): %w", addr, err)
	}
	mux := http.NewServeMux()
	return &Server{
		ln:  ln,
		mux: mux,
		srv: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
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

// NewClient devuelve un cliente HTTP con tiempos de espera pensados para una LAN.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     60 * time.Second,
		},
	}
}
