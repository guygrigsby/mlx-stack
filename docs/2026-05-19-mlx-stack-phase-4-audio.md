# mlx-stack Phase 4 Implementation Plan — Audio (TTS + Kokoro)

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`.

**Goal:** Spawn two managed audio backends — `tts` and `kokoro` — each running `mlx_audio.server` on its own port. Route `POST /v1/audio/*` through the router.

**Architecture:** Reuse `supervisor.Managed` from Phase 2. Add `--engine audio` to `launcher_shim.py` that dispatches to `mlx_audio.server.main()`. `mlx_audio.server` has built-in multi-model support, so each instance is preloaded with a `models = [...]` list from config.

---

## Task 1: launcher_shim — `--engine audio`

**Files:**
- Modify: `python/mlx_stack/launcher_shim.py`
- Modify: `python/tests/test_launcher_shim.py`

Add `"audio"` to `--engine` choices. In `main()`, when `engine == "audio"`:

- Apply NO patches (audio doesn't use mlx_lm.server).
- Skip watchdog/cache-janitor (no KV).
- Optionally start memlog if env set.
- Splice argv for `mlx_audio.server`: typically `mlx_audio.server --host H --port P --model M1 --model M2 ...` — verify with `~/venvs/mlx/bin/python -m mlx_audio.server --help`. The launcher takes a `--models` flag taking a comma-separated list, OR multiple `--model` flags. **The implementer must verify the upstream's actual flag shape before writing the dispatch.**

Add an optional `--audio-models` argparse flag in `launcher_shim.py` that takes a comma-separated list. In `build_server_argv` for audio, emit whatever `mlx_audio.server` expects.

- [ ] **Step 1: Verify upstream interface**

```
~/venvs/mlx/bin/python -m mlx_audio.server --help 2>&1 | head -40
```

Adjust `build_server_argv_audio()` to match.

- [ ] **Step 2: Failing test**

```python
def test_parse_args_accepts_audio_engine():
    from mlx_stack import launcher_shim
    args = launcher_shim.parse_args([
        "--engine", "audio",
        "--port", "1237",
        "--audio-models", "/m/tts,/m/kokoro",
    ])
    assert args.engine == "audio"
    assert args.audio_models == "/m/tts,/m/kokoro"

def test_build_server_argv_audio_emits_models():
    from mlx_stack import launcher_shim
    import types
    args = types.SimpleNamespace(
        engine="audio", model="", draft_model="", host="127.0.0.1", port=1237,
        audio_models="/m/tts,/m/kokoro",
    )
    argv = launcher_shim.build_server_argv(args)
    assert "mlx_audio.server" in argv[0] or argv[0] == "mlx_audio.server"
    # whatever flag mlx_audio.server takes — verify the implementer picked correctly
```

The exact assertions depend on mlx_audio.server's interface. Adjust after step 1.

- [ ] **Step 3: Implement**

- Add `--model` optional (audio doesn't need a single --model since it uses --audio-models).
- Add `--audio-models` flag, default `""`.
- In `build_server_argv`, when `engine == "audio"`, emit `["mlx_audio.server", "--host", H, "--port", P]` plus whatever model flags are needed.

- [ ] **Step 4: Run + commit.**

```
~/venvs/mlx/bin/python -m pytest python/tests/test_launcher_shim.py -v
git add python/mlx_stack/launcher_shim.py python/tests/test_launcher_shim.py
git commit -m "feat: launcher_shim --engine audio dispatch"
```

---

## Task 2: Config — [tts] and [kokoro] sections

**Files:**
- Modify: `internal/config/schema.go`
- Modify: `internal/config/schema_test.go`

Both use the same struct:

```go
type AudioInstance struct {
	Host   string   `toml:"host"`
	Port   int      `toml:"port"`
	Engine string   `toml:"engine"` // must be "audio"
	Models []string `toml:"models"`
	Alias  string   `toml:"alias"`
	Cache  Cache    `toml:"cache"`
	Memlog Memlog   `toml:"memlog"`
}
```

On Config:

```go
TTS    AudioInstance `toml:"tts"`
Kokoro AudioInstance `toml:"kokoro"`
```

Validation per instance (called for each):

```go
func (a AudioInstance) Validate(name string) error {
	if a.Alias == "" && a.Port == 0 && len(a.Models) == 0 {
		return nil // not configured
	}
	if a.Port <= 0 || a.Port > 65535 {
		return fmt.Errorf("%s.port: must be 1..65535", name)
	}
	if a.Engine != "audio" {
		return fmt.Errorf("%s.engine: must be 'audio', got %q", name, a.Engine)
	}
	if len(a.Models) == 0 {
		return fmt.Errorf("%s.models: at least one required", name)
	}
	if a.Alias == "" {
		return fmt.Errorf("%s.alias: required", name)
	}
	return nil
}
```

Call from `Config.Validate()`:

```go
if err := c.TTS.Validate("tts"); err != nil { return err }
if err := c.Kokoro.Validate("kokoro"); err != nil { return err }
```

Expand `~` in model paths in loader.go:

```go
for i, m := range c.TTS.Models { c.TTS.Models[i] = expandHome(m) }
for i, m := range c.Kokoro.Models { c.Kokoro.Models[i] = expandHome(m) }
```

Tests, run, commit:

```
go test ./internal/config/...
git commit -m "feat(config): tts + kokoro audio sections"
```

---

## Task 3: cmd/mlxd — wire TTS and Kokoro

**Files:**
- Modify: `cmd/mlxd/main.go`

Helper to build an audio Managed:

```go
func newAudioManaged(name string, ai config.AudioInstance, cfg *config.Config, logger *slog.Logger) *supervisor.Managed {
	if ai.Alias == "" { return nil }
	models := strings.Join(ai.Models, ",")
	return supervisor.NewManaged(supervisor.ManagedOpts{
		Name:          name,
		Host:          ai.Host,
		Port:          ai.Port,
		Alias:         ai.Alias,
		UpstreamModel: "",  // audio uses "model" field per-request from the multi-model set
		Args: []string{
			"-m", "mlx_stack.launcher_shim",
			"--engine", "audio",
			"--host", ai.Host,
			"--port", fmt.Sprintf("%d", ai.Port),
			"--audio-models", models,
		},
		WorkerFactory: func(args []string) *supervisor.Worker {
			return supervisor.New(supervisor.WorkerSpec{
				Name: name, Command: cfg.PythonBin, Args: args, Logger: logger,
			})
		},
	})
}
```

Use:

```go
ttsMgr := newAudioManaged("tts", cfg.TTS, cfg, logger)
kokoroMgr := newAudioManaged("kokoro", cfg.Kokoro, cfg, logger)
for _, m := range []*supervisor.Managed{ttsMgr, kokoroMgr} {
	if m == nil { continue }
	if err := m.Start(context.Background()); err != nil {
		logger.Error("audio start", "name", m.Name(), "err", err)
	}
}
```

Wire into the registry:

```go
var managed []router.ManagedBackend
for _, m := range []*supervisor.Managed{tagsMgr, ttsMgr, kokoroMgr} {
	if m != nil { managed = append(managed, m) }
}
if embedBackend != nil { managed = append(managed, embedBackend) }
registry := router.NewRegistry(cfg, chatSwap, managed...)
```

Add `Name()` method to `Managed`:

```go
func (m *Managed) Name() string { return m.opts.Name }
```

Add a `/v1/audio/speech` route to router that consults registry. Since the audio request body uses `"model"` field for the voice/model name, the generic `handleProxyByModel` works.

Add to `Server.Handler()`:

```go
mux.HandleFunc("POST /v1/audio/speech", s.handleProxyByModel)
mux.HandleFunc("POST /v1/audio/transcriptions", s.handleProxyByModel)
```

Audio aliases (tts, kokoro) are resolved by request `model` field — but the alias in config is just `"tts"` / `"kokoro"`, while the actual `model` in requests will be the model name (e.g., `"omnivoice"`). Need to think: does the user send `{"model":"tts"}` or `{"model":"omnivoice"}`?

**Convention:** when alias matches, route to that backend. The backend itself (mlx_audio.server) handles multi-model dispatch via its own `model` field. So if the user sends `{"model":"omnivoice"}` and `omnivoice` is in `tts.models`, we need to map that. For Phase 4 we keep it simple: **client must send the alias (`tts` or `kokoro`)**. Mapping happens at the backend's own /v1/models list.

Update Registry to also resolve aliases by membership in `audio.models`:

Add to Registry:

```go
type aliasRule struct {
	matches func(string) bool
	target  ManagedBackend
}
type Registry struct {
	cfg     *config.Config
	chat    ChatSwapper
	managed map[string]ManagedBackend
	audio   []aliasRule  // per-model membership for audio backends
}
```

In `NewRegistry`, also accept audio backends with their model lists and build the audio rules. Resolution order: chat profiles, then managed aliases, then audio rules.

Actually simpler: add each audio model name to the managed map pointing at the right backend:

```go
for _, ai := range []struct{ ai config.AudioInstance; mb ManagedBackend }{
	{cfg.TTS, ttsBackend}, {cfg.Kokoro, kokoroBackend},
} {
	if ai.mb != nil {
		for _, m := range ai.ai.Models {
			// extract last path segment as alias key
			key := filepath.Base(m)
			r.managed[key] = ai.mb
		}
	}
}
```

(Implementer: decide between alias-only or per-model resolution based on what feels right; for Phase 4 the simpler "client uses tts/kokoro alias" works.)

Commit:

```
go test ./...
git commit -m "feat: wire tts + kokoro managed backends"
```

---

## Task 4: e2e — audio request

**Files:**
- Modify: `e2e/e2e_test.go`

Configure `[tts]` with fake-python → fakemlx (extend fakemlx to accept `--audio-models` and ignore). Hit `POST /v1/audio/speech {"model":"tts","input":"hi"}` — verify the request reaches the fake backend (fakemlx should also handle this route with a canned response).

```
go test ./e2e/...
git commit -m "test(e2e): audio backend serves request"
```

---

## Acceptance

- `make test` green.
- `curl -X POST http://127.0.0.1:1231/v1/audio/speech -d '{"model":"tts","input":"hello","voice":"omnivoice"}'` produces audio (manual smoke).
