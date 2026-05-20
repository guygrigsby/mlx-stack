package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/guygrigsby/mlx-stack/internal/config"
)

func cmdConfig(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl config <migrate|show>")
		os.Exit(2)
	}
	switch args[0] {
	case "migrate":
		cmdConfigMigrate(args[1:])
	case "show":
		cmdConfigShow(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown: mlxctl config %s\n", args[0])
		os.Exit(2)
	}
}

// legacyVars is the ordered list of scalar variables to extract from the
// legacy zsh config.
var legacyVars = []string{
	"MLX_VENV",
	"MLX_LOG_DIR",
	"MLX_HOST",
	"MODELS_ROOT",
	"ALLOWED_ORIGINS",
	"CHAT_PORT",
	"CHAT_PROFILE_DEFAULT",
	"CHAT_CACHE_LIMIT_BYTES",
	"CHAT_CACHE_CLEAR_INTERVAL_SEC",
	"CHAT_CACHE_CLEAR_THRESHOLD_BYTES",
	"CHAT_MEMORY_LOG_INTERVAL_SEC",
	"TAGS_MODEL",
	"TAGS_PORT",
	"TAGS_ENGINE",
	"ROUTER_TAGS_ALIAS",
	"TAGS_CACHE_LIMIT_BYTES",
	"TAGS_CACHE_CLEAR_INTERVAL_SEC",
	"TAGS_CACHE_CLEAR_THRESHOLD_BYTES",
	"TAGS_MEMORY_LOG_INTERVAL_SEC",
	"ROUTER_PORT",
	"ROUTER_HOST",
	"ROUTER_EXTRA_PORTS",
	"ROUTER_EMBED_ALIAS",
	"ROUTER_TTS_ALIAS",
	"TTS_MODEL",
	"TTS_PORT",
	"KOKORO_MODEL",
	"KOKORO_PORT",
}

// legacyProfile holds the parsed fields of one CHAT_MODEL_PROFILES entry.
type legacyProfile struct {
	Name   string
	Model  string
	Draft  string
	Engine string
}

