# mlx-stack Phase 5 Implementation Plan — CLI Parity

> **Status (2026-05-22):** shipped, then re-shaped. mlxctl was later ported to spf13/cobra (commit `2e5c456`), so the hand-rolled subcommand dispatch in this plan no longer matches the code. The chat command was also rewritten — see `2026-05-22-runtime-stability-and-samplers.md` for the current shape (SSE streaming, per-backend samplers, config-driven router URL). `mlxctl add` / `scan` / `bootstrap` were added later. Current CLI surface: README + `mlxctl --help`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`.

**Goal:** Replace remaining zsh helpers with `mlxctl` subcommands: `monitor`, `tail`, `chat`, `tag`, `tags`. Ship a launchd plist template.

**Architecture:** Admin API gains `GET /v1/logs/tail?backend=…` (SSE stream) and `GET /v1/status` is extended with mem/timing fields. CLI subcommands consume them.

---

## Task 1: Admin /v1/logs/tail SSE

**Files:**
- Modify: `internal/admin/handlers.go`
- Modify: `internal/admin/handlers_test.go`
- Modify: `cmd/mlxd/main.go`

Add a log multiplexer in mlxd: a goroutine subscribes to each Worker's stderr events and broadcasts to N HTTP subscribers. New `internal/logobs/broker.go`:

```go
package logobs

import (
	"context"
	"sync"
)

type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

func (b *Broker) Publish(ev Event) {
	b.mu.Lock(); defer b.mu.Unlock()
	for c := range b.subs {
		select { case c <- ev: default: }
	}
}

func (b *Broker) Subscribe(ctx context.Context) <-chan Event {
	c := make(chan Event, 256)
	b.mu.Lock(); b.subs[c] = struct{}{}; b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock(); delete(b.subs, c); close(c); b.mu.Unlock()
	}()
	return c
}
```

In `Worker.consumeStderr`, also publish to the broker if one was passed. Add `Broker *logobs.Broker` to `WorkerSpec`.

In `admin.Handlers`, add a `Broker *logobs.Broker` field. Register:

```go
mux.HandleFunc("GET /v1/logs/tail", h.tail)

func (h *Handlers) tail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	flusher := w.(http.Flusher)
	events := h.Broker.Subscribe(r.Context())
	for ev := range events {
		fmt.Fprintf(w, "data: %s\n\n", ev.Raw)
		flusher.Flush()
	}
}
```

Tests: subscribe + publish + receive. Commit:

```
go test ./...
git commit -m "feat(admin): /v1/logs/tail SSE multiplexer"
```

---

## Task 2: mlxctl monitor — TTY refresh

**Files:**
- Create: `cmd/mlxctl/monitor.go`
- Modify: `cmd/mlxctl/main.go`

`mlxctl monitor` polls `/v1/status` every 500ms and redraws the terminal in place. Uses ANSI cursor codes.

```go
func cmdMonitor(args []string) {
	c := newClient()
	for {
		b, err := c.Get(context.Background(), "/v1/status")
		if err != nil { notRunning() }
		// clear screen + home
		fmt.Print("\033[2J\033[H")
		fmt.Println("mlx-stack status")
		fmt.Println(string(b))  // pretty-print later
		time.Sleep(500 * time.Millisecond)
	}
}
```

For Phase 5, raw JSON is acceptable. Phase 7 polishes the table render.

Add `case "monitor": cmdMonitor(...)`. Commit.

---

## Task 3: mlxctl tail — SSE consumer

**Files:**
- Create: `cmd/mlxctl/tail.go`
- Modify: `cmd/mlxctl/main.go`
- Modify: `internal/ipc/client.go` (add `GetStream(path)` returning `io.ReadCloser`)

```go
// In ipc/client.go:
func (c *Client) GetStream(ctx context.Context, path string) (io.ReadCloser, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://mlxd"+path, nil)
	resp, err := c.http.Do(req)
	if err != nil { return nil, err }
	if resp.StatusCode >= 400 { resp.Body.Close(); return nil, fmt.Errorf("status %d", resp.StatusCode) }
	return resp.Body, nil
}
```

```go
// In cmd/mlxctl/tail.go:
func cmdTail(args []string) {
	c := newClient()
	rc, err := c.GetStream(context.Background(), "/v1/logs/tail")
	if err != nil { notRunning() }
	defer rc.Close()
	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			fmt.Println(strings.TrimPrefix(line, "data: "))
		}
	}
}
```

Commit.

---

## Task 4: mlxctl chat / tag / tags

**Files:**
- Create: `cmd/mlxctl/chat.go`

Direct HTTP to the router on port 1231 — not through the admin socket. Helper subcommands:

- `mlxctl chat "..."` → POST /v1/chat/completions with `{model: default_profile, messages: [{role:user, content:...}]}`. Print the response text.
- `mlxctl tag <image-path>` → reads image, POSTs to router with model=tags-alias and a multipart body or base64. Specifics depend on the vlm endpoint shape — implementer verifies with the existing zsh script.
- `mlxctl tags` → lists known profiles + tags alias (just calls /v1/models).

Need router URL: read from config (config.toml) or use default http://127.0.0.1:1231. Use env `MLXD_ROUTER` or default. **Implementer:** for Phase 5, hard-code `http://127.0.0.1:1231` unless env is set.

Test each manually; commit.

---

## Task 5: launchd plist template

**Files:**
- Create: `deploy/dev.grigsby.mlxd.plist.template`
- Modify: `Makefile` (add `install-launchd` target)
- Modify: `README.md` (install instructions)

Template (sed-substituted at install time):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.grigsby.mlxd</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{INSTALL_DIR}}/mlxd</string>
    <string>run</string>
    <string>--config</string>
    <string>{{HOME}}/.config/mlx/config.toml</string>
    <string>--log-json</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>{{HOME}}/.logs/mlx/mlxd-launchd.log</string>
  <key>StandardErrorPath</key><string>{{HOME}}/.logs/mlx/mlxd-launchd.log</string>
  <key>WorkingDirectory</key><string>{{HOME}}</string>
</dict>
</plist>
```

Makefile target:

```makefile
install-launchd: install
	@mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.logs/mlx
	@sed -e "s|{{INSTALL_DIR}}|$(INSTALL_DIR)|g" -e "s|{{HOME}}|$(HOME)|g" \
		deploy/dev.grigsby.mlxd.plist.template \
		> $(HOME)/Library/LaunchAgents/dev.grigsby.mlxd.plist
	@echo "Installed launchd plist. Load: launchctl load ~/Library/LaunchAgents/dev.grigsby.mlxd.plist"
```

Document in README.

Commit:

```
git add deploy/ Makefile README.md
git commit -m "feat: launchd plist template + install target"
```

---

## Acceptance

- `mlxctl monitor` shows live-refreshing status.
- `mlxctl tail` streams stderr events from any worker.
- `mlxctl chat "..."` and `mlxctl tags` produce useful output.
- `make install-launchd` plus `launchctl load …` autostarts mlxd on login.
