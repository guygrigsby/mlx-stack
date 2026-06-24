package config

import (
	"fmt"
)

type Config struct {
	LogDir     string        `toml:"log_dir"`
	ModelsRoot string        `toml:"models_root"`
	PythonBin  string        `toml:"python_bin"`
	Router     Router        `toml:"router"`
	Defaults   Defaults      `toml:"defaults"`
	Offload    *Offload      `toml:"offload"`
	Backends   []BackendSpec `toml:"backend"`
}

// Offload configures two-tier model storage. When nil, models are single-tier
// (today's behavior). ExternalRoot is the durable library; ModelsRoot is the
// budgeted cache.
type Offload struct {
	ExternalRoot     string `toml:"external_root"`
	LocalBudgetBytes int64  `toml:"local_budget_bytes"`
}

type Router struct {
	Host           string   `toml:"host"`
	Port           int      `toml:"port"`
	ExtraPorts     []int    `toml:"extra_ports"`
	AllowedOrigins []string `toml:"allowed_origins"`
}

type Defaults struct {
	Cache    Cache    `toml:"cache"`
	Watchdog Watchdog `toml:"watchdog"`
	Memlog   Memlog   `toml:"memlog"`
}

// BackendSpec is one entry in the [[backend]] array-of-tables.
type BackendSpec struct {
	Name   string `toml:"name"`
	Engine string `toml:"engine"`

	// Slot/Remote are the canonical vocabulary (see docs/adr/0001, 0002).
	// Slot is the addressable name a request targets; members sharing a slot
	// share memory and swap. Remote proxies an external URL. Everything else
	// is a lazy slot (load on first request, reload on next request if it
	// dies). Mode/Group are the legacy synonyms, still parsed for back-compat;
	// normalize() keeps both sides consistent and the internal supervisor/
	// router code continues to read Mode/Group.
	//
	// Warm is inert: parsed only so old `warm = true` configs still decode
	// (strict decode rejects unknown keys). It normalizes to a lazy slot of
	// one. ponytail: drop once configs are migrated.
	Slot   string `toml:"slot"`
	Warm   bool   `toml:"warm"`
	Remote bool   `toml:"remote"`

	Mode            string    `toml:"mode"`    // legacy: "swap" | "external" (derived from Slot/Remote)
	Group           string    `toml:"group"`   // legacy synonym for Slot
	Default         bool      `toml:"default"` // for slot members: load when the slot is addressed by name
	Host            string    `toml:"host"`
	Port            int       `toml:"port"`
	Model           string    `toml:"model"`
	DraftModel      string    `toml:"draft_model"`
	URL             string    `toml:"url"`               // mode=external
	UpstreamModel   string    `toml:"upstream_model"`    // mode=external
	TrustRemoteCode bool      `toml:"trust_remote_code"` // mlx_lm/mlx_vlm: pass --trust-remote-code
	Sampler         *Sampler  `toml:"sampler"`           // default sampler params for chat-style requests
	Cache           *Cache    `toml:"cache"`             // optional override
	Watchdog        *Watchdog `toml:"watchdog"`
	Memlog          *Memlog   `toml:"memlog"`
}

// Sampler holds default generation parameters mlxctl chat sends with each
// request. mlx_lm.server treats them as request-body overrides over its CLI
// defaults. Zero values are omitted from the request.
type Sampler struct {
	Temperature       float64 `toml:"temperature"`
	TopP              float64 `toml:"top_p"`
	TopK              int     `toml:"top_k"`
	MinP              float64 `toml:"min_p"`
	RepetitionPenalty float64 `toml:"repetition_penalty"`
	MaxTokens         int     `toml:"max_tokens"`
}

type Cache struct {
	LimitBytes          int64 `toml:"limit_bytes"`
	ClearIntervalSec    int   `toml:"clear_interval_sec"`
	ClearThresholdBytes int64 `toml:"clear_threshold_bytes"`
}

type Watchdog struct {
	KVHeadroomBytes  int64 `toml:"kv_headroom_bytes"`
	CheckIntervalSec int   `toml:"check_interval_sec"`
	GraceSec         int   `toml:"grace_sec"`
}

type Memlog struct {
	IntervalSec int `toml:"interval_sec"`
}

// normalize reconciles the canonical slot/remote fields with the legacy
// mode/group fields so that, whichever the user authored, both sides are
// consistent. The internal supervisor and router read Mode/Group; the CLI and
// migrator read Slot/Remote. Derivation:
//
//   - remote (or legacy mode=external)  -> Mode=external, slot of one
//   - otherwise                         -> Mode=swap (lazy), slot defaults to name
//
// Every backend loads lazily on first request through one Group path; a
// singleton (embed/audio, or a lone lm/vlm) is just a slot of one. There is no
// warm/persistent lifecycle: a crashed worker reloads on the next request.
// ponytail: Warm is parsed but inert, kept only so existing `warm = true`
// configs still decode (strict decode rejects unknown keys); drop the field
// once configs are migrated.
func (b *BackendSpec) normalize() {
	// Absorb legacy mode into the new flags. Legacy persistent collapses into
	// the lazy swap path like everything else.
	if b.Mode == "external" {
		b.Remote = true
	}
	// group is the legacy synonym for slot.
	if b.Slot == "" {
		b.Slot = b.Group
	}

	if b.Remote {
		b.Mode = "external"
		if b.Slot == "" {
			b.Slot = b.Name
		}
		if b.UpstreamModel == "" {
			b.UpstreamModel = b.Name
		}
		b.Group = b.Slot
		return
	}

	// Lazy slot: loads on demand, swaps with slot-mates. A bare model with no
	// slot becomes its own slot of one.
	b.Mode = "swap"
	if b.Slot == "" {
		b.Slot = b.Name
	}
	b.Group = b.Slot
}

