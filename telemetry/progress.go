package telemetry

import (
	"context"
	"sync/atomic"
	"time"
)

type Progress struct {
	Phase          Phase   `json:"phase"`
	Status         string  `json:"status"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
}

// ProgressQueue decouples consumers from execution. A blocked exporter never blocks an agent.
type ProgressQueue struct {
	C       chan Progress
	dropped atomic.Uint64
}

func NewProgressQueue() *ProgressQueue { return &ProgressQueue{C: make(chan Progress, 32)} }
func (q *ProgressQueue) Send(p Progress) {
	select {
	case q.C <- p:
	default:
		q.dropped.Add(1)
	}
}
func (q *ProgressQueue) Dropped() uint64 { return q.dropped.Load() }
func (q *ProgressQueue) Drain(ctx context.Context, send func(Progress)) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	var pending *Progress
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-q.C:
			pending = &p
		case <-tick.C:
			if pending != nil {
				send(*pending)
				pending = nil
			}
		}
	}
}
