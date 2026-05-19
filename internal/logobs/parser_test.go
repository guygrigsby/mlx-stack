package logobs

import (
	"testing"
)

func TestParse_MemSnapshot(t *testing.T) {
	ev, ok := Parse("[mlx-launch] mem: active=1234 cache=5678 peak=9999")
	if !ok || ev.Kind != KindMem {
		t.Fatalf("want mem kind, got: %+v ok=%v", ev, ok)
	}
	if ev.Mem.Active != 1234 || ev.Mem.Cache != 5678 || ev.Mem.Peak != 9999 {
		t.Errorf("mem fields wrong: %+v", ev.Mem)
	}
}

func TestParse_Timing(t *testing.T) {
	ev, ok := Parse("[mlx-launch] req=r-1 prompt=42t prefill=120.5ms@123.4tps decode=850.2ms@45.6tps")
	if !ok || ev.Kind != KindTiming {
		t.Fatalf("want timing kind, got: %+v ok=%v", ev, ok)
	}
	if ev.Timing.RequestID != "r-1" {
		t.Errorf("RequestID: %q", ev.Timing.RequestID)
	}
	if ev.Timing.PromptTokens != 42 {
		t.Errorf("PromptTokens: %d", ev.Timing.PromptTokens)
	}
	if ev.Timing.PrefillMs < 120.4 || ev.Timing.PrefillMs > 120.6 {
		t.Errorf("PrefillMs: %v", ev.Timing.PrefillMs)
	}
}

func TestParse_WatchdogArmed(t *testing.T) {
	ev, ok := Parse("[mlx-launch] WATCHDOG: armed. baseline=1000000000 trigger=9000000000")
	if !ok || ev.Kind != KindWatchdogArmed {
		t.Fatalf("want watchdog-armed, got: %+v ok=%v", ev, ok)
	}
	if ev.Watchdog.Baseline != 1_000_000_000 || ev.Watchdog.Trigger != 9_000_000_000 {
		t.Errorf("watchdog fields: %+v", ev.Watchdog)
	}
}

func TestParse_WatchdogTrigger(t *testing.T) {
	ev, ok := Parse("[mlx-launch] WATCHDOG: active=9500000000 > trigger=9000000000 — execv-restarting")
	if !ok || ev.Kind != KindWatchdogTrigger {
		t.Fatalf("want watchdog-trigger, got: %+v ok=%v", ev, ok)
	}
}

func TestParse_Starting(t *testing.T) {
	ev, ok := Parse("[mlx-launch] starting engine=lm model=/tmp/m port=1234")
	if !ok || ev.Kind != KindStarting {
		t.Fatalf("want starting, got: %+v ok=%v", ev, ok)
	}
}

func TestParse_NonMatching(t *testing.T) {
	_, ok := Parse("loading model from /tmp/m")
	if ok {
		t.Errorf("expected ok=false for non-mlx-launch line")
	}
}

func TestParse_UnknownTag(t *testing.T) {
	ev, ok := Parse("[mlx-launch] something we don't recognize yet")
	if !ok || ev.Kind != KindUnknown {
		t.Fatalf("want unknown, got: %+v ok=%v", ev, ok)
	}
}
