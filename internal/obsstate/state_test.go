package obsstate

import (
	"context"
	"testing"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/logobs"
)

func TestStore_ApplyMem(t *testing.T) {
	s := New()
	s.Apply(logobs.Event{Worker: "chat", Kind: logobs.KindMem, Mem: logobs.MemSnapshot{Active: 100, Cache: 50, Peak: 200}})
	snap := s.Snapshot()
	if w, ok := snap["chat"]; !ok || w.LatestMem == nil {
		t.Fatalf("expected chat mem snapshot, got %+v", snap)
	} else if w.LatestMem.Active != 100 {
		t.Errorf("active: %d", w.LatestMem.Active)
	}
}

func TestStore_ApplyTimingRingBuffer(t *testing.T) {
	s := New()
	for i := 0; i < 15; i++ {
		s.Apply(logobs.Event{Worker: "chat", Kind: logobs.KindTiming, Timing: logobs.Timing{RequestID: "r"}})
	}
	snap := s.Snapshot()
	w := snap["chat"]
	if len(w.RecentTiming) != ringSize {
		t.Errorf("want %d entries, got %d", ringSize, len(w.RecentTiming))
	}
}

func TestStore_IgnoresNoWorker(t *testing.T) {
	s := New()
	s.Apply(logobs.Event{Worker: "", Kind: logobs.KindMem})
	if len(s.Snapshot()) != 0 {
		t.Errorf("expected empty snapshot")
	}
}

func TestStore_WatchdogLatched(t *testing.T) {
	s := New()
	s.Apply(logobs.Event{Worker: "chat", Kind: logobs.KindWatchdogArmed, Watchdog: logobs.WatchdogEvent{Baseline: 1e9, Trigger: 9e9}})
	snap := s.Snapshot()
	w := snap["chat"]
	if w.LastWatchdog == nil || w.LastWatchdog.Baseline != 1e9 {
		t.Errorf("wd: %+v", w.LastWatchdog)
	}
}

func TestStore_RunConsumesBroker(t *testing.T) {
	s := New()
	broker := logobs.NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx, broker)
	time.Sleep(10 * time.Millisecond) // allow goroutine to call Subscribe before Publish

	broker.Publish(logobs.Event{Worker: "chat", Kind: logobs.KindMem, Mem: logobs.MemSnapshot{Active: 42}})

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s.Snapshot()["chat"].LatestMem != nil && s.Snapshot()["chat"].LatestMem.Active == 42 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("event not applied: %+v", s.Snapshot())
}
