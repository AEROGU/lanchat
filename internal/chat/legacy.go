package chat

import (
	"cmp"
	"fmt"
	"net/http"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/ids"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/version"
)

// Aviso a los equipos con el protocolo anterior: ya no se comunican con este,
// así que se les manda una vez, en su protocolo, un mensaje normal que les
// pide actualizar. Es lo único que se les envía.

// noticeRetry: tiempo mínimo entre intentos de avisar al mismo equipo (sus
// anuncios llegan cada pocos segundos).
const noticeRetry = 5 * time.Minute

// legacyMessage es un mensaje de texto tal como lo entiende el protocolo
// protocol.LegacyVersion. Va aparte de wireMessage para que los cambios del
// protocolo actual no lo alteren.
type legacyMessage struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	FromName string `json:"from_name,omitempty"`
	FromHost string `json:"from_host,omitempty"`
	Body     string `json:"body"`
	SentAt   int64  `json:"sent_at"` // Unix en milisegundos
}

// NotifyIncompatible pide una sola vez a un equipo que usa
// protocol.LegacyVersion que actualice LanChat. Se intenta con sus anuncios
// (como mucho cada noticeRetry) hasta que responda; a uno con otra versión,
// más vieja o más nueva, no se le sabe escribir.
func (s *Service) NotifyIncompatible(p discovery.Peer) {
	if p.Protocol != protocol.LegacyVersion || !p.Online {
		return
	}
	s.mu.Lock()
	if last, ok := s.noticeTried[p.ID]; ok && time.Since(last) < noticeRetry {
		s.mu.Unlock()
		return
	}
	s.noticeTried[p.ID] = time.Now()
	s.mu.Unlock()
	s.goSafe(func() { s.sendNotice(p) })
}

func (s *Service) sendNotice(p discovery.Peer) {
	notified, err := s.store.NotifiedProtocol(s.ctx, p.ID)
	if err != nil || notified >= protocol.Version {
		return
	}
	m := legacyMessage{
		ID:       ids.New(),
		From:     s.self.ID,
		FromName: s.self.Name(),
		FromHost: s.self.Hostname,
		Body:     noticeText(cmp.Or(s.self.Name(), s.self.Hostname), p.AppVersion),
		SentAt:   time.Now().UnixMilli(),
	}
	status, err := s.post(p, protocol.LegacyRouteMessage, m)
	if err != nil || status >= http.StatusInternalServerError {
		// Se reintenta con un anuncio posterior.
		s.log.Debug("aviso de actualizar no entregado", "peer", p.ID, "status", status, "err", err)
		return
	}
	// Cualquier otra respuesta es definitiva: reintentar no serviría.
	if status != http.StatusNoContent {
		s.log.Warn("el equipo rechazó el aviso de actualizar LanChat", "peer", p.ID, "status", status)
	} else {
		s.log.Info("se pidió actualizar LanChat a un equipo con una versión anterior",
			"peer", p.ID, "app", p.AppVersion)
	}
	if err := s.store.SetNotifiedProtocol(s.ctx, p.ID, protocol.Version); err != nil {
		s.log.Error("guardando el aviso de actualizar", "peer", p.ID, "err", err)
	}
}

// noticeText es el mensaje que recibe un equipo con una versión anterior.
func noticeText(who, theirs string) string {
	if theirs != "" {
		theirs = " (" + theirs + ")"
	}
	return fmt.Sprintf("⚠️ Aviso automático de LanChat: %s actualizó a la versión %s, que ya no puede "+
		"comunicarse con la tuya%s. Para seguir escribiéndose, instala la última versión desde %s",
		who, version.App, theirs, version.Releases)
}
