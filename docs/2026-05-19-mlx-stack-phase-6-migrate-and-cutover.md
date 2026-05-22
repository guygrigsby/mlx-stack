# mlx-stack Phase 6 Implementation Plan — TOML Migration + Port Cutover

> **Status (2026-05-22):** partially shipped, partially reverted. Port cutover (router on 1230 + 8080, legacy scripts moved to `legacy/`) shipped. **The `mlxctl config migrate` command described here was removed** in commit `ab482fc feat: remove config migrate` — `~/.config/mlx.conf` is long gone and migration has no target. The TOML shape the migrator would have produced was itself later replaced by phase 8's `[[backend]]` array. CLI is `mlxctl`. Current schema: phase 8 + README.

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`.

**Goal:** Provide a `mlxctl config migrate` command that reads the legacy `~/.config/mlx.conf` shell file and emits an equivalent `~/.config/mlx/config.toml`. Cut router defaults over to ports 1230 + 8080 (matching today's stack). Archive the legacy zsh + Python scripts into `legacy/`.

---

## Task 1: mlxctl config migrate

**Files:**
- Create: `cmd/mlxctl/config.go`
- Modify: `cmd/mlxctl/main.go`

The migrator:
1. Takes a path to the old `.conf` (default: `~/.config/mlx.conf`).
2. Runs `zsh -c "source <path> && env"` (or `set -a; source; printenv`) to dump the resolved env.
3. Parses known variables (CHAT_PORT, CHAT_MODEL_PROFILES, ROUTER_STATICS, CHAT_CACHE_LIMIT_BYTES, etc.).
4. Emits a TOML document to stdout.

Reference the legacy variable names from the spec's appendix. Known shell variables (non-exhaustive):
- `CHAT_PORT`, `CHAT_HOST`
- `CHAT_MODEL_PROFILES` — newline-separated `name:path:draft:engine` lines (verify exact format from the user's file).
- `CHAT_CACHE_LIMIT_BYTES`, `CHAT_KV_HEADROOM_BYTES`, etc.
- `TAGS_*` → emitted as `[tags]`
- `EMBED_*` → emitted as `[embed]`
- `TTS_*`, `KOKORO_*` → `[tts]`, `[kokoro]`
- `ROUTER_PORT`, `ROUTER_EXTRA_PORTS`, `ROUTER_STATICS`

Skeleton:

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func cmdConfigMigrate(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl config migrate <path-to-mlx.conf>")
		os.Exit(2)
	}
	src := args[0]

	cmd := exec.CommandContext(context.Background(), "zsh", "-c",
		fmt.Sprintf("set -a; source %q; env", src))
	out, err := cmd.Output()
	if err != nil { fmt.Fprintln(os.Stderr, "source failed:", err); os.Exit(1) }

	env := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		k, v, ok := strings.Cut(line, "=")
		if ok { env[k] = v }
	}
	emitTOML(env)
}

func emitTOML(env map[string]string) {
	// build [router] [chat] [tags] [embed] [tts] [kokoro] sections from env.
	// ... (write to stdout)
}
```

Implementer:
1. Pull the actual variable names by running `~/scripts/mlx-router.py --help` or `grep -E "^[A-Z_]+=" ~/.config/mlx.conf` first.
2. Write `emitTOML` to produce a valid config file.
3. Test the round-trip: migrate a sample, load with `config.Load`, verify shape.

Add CLI:

```go
case "config":
	if len(os.Args) < 3 { /* usage */ }
	switch os.Args[2] {
	case "migrate": cmdConfigMigrate(os.Args[3:])
	case "show": cmdConfigShow(os.Args[3:])
	default: /* usage */
	}
```

Commit:

```
git add cmd/mlxctl/config.go cmd/mlxctl/main.go
git commit -m "feat(mlxctl): config migrate"
```

---

## Task 2: mlxctl config show

**Files:**
- Modify: `cmd/mlxctl/config.go`

Reads the current TOML config, prints it (resolved, with `~` expanded). Useful for users to verify their config:

```go
func cmdConfigShow(args []string) {
	path := defaultConfigPath()
	if len(args) > 0 { path = args[0] }
	b, err := os.ReadFile(path)
	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	fmt.Print(string(b))
}
```

Optional flag `--resolved` to load via `config.Load()` and re-serialize with expanded paths (use `BurntSushi/toml.Encoder`).

Commit.

---

## Task 3: Port cutover defaults

**Files:**
- Modify: `internal/config/schema.go` (no change actually — defaults come from user config)
- Modify: example config in `README.md` to recommend port 1230 + extra_ports=[8080]

No code change to defaults — config is required. But update docs to recommend the production port set. Maybe also: when `mlxd run` starts and the router port matches the legacy stack's, log a warning if the legacy `mlx-router.py` PID is detected (best-effort heuristic via `pgrep`).

Commit:

```
git add README.md
git commit -m "docs: recommend port 1230 + 8080 for production cutover"
```

---

## Task 4: Archive legacy scripts

**Files:**
- Create: `legacy/README.md`
- Move (copy then commit): `~/scripts/mlx`, `~/scripts/mlx-router.py`, `~/scripts/mlx-server-launch.py`, `~/scripts/mlx-embed-server.py`, `~/.config/mlx.conf` → `legacy/`

The mlx-stack repo absorbs these files for archival. **The implementer must verify with the user that the source paths exist and confirm before copying.**

```
mkdir -p legacy
cp ~/scripts/mlx legacy/mlx
cp ~/scripts/mlx-router.py legacy/mlx-router.py
cp ~/scripts/mlx-server-launch.py legacy/mlx-server-launch.py
cp ~/scripts/mlx-embed-server.py legacy/mlx-embed-server.py
cp ~/.config/mlx.conf legacy/mlx.conf
cat > legacy/README.md <<'EOF'
# Archived legacy scripts

Pre-mlx-stack inference setup. Kept for reference. Replaced by `mlxd` + `mlxctl` in this repo.

DO NOT run these — they conflict with mlxd on port 1230.
EOF

git add legacy/
git commit -m "chore: archive legacy zsh + Python scripts"
```

---

## Acceptance

- `mlxctl config migrate ~/.config/mlx.conf > /tmp/out.toml && go run ./cmd/mlxd run --config /tmp/out.toml` works.
- Old scripts visible under `legacy/` with a clear README.
- README documents the cutover steps.
