package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"

	"github.com/guygrigsby/mlx-stack/internal/logobs"
)

type WorkerSpec struct {
	Name    string
	Command string
	Args    []string
	Env     []string
	Logger  *slog.Logger
	Broker  *logobs.Broker
}

type WorkerResult struct {
	ExitCode int
	Err      error
}

type Worker struct {
	spec WorkerSpec
	cmd  *exec.Cmd
	pid  int

	stderrMu    sync.Mutex
	stderrLines []string

	events     chan logobs.Event
	done       chan WorkerResult
	stderrDone chan struct{} // closed when consumeStderr returns

	startOnce sync.Once
}

func New(spec WorkerSpec) *Worker {
	if spec.Logger == nil {
		spec.Logger = slog.Default()
	}
	return &Worker{
		spec:       spec,
		events:     make(chan logobs.Event, 256),
		done:       make(chan WorkerResult, 1),
		stderrDone: make(chan struct{}),
	}
}

func (w *Worker) Start(ctx context.Context) error {
	var startErr error
	w.startOnce.Do(func() {
		// Intentionally NOT exec.CommandContext(ctx, ...). ctx here is the
		// caller's (often an HTTP request) context — binding the worker's
		// lifetime to it would SIGKILL the model server the moment the
		// request that started it completes. Worker lifetime is managed
		// explicitly via Signal / Stop / the group reaper.
		_ = ctx
		cmd := exec.Command(w.spec.Command, w.spec.Args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = append(cmd.Environ(), w.spec.Env...)

		stderr, err := cmd.StderrPipe()
		if err != nil {
			startErr = fmt.Errorf("stderr pipe: %w", err)
			return
		}

		if err := cmd.Start(); err != nil {
			startErr = fmt.Errorf("start: %w", err)
			return
		}
		w.cmd = cmd
		w.pid = cmd.Process.Pid

		go w.consumeStderr(stderr)
		go w.wait()
	})
	return startErr
}

func (w *Worker) consumeStderr(r io.Reader) {
	// Signal wait() that no more sends to w.events will happen, so it can
	// safely close the channel without racing a send here.
	defer close(w.stderrDone)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		w.stderrMu.Lock()
		w.stderrLines = append(w.stderrLines, line)
		if len(w.stderrLines) > 500 {
			w.stderrLines = w.stderrLines[len(w.stderrLines)-500:]
		}
		w.stderrMu.Unlock()

		w.spec.Logger.Info("worker.stderr", "name", w.spec.Name, "pid", w.pid, "line", line)

		if ev, ok := logobs.Parse(line); ok {
			ev.Worker = w.spec.Name
			select {
			case w.events <- ev:
			default:
				select {
				case <-w.events:
				default:
				}
				select {
				case w.events <- ev:
				default:
				}
			}
			if w.spec.Broker != nil {
				w.spec.Broker.Publish(ev)
			}
		}
	}
}

func (w *Worker) wait() {
	err := w.cmd.Wait()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	// cmd.Wait closed the stderr pipe; wait for consumeStderr to drain it and
	// stop sending before closing events (else: send on closed channel).
	<-w.stderrDone
	close(w.events)
	w.done <- WorkerResult{ExitCode: code, Err: err}
	close(w.done)
}

func (w *Worker) Done() <-chan WorkerResult   { return w.done }
func (w *Worker) Events() <-chan logobs.Event { return w.events }
func (w *Worker) PID() int                    { return w.pid }

func (w *Worker) StderrLines() []string {
	w.stderrMu.Lock()
	defer w.stderrMu.Unlock()
	out := make([]string, len(w.stderrLines))
	copy(out, w.stderrLines)
	return out
}

func (w *Worker) Signal(name string) error {
	if w.cmd == nil || w.cmd.Process == nil {
		return fmt.Errorf("worker not started")
	}
	sig, err := signalFor(name)
	if err != nil {
		return err
	}
	return syscall.Kill(-w.cmd.Process.Pid, sig)
}

func signalFor(name string) (syscall.Signal, error) {
	switch name {
	case "TERM":
		return syscall.SIGTERM, nil
	case "KILL":
		return syscall.SIGKILL, nil
	case "INT":
		return syscall.SIGINT, nil
	case "HUP":
		return syscall.SIGHUP, nil
	default:
		return 0, fmt.Errorf("unknown signal %q", name)
	}
}
