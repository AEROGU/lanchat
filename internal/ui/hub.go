package ui

import (
	"encoding/json"
	"sync"
)

// clientBuffer: eventos que puede acumular una ventana lenta antes de que se
// la desconecte (EventSource se reconecta sola y recarga el estado).
const clientBuffer = 64

type sseEvent struct {
	name string
	data []byte
}

// hub reparte eventos a todas las ventanas conectadas por /api/events.
type hub struct {
	mu      sync.Mutex
	clients map[chan sseEvent]struct{}
	closed  bool
}

func newHub() *hub {
	return &hub{clients: map[chan sseEvent]struct{}{}}
}

func (h *hub) subscribe() (chan sseEvent, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	ch := make(chan sseEvent, clientBuffer)
	h.clients[ch] = struct{}{}
	return ch, true
}

// unsubscribe quita al cliente y devuelve cuántos quedan.
func (h *hub) unsubscribe(ch chan sseEvent) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	return len(h.clients)
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *hub) broadcast(name string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- sseEvent{name, data}:
		default:
			// Ventana demasiado lenta: se la desconecta para que recargue.
			delete(h.clients, ch)
			close(ch)
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.clients {
		delete(h.clients, ch)
		close(ch)
	}
}