func (b BackendSpec) EffectiveCache(d Defaults) Cache {
	if b.Cache == nil {
		return d.Cache
	}
	return *b.Cache
}

func (b BackendSpec) EffectiveWatchdog(d Defaults) Watchdog {
	if b.Watchdog == nil {
		return d.Watchdog
	}
	return *b.Watchdog
}

func (b BackendSpec) EffectiveMemlog(d Defaults) Memlog {
	if b.Memlog == nil {
		return d.Memlog
	}
	return *b.Memlog
}

func (c *Config) Validate() error {
	if c.PythonBin == "" {
		return fmt.Errorf("python_bin: required")
	}
	if c.Router.Port <= 0 || c.Router.Port > 65535 {
		return fmt.Errorf("router.port: must be 1..65535, got %d", c.Router.Port)
	}
	for _, p := range c.Router.ExtraPorts {
		if p <= 0 || p > 65535 {
			return fmt.Errorf("router.extra_ports: %d out of range", p)
		}
	}
	if len(c.Backends) == 0 {
		return fmt.Errorf("at least one [[backend]] entry required")
	}

	validEngines := map[string]bool{"lm": true, "vlm": true, "audio": true, "embed": true}
	seen := map[string]bool{}
	groupPorts := map[string]int{}
	groupDefaults := map[string]int{}

	for i, b := range c.Backends {
		idx := fmt.Sprintf("backend[%d:%s]", i, b.Name)
		if b.Name == "" {
			return fmt.Errorf("%s: name required", idx)
		}
		if seen[b.Name] {
			return fmt.Errorf("%s: duplicate name %q", idx, b.Name)
		}
		seen[b.Name] = true

		switch b.Mode {
		case "swap":
			if !validEngines[b.Engine] {
				return fmt.Errorf("%s.engine: must be lm|vlm|audio|embed, got %q", idx, b.Engine)
			}
			// audio servers (TTS) take no model path; everything else needs one.
			if b.Engine != "audio" && b.Model == "" {
				return fmt.Errorf("%s.model: required for engine=%s", idx, b.Engine)
			}
			if b.Host == "" || b.Port <= 0 {
				return fmt.Errorf("%s: host+port required", idx)
			}
			group := b.Group
			if group == "" {
				return fmt.Errorf("%s.group: required for swap mode", idx)
			}
			if p, ok := groupPorts[group]; ok && p != b.Port {
				return fmt.Errorf("%s.port: swap members of group %q must share a port (got %d vs %d)", idx, group, b.Port, p)
			}
			groupPorts[group] = b.Port
			if b.Default {
				groupDefaults[group]++
			}
		case "external":
			if b.URL == "" {
				return fmt.Errorf("%s.url: required for external mode", idx)
			}
		default:
			return fmt.Errorf("%s.mode: must be 'swap' or 'external', got %q", idx, b.Mode)
		}
	}

	for g, n := range groupDefaults {
		if n > 1 {
			return fmt.Errorf("group %q: only one backend may have default=true (got %d)", g, n)
		}
	}

	if c.Offload != nil {
		if c.Offload.ExternalRoot == "" {
			return fmt.Errorf("offload.external_root: required when [offload] is set")
		}
		if c.ModelsRoot == "" {
			return fmt.Errorf("offload: models_root required (it is the cache root)")
		}
	}
	return nil
}

// BackendsByGroup returns swap-mode backends grouped by their Group name,
// preserving declaration order within each group.
func (c *Config) BackendsByGroup() map[string][]BackendSpec {
	out := map[string][]BackendSpec{}
	for _, b := range c.Backends {
		if b.Mode == "swap" {
			out[b.Group] = append(out[b.Group], b)
		}
	}
	return out
}

func (c *Config) Externals() []BackendSpec {
	out := []BackendSpec{}
	for _, b := range c.Backends {
		if b.Mode == "external" {
			out = append(out, b)
		}
	}
	return out
}

// AllNames returns every backend name and every swap group name
// (groups may not match a backend name; e.g. group "chat" has members
// "valkyrie"/"scout"). The router's catalog and registry consume this.
func (c *Config) AllNames() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, b := range c.Backends {
		if !seen[b.Name] {
			seen[b.Name] = true
			out = append(out, b.Name)
		}
	}
	for _, b := range c.Backends {
		if b.Mode == "swap" && !seen[b.Group] {
			seen[b.Group] = true
			out = append(out, b.Group)
		}
	}
	return out
}
