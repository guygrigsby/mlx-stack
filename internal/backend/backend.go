package backend

import (
	"context"
	"sync"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

// Backend is what the router talks to. Implementations either own a managed
// process or just hold an external URL.
type Backend interface {
	Alias() string
	URL() string
	UpstreamModel(ctx context.Context) (string, error)
	Health(ctx context.Context) error
}

// External: mlxd doesn't own the process. URL is fixed; UpstreamModel is fixed.
type External struct {
	AliasName    string
	BaseURL      string
	UpstreamName string
}

func (e *External) Alias() string { return e.AliasName }
func (e *External) URL() string   { return e.BaseURL }
func (e *External) UpstreamModel(_ context.Context) (string, error) {
	return e.UpstreamName, nil
}
func (e *External) Health(ctx context.Context) error { return nil }

// ChatState is the live state of a swap-on-demand chat backend.
// Mutable; held inside ChatSwap.
type ChatState struct {
	Mu             sync.Mutex
	CurrentProfile string
	WorkerPID      int
	WorkerURL      string
}

// ChatSwap is the backend used by router when the request looks like a chat
// completion. It does not implement Backend directly because the router has
// to call EnsureProfile first; the supervisor will adapt.
type ChatSwap struct {
	Host           string
	Port           int
	Profiles       map[string]config.Profile
	DefaultProfile string
	SwapTimeoutSec int
	State          *ChatState
}

func (c *ChatSwap) URL() string { return ChatURL(c.Host, c.Port) }

// ChatURL is the canonical URL for a chat-swap backend.
func ChatURL(host string, port int) string {
	return "http://" + host + ":" + itoa(port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
