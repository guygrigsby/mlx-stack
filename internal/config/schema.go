package config

import "fmt"

type Config struct {
	LogDir     string `toml:"log_dir"`
	ModelsRoot string `toml:"models_root"`
	PythonBin  string `toml:"python_bin"`
	Router     Router `toml:"router"`
	Chat       Chat   `toml:"chat"`
	Tags       Tags   `toml:"tags"`
	Embed      Embed  `toml:"embed"`
}

type Embed struct {
	Managed bool   `toml:"managed"`
	Host    string `toml:"host"`
	Port    int    `toml:"port"`
	Model   string `toml:"model"`
	Alias   string `toml:"alias"`
	URL     string `toml:"url"`
}

type Router struct {
	Host           string   `toml:"host"`
	Port           int      `toml:"port"`
	ExtraPorts     []int    `toml:"extra_ports"`
	AllowedOrigins []string `toml:"allowed_origins"`
}

type Chat struct {
	DefaultProfile string             `toml:"default_profile"`
	Host           string             `toml:"host"`
	Port           int                `toml:"port"`
	SwapTimeoutSec int                `toml:"swap_timeout_sec"`
	Cache          Cache              `toml:"cache"`
	Watchdog       Watchdog           `toml:"watchdog"`
	Memlog         Memlog             `toml:"memlog"`
	Profiles       map[string]Profile `toml:"profiles"`
}

type Profile struct {
	Model  string `toml:"model"`
	Draft  string `toml:"draft"`
	Engine string `toml:"engine"`
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

type Tags struct {
	Host     string   `toml:"host"`
	Port     int      `toml:"port"`
	Model    string   `toml:"model"`
	Engine   string   `toml:"engine"`
	Alias    string   `toml:"alias"`
	Cache    Cache    `toml:"cache"`
	Watchdog Watchdog `toml:"watchdog"`
	Memlog   Memlog   `toml:"memlog"`
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
	if c.Chat.Port <= 0 || c.Chat.Port > 65535 {
		return fmt.Errorf("chat.port: must be 1..65535, got %d", c.Chat.Port)
	}
	if len(c.Chat.Profiles) == 0 {
		return fmt.Errorf("chat.profiles: at least one required")
	}
	if _, ok := c.Chat.Profiles[c.Chat.DefaultProfile]; !ok {
		return fmt.Errorf("chat.default_profile %q: not found among profiles", c.Chat.DefaultProfile)
	}
	for name, prof := range c.Chat.Profiles {
		if prof.Model == "" {
			return fmt.Errorf("chat.profiles.%s.model: required", name)
		}
		if prof.Engine != "lm" && prof.Engine != "vlm" {
			return fmt.Errorf("chat.profiles.%s.engine: must be 'lm' or 'vlm', got %q", name, prof.Engine)
		}
	}
	if c.Tags.Model != "" || c.Tags.Port != 0 || c.Tags.Alias != "" {
		if c.Tags.Port <= 0 || c.Tags.Port > 65535 {
			return fmt.Errorf("tags.port: must be 1..65535, got %d", c.Tags.Port)
		}
		if c.Tags.Model == "" {
			return fmt.Errorf("tags.model: required when tags configured")
		}
		if c.Tags.Engine != "lm" && c.Tags.Engine != "vlm" {
			return fmt.Errorf("tags.engine: must be 'lm' or 'vlm', got %q", c.Tags.Engine)
		}
		if c.Tags.Alias == "" {
			return fmt.Errorf("tags.alias: required when tags configured")
		}
	}
	if c.Embed.Alias != "" || c.Embed.Model != "" || c.Embed.URL != "" || c.Embed.Port != 0 {
		if c.Embed.Alias == "" {
			return fmt.Errorf("embed.alias: required when embed configured")
		}
		if c.Embed.Managed {
			if c.Embed.Port <= 0 || c.Embed.Port > 65535 {
				return fmt.Errorf("embed.port: must be 1..65535, got %d", c.Embed.Port)
			}
			if c.Embed.Model == "" {
				return fmt.Errorf("embed.model: required when embed managed")
			}
		} else {
			if c.Embed.URL == "" {
				return fmt.Errorf("embed.url: required when embed external (managed=false)")
			}
		}
	}
	return nil
}
