package grpcsrv

import (
	"sync"

	"github.com/google/uuid"
)

// StreamRegistry tracks the open RunEvents streams per worker, so a
// revocation can end them (spec §4 "Revocation"). It lives in process: with
// several managers each ends only its own streams, while the interceptor
// refuses the revoked worker's next call on every manager.
type StreamRegistry struct {
	mu       sync.Mutex
	next     uint64
	byWorker map[uuid.UUID]map[uint64]chan struct{}
}

// NewStreamRegistry builds an empty registry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{byWorker: map[uuid.UUID]map[uint64]chan struct{}{}}
}

// track registers one stream of workerID; the channel closes when the
// worker is revoked, release unregisters it.
func (r *StreamRegistry) track(workerID uuid.UUID) (<-chan struct{}, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id, ch := r.next, make(chan struct{})
	if r.byWorker[workerID] == nil {
		r.byWorker[workerID] = map[uint64]chan struct{}{}
	}
	r.byWorker[workerID][id] = ch
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if streams, ok := r.byWorker[workerID]; ok {
			delete(streams, id)
			if len(streams) == 0 {
				delete(r.byWorker, workerID)
			}
		}
	}
}

// CloseWorker ends every open stream of workerID and reports how many.
func (r *StreamRegistry) CloseWorker(workerID uuid.UUID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	streams := r.byWorker[workerID]
	for _, ch := range streams {
		close(ch)
	}
	delete(r.byWorker, workerID)
	return len(streams)
}
