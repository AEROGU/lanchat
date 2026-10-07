package transfer

import (
	"sync"
	"time"

	"github.com/AEROGU/lanchat/internal/store"
)

// progress cuenta bytes (como io.Writer) y emite eventos TransferProgress a
// lo sumo cada progressInterval.
type progress struct {
	s     *Service
	id    string
	total int64

	mu       sync.Mutex
	done     int64
	lastEmit time.Time
	lastDone int64
}

func (s *Service) newProgress(t store.Transfer, done int64) *progress {
	return &progress{s: s, id: t.ID, total: t.TotalSize(), done: done, lastDone: done, lastEmit: time.Now()}
}

func (p *progress) Write(b []byte) (int, error) {
	p.add(int64(len(b)))
	return len(b), nil
}

func (p *progress) add(n int64) {
	p.mu.Lock()
	p.done += n
	now := time.Now()
	if now.Sub(p.lastEmit) < progressInterval {
		p.mu.Unlock()
		return
	}
	ev := p.snapshot(now)
	p.mu.Unlock()
	p.s.emit(ev)
}

// flush emite el último valor aunque no haya pasado progressInterval.
func (p *progress) flush() {
	p.mu.Lock()
	ev := p.snapshot(time.Now())
	p.mu.Unlock()
	p.s.emit(ev)
}

func (p *progress) snapshot(now time.Time) Event {
	var rate float64
	if dt := now.Sub(p.lastEmit).Seconds(); dt > 0 {
		rate = float64(p.done-p.lastDone) / dt
	}
	p.lastEmit, p.lastDone = now, p.done
	return Event{Type: TransferProgress, Progress: Progress{ID: p.id, Done: p.done, Total: p.total, Rate: rate}}
}
