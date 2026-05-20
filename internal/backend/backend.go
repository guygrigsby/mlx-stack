package backend

import (
	"context"
	"sync"
)

// Backend is the unified lifecycle interface every concrete backend
// (Group / Persistent / External) implements.
type Backend interface {
	Name() string
	Group() string         // for swap: shared port group name; otherwise == Name
	Mode() string          // "swap" | "persistent" | "external"
	Engine() string        // "lm" | "vlm" | "audio" | "embed" | ""

	BaseURL() string
	UpstreamModel() string // model field value to send upstream

	Running() bool
	PID() int

	// EnsureLoaded prepares the backend to serve a request for `name`.
	// - Group: loads the named member onto the shared port if not already current.
	// - Persistent: starts the worker if not running. Errors if name != self.
	// - External: no-op.
	EnsureLoaded(ctx context.Context, name string) error

	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// State holds per-backend live state. Embedded into Group / Persistent.
type State struct {
	Mu        sync.Mutex
	Current   string
	WorkerPID int
	WorkerURL string
}
