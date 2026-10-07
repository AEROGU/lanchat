package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/protocol"
)

// wireRead es el aviso de lectura: From leyó estos mensajes que le enviamos.
type wireRead struct {
	From string   `json:"from"`
	IDs  []string `json:"ids"`
}

// maxReadRequestBytes acota el JSON de un aviso de lectura.
const maxReadRequestBytes = protocol.MaxReceiptIDs*(protocol.MaxIDLen+8) + 256

func (r wireRead) validate() error {
	if len(r.IDs) == 0 || len(r.IDs) > protocol.MaxReceiptIDs {
		return errors.New("cantidad de mensajes inválida")
	}
	errs := []error{protocol.ValidateID(r.From)}
	for _, id := range r.IDs {
		errs = append(errs, protocol.ValidateID(id))
	}
	return errors.Join(errs...)
}

// sendReceipts avisa a p qué mensajes suyos ya leímos, en tandas. Si p tiene
// una versión que no conoce los avisos (404), se descartan.
func (s *Service) sendReceipts(p discovery.Peer) {
	for {
		ids, err := s.store.PendingReceipts(s.ctx, p.ID, protocol.MaxReceiptIDs)
		if err != nil || len(ids) == 0 {
			return
		}
		status, err := s.post(p, protocol.RouteRead, wireRead{From: s.self.ID, IDs: ids})
		if err != nil || (status != http.StatusNoContent && status != http.StatusNotFound) {
			s.log.Debug("aviso de lectura fallido", "peer", p.ID, "status", status, "err", err)
			return
		}
		if err := s.store.ClearReceipts(s.ctx, ids); err != nil {
			s.log.Error("limpiando avisos de lectura", "err", err)
			return
		}
	}
}

func (s *Service) handleRead(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReadRequestBytes)
	var req wireRead
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}
	if err := req.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := s.checkSender(w, r, req.From); !ok {
		return
	}
	msgs, err := s.store.MarkReadByPeer(r.Context(), req.From, req.IDs, time.Now())
	if err != nil {
		s.log.Error("guardando lectura", "err", err)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	for _, m := range msgs {
		s.emit(Event{MessageRead, m})
	}
	w.WriteHeader(http.StatusNoContent)
}
