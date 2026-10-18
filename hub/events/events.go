// Package events is a small in-process pub/sub used to push live updates to
// browser WebSocket connections.
package events

import "sync"

type Event struct {
	Type   string `json:"type"`
	HostID string `json:"host_id,omitempty"`
	Data   any    `json:"data,omitempty"`
}

type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

// Subscribe returns a buffered channel of events and a cancel function.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish delivers e to all subscribers, dropping it for slow ones so a
// stuck browser never blocks agent processing.
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
