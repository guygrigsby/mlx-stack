package obsstate

import (
	"context"
	"sync"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/logobs"
)

const ringSize = 10

type WorkerObs struct {
	Name         string                `json:"name"`
	LatestMem    *logobs.MemSnapshot   `json:"latest_mem,omitempty"`
	LatestTiming *logobs.Timing        `json:"latest_timing,omitempty"`
	LastTimingAt int64                 `json:"last_timing_at,omitempty"` // unix seconds
	ActiveReq    *logobs.Progress      `json:"active_req,omitempty"`
	LastWatchdog *logobs.WatchdogEvent `json:"last_watchdog,omitempty"`
	RecentTiming []logobs.Timing       `json:"recent_timing,omitempty"`
	Updated      time.Time             `json:"updated"`
}

type Store struct {
	mu      sync.RWMutex
	workers map[string]*WorkerObs
}

func New() *Store {
	return &Store{workers: map[string]*WorkerObs{}}
}

func (s *Store) Apply(ev logobs.Event) {
	if ev.Worker == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.workers[ev.Worker]
	if !ok {
		w = &WorkerObs{Name: ev.Worker}
		s.workers[ev.Worker] = w
	}
	now := time.Now()
	w.Updated = now
	switch ev.Kind {
	case logobs.KindMem:
		mem := ev.Mem
		w.LatestMem = &mem
	case logobs.KindTiming:
		t := ev.Timing
		w.LatestTiming = &t
		w.LastTimingAt = now.Unix()
		w.ActiveReq = nil // request complete
		w.RecentTiming = append(w.RecentTiming, t)
		if len(w.RecentTiming) > ringSize {
			w.RecentTiming = w.RecentTiming[len(w.RecentTiming)-ringSize:]
		}
	case logobs.KindProgress:
		p := ev.Progress
		w.ActiveReq = &p
	case logobs.KindWatchdogArmed, logobs.KindWatchdogTrigger:
		wd := ev.Watchdog
		w.LastWatchdog = &wd
	}
}

// Snapshot returns a deep copy of all worker state.
func (s *Store) Snapshot() map[string]WorkerObs {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]WorkerObs, len(s.workers))
	for k, v := range s.workers {
		copy := *v // shallow
		if v.RecentTiming != nil {
			copy.RecentTiming = append([]logobs.Timing(nil), v.RecentTiming...)
		}
		out[k] = copy
	}
	return out
}

// Run subscribes to broker and applies events until ctx is canceled.
func (s *Store) Run(ctx context.Context, broker *logobs.Broker) {
	ch := broker.Subscribe(ctx)
	for ev := range ch {
		s.Apply(ev)
	}
}
