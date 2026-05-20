package logobs

import (
	"context"
	"testing"
	"time"
)

func TestBroker_PublishesToSubscribers(t *testing.T) {
	b := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub1 := b.Subscribe(ctx)
	sub2 := b.Subscribe(ctx)

	ev := Event{Raw: "test", Kind: KindMem}
	b.Publish(ev)

	for _, c := range []<-chan Event{sub1, sub2} {
		select {
		case got := <-c:
			if got.Raw != "test" {
				t.Errorf("got: %+v", got)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("timeout receiving")
		}
	}
}

func TestBroker_ContextCancelUnsubscribes(t *testing.T) {
	b := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	sub := b.Subscribe(ctx)
	cancel()

	// channel should close shortly
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		_, ok := <-sub
		if !ok {
			return // closed; good
		}
	}
	t.Fatal("channel not closed after context cancel")
}

func TestBroker_DropsOnSlowConsumer(t *testing.T) {
	b := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = b.Subscribe(ctx) // never receive

	// Publish many events; should not block.
	for i := 0; i < 1000; i++ {
		b.Publish(Event{Raw: "x"})
	}
	// If we got here without hanging, the drop-on-full worked.
}
