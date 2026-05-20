package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

func Load(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("config load %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}

	c.LogDir = expandHome(c.LogDir)
	c.ModelsRoot = expandHome(c.ModelsRoot)
	c.PythonBin = expandHome(c.PythonBin)
	c.Tags.Model = expandHome(c.Tags.Model)
	c.Embed.Model = expandHome(c.Embed.Model)
	for name, prof := range c.Chat.Profiles {
		prof.Model = expandHome(prof.Model)
		prof.Draft = expandHome(prof.Draft)
		c.Chat.Profiles[name] = prof
	}
	for i, m := range c.TTS.Models {
		c.TTS.Models[i] = expandHome(m)
	}
	for i, m := range c.Kokoro.Models {
		c.Kokoro.Models[i] = expandHome(m)
	}

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}

func expandHome(p string) string {
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
