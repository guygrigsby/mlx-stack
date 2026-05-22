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
	Backends   []BackendSpec `toml:"backend"`
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
	Name            string    `toml:"name"`
	Engine          string    `toml:"engine"`
	Mode            string    `toml:"mode"`              // "swap" | "persistent" | "external"
	Group           string    `toml:"group"`             // for swap mode; defaults to Name
	Default         bool      `toml:"default"`           // for swap members: auto-load on group start
	Host            string    `toml:"host"`
	Port            int       `toml:"port"`
	Model           string    `toml:"model"`
	DraftModel      string    `toml:"draft_model"`
	URL             string    `toml:"url"`               // mode=external
	UpstreamModel   string    `toml:"upstream_model"`    // mode=external
	TrustRemoteCode bool      `toml:"trust_remote_code"` // mlx_lm/mlx_vlm: pass --trust-remote-code
	Cache           *Cache    `toml:"cache"`             // optional override
	Watchdog        *Watchdog `toml:"watchdog"`
	Memlog          *Memlog   `toml:"memlog"`
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
			if b.Engine != "lm" && b.Engine != "vlm" {
				return fmt.Errorf("%s.engine: must be 'lm' or 'vlm' for swap mode, got %q", idx, b.Engine)
			}
			if b.Model == "" {
				return fmt.Errorf("%s.model: required", idx)
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
		case "persistent":
			if !validEngines[b.Engine] {
				return fmt.Errorf("%s.engine: must be lm|vlm|audio|embed, got %q", idx, b.Engine)
			}
			if b.Host == "" || b.Port <= 0 {
				return fmt.Errorf("%s: host+port required", idx)
			}
			if b.Engine != "audio" && b.Model == "" {
				return fmt.Errorf("%s.model: required for engine=%s", idx, b.Engine)
			}
		case "external":
			if b.URL == "" {
				return fmt.Errorf("%s.url: required for external mode", idx)
			}
		default:
			return fmt.Errorf("%s.mode: must be 'swap', 'persistent', or 'external', got %q", idx, b.Mode)
		}
	}

	for g, n := range groupDefaults {
		if n > 1 {
			return fmt.Errorf("group %q: only one backend may have default=true (got %d)", g, n)
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

func (c *Config) Persistents() []BackendSpec {
	out := []BackendSpec{}
	for _, b := range c.Backends {
		if b.Mode == "persistent" {
			out = append(out, b)
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
