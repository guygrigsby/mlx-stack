package logobs

import (
	"context"
	"sync"
)

// Broker fans out events from many publishers to many subscribers. Drop-on-
// slow-consumer (best-effort).
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBroker() *Broker {
	return &Broker{subs: map[chan Event]struct{}{}}
}

func (b *Broker) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.subs {
		select {
		case c <- ev:
		default:
		}
	}
}

// Subscribe returns a channel of events. Closed when ctx is canceled.
func (b *Broker) Subscribe(ctx context.Context) <-chan Event {
	c := make(chan Event, 256)
	b.mu.Lock()
	b.subs[c] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, c)
		close(c)
		b.mu.Unlock()
	}()
	return c
}
