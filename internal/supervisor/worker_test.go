package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A worker that emits parseable stderr right up to the moment it exits must
// not panic with "send on closed channel": consumeStderr must finish before
// wait() closes the events channel. Run under -race to surface the data race.
func TestWorker_NoSendOnClosedEventsRace(t *testing.T) {
	for i := 0; i < 40; i++ {
		w := New(WorkerSpec{
			Name:    "race",
			Command: "/bin/sh",
			Args:    []string{"-c", `for n in $(seq 1 60); do echo "[mlx-launch] mem: active=$n cache=0 peak=0" 1>&2; done; exit 0`},
		})
		if err := w.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		<-w.Done()
	}
}

func TestWorker_StartAndExitNaturally(t *testing.T) {
	w := New(WorkerSpec{
		Name:    "test-1",
		Command: "/bin/sh",
		Args:    []string{"-c", "echo '[mlx-launch] starting engine=lm model=/x port=1' 1>&2; exit 0"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case res := <-w.Done():
		if res.ExitCode != 0 {
			t.Errorf("exit code: want 0 got %d", res.ExitCode)
		}
	case <-ctx.Done():
		t.Fatal("worker didn't exit in time")
	}

	lines := w.StderrLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "starting engine=lm") {
		t.Errorf("expected starting line in stderr, got: %q", joined)
	}
}

func TestWorker_StreamingStderr(t *testing.T) {
	w := New(WorkerSpec{
		Name:    "test-2",
		Command: "/bin/sh",
		Args:    []string{"-c", "for i in 1 2 3 4 5; do echo \"[mlx-launch] mem: active=$i cache=0 peak=0\" 1>&2; done; exit 0"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Done is delivered after wait() closes the events channel, so by here the
	// buffered events are complete and can be drained without a concurrent
	// reader (which would race the len check below).
	<-w.Done()
	var events []string
	for ev := range w.Events() {
		events = append(events, ev.Raw)
	}
	if len(events) < 5 {
		t.Errorf("want >=5 events, got %d: %v", len(events), events)
	}
}

func TestWorker_Signal(t *testing.T) {
	w := New(WorkerSpec{
		Name:    "test-3",
		Command: "/bin/sh",
		Args:    []string{"-c", "trap 'exit 0' TERM; while true; do sleep 0.1; done"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if err := w.Signal("TERM"); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	select {
	case res := <-w.Done():
		if res.ExitCode != 0 {
			t.Errorf("exit code after TERM: %d", res.ExitCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker didn't exit after TERM")
	}
}

func TestWorker_PIDExposed(t *testing.T) {
	w := New(WorkerSpec{
		Name:    "test-4",
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 1"},
	})
	ctx := context.Background()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if w.PID() <= 0 {
		t.Errorf("expected PID > 0, got %d", w.PID())
	}
	w.Signal("KILL")
	<-w.Done()
}