func cmdConfigMigrate(args []string) {
	src := os.ExpandEnv("$HOME/.config/mlx.conf")
	if len(args) > 0 {
		src = args[0]
	}
	if _, err := os.Stat(src); err != nil {
		fmt.Fprintln(os.Stderr, "cannot read", src, ":", err)
		os.Exit(1)
	}

	// Build a zsh snippet that sources the legacy config and prints vars/profiles.
	var sb strings.Builder
	fmt.Fprintf(&sb, "source %q\n", src)
	sb.WriteString("echo 'MLX_STACK_VARS_BEGIN'\n")
	for _, v := range legacyVars {
		fmt.Fprintf(&sb, "echo '%s='\"${%s}\"\n", v, v)
	}
	sb.WriteString("echo 'MLX_STACK_VARS_END'\n")
	sb.WriteString("echo 'MLX_STACK_PROFILES_BEGIN'\n")
	sb.WriteString("for k in \"${(@k)CHAT_MODEL_PROFILES}\"; do\n")
	sb.WriteString("  echo \"$k|${CHAT_MODEL_PROFILES[$k]}|${CHAT_DRAFT_PROFILES[$k]:-}|${CHAT_ENGINE_PROFILES[$k]:-lm}\"\n")
	sb.WriteString("done\n")
	sb.WriteString("echo 'MLX_STACK_PROFILES_END'\n")

	cmd := exec.CommandContext(context.Background(), "zsh", "-c", sb.String())
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "source failed:", err)
		os.Exit(1)
	}

	env := map[string]string{}
	var profiles []legacyProfile
	state := "scan"
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		switch line {
		case "MLX_STACK_VARS_BEGIN":
			state = "vars"
			continue
		case "MLX_STACK_VARS_END":
			state = "scan"
			continue
		case "MLX_STACK_PROFILES_BEGIN":
			state = "profiles"
			continue
		case "MLX_STACK_PROFILES_END":
			state = "scan"
			continue
		}
		switch state {
		case "vars":
			k, v, ok := strings.Cut(line, "=")
			if ok {
				env[k] = v
			}
		case "profiles":
			parts := strings.SplitN(line, "|", 4)
			if len(parts) == 4 {
				profiles = append(profiles, legacyProfile{
					Name:   parts[0],
					Model:  parts[1],
					Draft:  parts[2],
					Engine: parts[3],
				})
			}
		}
	}

	cfg := buildConfig(env, profiles)
	enc := toml.NewEncoder(os.Stdout)
	if err := enc.Encode(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func parsePortList(s string) []int {
	if s == "" {
		return nil
	}
	// Legacy conf uses comma-separated OR space-separated.
	s = strings.ReplaceAll(s, ",", " ")
	parts := strings.Fields(s)
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		if n := atoi(p); n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// buildConfig converts the legacy env vars and profile list into the new
// [[backend]] array-of-tables Config shape.
func buildConfig(env map[string]string, profiles []legacyProfile) *config.Config {
	host := firstNonEmpty(env["ROUTER_HOST"], env["MLX_HOST"], "127.0.0.1")
	chatPort := atoi(firstNonEmpty(env["CHAT_PORT"], "1234"))

	c := &config.Config{
		LogDir:     env["MLX_LOG_DIR"],
		ModelsRoot: env["MODELS_ROOT"],
		PythonBin:  env["MLX_VENV"] + "/bin/python",
		Router: config.Router{
			Host:       host,
			Port:       atoi(firstNonEmpty(env["ROUTER_PORT"], "8080")),
			ExtraPorts: parsePortList(env["ROUTER_EXTRA_PORTS"]),
		},
	}

	defaultProfile := env["CHAT_PROFILE_DEFAULT"]

	// Emit one swap-mode backend per legacy chat profile.
	for _, p := range profiles {
		spec := config.BackendSpec{
			Name:    p.Name,
			Engine:  p.Engine,
			Mode:    "swap",
			Group:   "chat",
			Host:    host,
			Port:    chatPort,
			Model:   p.Model,
			Default: p.Name == defaultProfile,
		}
		if p.Draft != "" {
			spec.DraftModel = p.Draft
		}
		// Attach cache/memlog from legacy CHAT_* vars if set.
		cl := atoi64(env["CHAT_CACHE_LIMIT_BYTES"])
		ci := atoi(env["CHAT_CACHE_CLEAR_INTERVAL_SEC"])
		ct := atoi64(env["CHAT_CACHE_CLEAR_THRESHOLD_BYTES"])
		mi := atoi(env["CHAT_MEMORY_LOG_INTERVAL_SEC"])
		if cl != 0 || ci != 0 || ct != 0 {
			spec.Cache = &config.Cache{
				LimitBytes:          cl,
				ClearIntervalSec:    ci,
				ClearThresholdBytes: ct,
			}
		}
		if mi != 0 {
			spec.Memlog = &config.Memlog{IntervalSec: mi}
		}
		c.Backends = append(c.Backends, spec)
	}

	// Tags backend.
	if env["TAGS_MODEL"] != "" {
		tagsAlias := firstNonEmpty(env["ROUTER_TAGS_ALIAS"], filepath.Base(env["TAGS_MODEL"]))
		spec := config.BackendSpec{
			Name:   tagsAlias,
			Engine: firstNonEmpty(env["TAGS_ENGINE"], "vlm"),
			Mode:   "persistent",
			Host:   host,
			Port:   atoi(env["TAGS_PORT"]),
			Model:  env["TAGS_MODEL"],
		}
		cl := atoi64(env["TAGS_CACHE_LIMIT_BYTES"])
		ci := atoi(env["TAGS_CACHE_CLEAR_INTERVAL_SEC"])
		ct := atoi64(env["TAGS_CACHE_CLEAR_THRESHOLD_BYTES"])
		mi := atoi(env["TAGS_MEMORY_LOG_INTERVAL_SEC"])
		if cl != 0 || ci != 0 || ct != 0 {
			spec.Cache = &config.Cache{
				LimitBytes:          cl,
				ClearIntervalSec:    ci,
				ClearThresholdBytes: ct,
			}
		}
		if mi != 0 {
			spec.Memlog = &config.Memlog{IntervalSec: mi}
		}
		c.Backends = append(c.Backends, spec)
	}

	// TTS backend.
	if env["TTS_MODEL"] != "" {
		ttsAlias := firstNonEmpty(env["ROUTER_TTS_ALIAS"], filepath.Base(env["TTS_MODEL"]))
		c.Backends = append(c.Backends, config.BackendSpec{
			Name:   ttsAlias,
			Engine: "audio",
			Mode:   "persistent",
			Host:   host,
			Port:   atoi(firstNonEmpty(env["TTS_PORT"], "1237")),
		})
	}

	// Kokoro backend.
	if env["KOKORO_MODEL"] != "" {
		c.Backends = append(c.Backends, config.BackendSpec{
			Name:   "kokoro",
			Engine: "audio",
			Mode:   "persistent",
			Host:   host,
			Port:   atoi(firstNonEmpty(env["KOKORO_PORT"], "8880")),
		})
	}

	return c
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func cmdConfigShow(args []string) {
	path := os.ExpandEnv("$HOME/.config/mlx/config.toml")
	resolved := false

	for _, a := range args {
		if a == "--resolved" {
			resolved = true
		} else if !strings.HasPrefix(a, "-") {
			path = a
		}
	}

	if !resolved {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(string(b))
		return
	}

	c, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := toml.NewEncoder(os.Stdout).Encode(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
