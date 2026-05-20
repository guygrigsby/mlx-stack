# mlx-stack Phase 3 Implementation Plan — Embed Backend

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`.

**Goal:** Add an embedding backend with two variants: managed (mlxd spawns a Python `mlx_stack.embed_server` FastAPI app) or external (mlxd just proxies to a fixed URL). Routes `POST /v1/embeddings` through the router.

**Architecture:** Lift the existing `mlx-embed-server.py` content into `python/mlx_stack/embed_server/app.py` (FastAPI). Add `--engine embed` to `launcher_shim.py` (dispatches to `uvicorn.run(app, host=..., port=...)`). On the Go side, add `[embed]` config (`managed=true/false`, `url=...` for external), an `External` backend struct that satisfies `ManagedBackend`, and route `/v1/embeddings` via the router using the same `Registry` pattern from Phase 2.

**Out of scope:** Audio.

---

## Task 1: Python — embed_server FastAPI app

**Files:**
- Create: `python/mlx_stack/embed_server/__init__.py` (empty)
- Create: `python/mlx_stack/embed_server/app.py`
- Create: `python/tests/test_embed_server.py`

The app exposes `POST /v1/embeddings` and `GET /v1/models`. Body shape (OpenAI): `{"input": "...", "model": "..."}`. Returns `{"object":"list","data":[{"object":"embedding","embedding":[...],"index":0}],"model":"...","usage":{...}}`.

For Phase 3 we use a real mlx_embeddings model loaded once at startup. Tests stub the model (no real load).

- [ ] **Step 1: Failing test (`python/tests/test_embed_server.py`)**

```python
import sys
import types

import pytest


@pytest.fixture
def fake_mlx_embeddings(monkeypatch):
    fake = types.ModuleType("mlx_embeddings.utils")

    class FakeModel:
        def __call__(self, input_ids):
            class O:
                text_embeds = [[0.1] * 4]
            return O()

    def load(_path):
        return FakeModel(), FakeTokenizer()

    class FakeTokenizer:
        def encode(self, s, **kw):
            class B:
                input_ids = type("I", (), {"reshape": lambda self, *a: self})()
                attention_mask = None
            return B()

        def __call__(self, s, **kw):
            return {"input_ids": [[1, 2, 3]], "attention_mask": [[1, 1, 1]]}

    fake.load = load
    monkeypatch.setitem(sys.modules, "mlx_embeddings", types.ModuleType("mlx_embeddings"))
    monkeypatch.setitem(sys.modules, "mlx_embeddings.utils", fake)
    return fake


def test_build_app_exposes_routes(fake_mlx_embeddings):
    from mlx_stack.embed_server import app as embed_app

    application = embed_app.build_app(model_path="/tmp/fake")
    routes = {r.path for r in application.routes}
    assert "/v1/embeddings" in routes
    assert "/v1/models" in routes
```

- [ ] **Step 2: Implement `python/mlx_stack/embed_server/app.py`**

```python
"""FastAPI app serving /v1/embeddings using mlx_embeddings.

Started by launcher_shim --engine embed.
"""
from __future__ import annotations

from typing import Any

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel


class EmbeddingsRequest(BaseModel):
    input: Any
    model: str | None = None
    encoding_format: str | None = None
    dimensions: int | None = None


class EmbeddingsItem(BaseModel):
    object: str = "embedding"
    embedding: list[float]
    index: int


class EmbeddingsResponse(BaseModel):
    object: str = "list"
    data: list[EmbeddingsItem]
    model: str
    usage: dict[str, int]


def build_app(model_path: str) -> FastAPI:
    from mlx_embeddings.utils import load
    model, tokenizer = load(model_path)

    app = FastAPI()

    @app.get("/v1/models")
    def list_models():
        return {"object": "list", "data": [{"id": model_path, "object": "model"}]}

    @app.post("/v1/embeddings")
    def embeddings(req: EmbeddingsRequest):
        texts = req.input if isinstance(req.input, list) else [req.input]
        out_items = []
        total_tokens = 0
        for i, text in enumerate(texts):
            tokens = tokenizer(text)
            ids = tokens["input_ids"]
            n = sum(len(row) for row in ids) if isinstance(ids, list) else len(ids)
            total_tokens += n
            result = model(ids)
            vec = result.text_embeds[0] if hasattr(result, "text_embeds") else list(result[0])
            if isinstance(vec, list):
                emb = [float(x) for x in vec]
            else:
                emb = [float(x) for x in list(vec)]
            if req.dimensions:
                emb = emb[: req.dimensions]
            out_items.append(EmbeddingsItem(embedding=emb, index=i))
        return EmbeddingsResponse(data=out_items, model=model_path, usage={"prompt_tokens": total_tokens, "total_tokens": total_tokens})

    return app


def main(host: str, port: int, model_path: str) -> None:
    import uvicorn
    uvicorn.run(build_app(model_path), host=host, port=port, log_level="info")
```

- [ ] **Step 3: Run test, commit.**

```
~/venvs/mlx/bin/pip install -e ./python  # picks up new package dir
~/venvs/mlx/bin/python -m pytest python/tests/test_embed_server.py -v
git add python/mlx_stack/embed_server/ python/tests/test_embed_server.py
git commit -m "feat: embed_server FastAPI app"
```

If `fastapi`/`uvicorn`/`pydantic` aren't installed, add `pip install fastapi uvicorn pydantic` to the README install notes — but they ARE already in the existing venv (mlx-embed-server.py uses them). Verify with `~/venvs/mlx/bin/python -c "import fastapi, uvicorn, pydantic"`.

---

## Task 2: launcher_shim — `--engine embed`

**Files:**
- Modify: `python/mlx_stack/launcher_shim.py`
- Modify: `python/tests/test_launcher_shim.py`

- [ ] **Step 1: Failing test**

Append to test_launcher_shim.py:

```python
def test_parse_args_accepts_embed_engine():
    from mlx_stack import launcher_shim
    args = launcher_shim.parse_args(["--engine", "embed", "--model", "/m", "--port", "1236"])
    assert args.engine == "embed"
```

Run and watch fail (current code only accepts `lm`/`vlm`).

- [ ] **Step 2: Implement**

In `launcher_shim.py`:

- Add `"embed"` to the `choices` list in `parse_args`.
- In `main()`, after parsing, dispatch when `args.engine == "embed"`:

```python
if args.engine == "embed":
    from mlx_stack.embed_server import app as embed_app
    embed_app.main(host=args.host, port=args.port, model_path=args.model)
    return 0
```

Place this branch BEFORE the patches/mlx import block (no monkey-patching needed for embed). Skip memory-thread setup for embed (no KV cache in embeddings).

- [ ] **Step 3: Run tests + commit.**

```
~/venvs/mlx/bin/python -m pytest python/tests/test_launcher_shim.py -v
git add python/mlx_stack/launcher_shim.py python/tests/test_launcher_shim.py
git commit -m "feat: launcher_shim --engine embed dispatch"
```

---

## Task 3: Config — [embed] section

**Files:**
- Modify: `internal/config/schema.go`
- Modify: `internal/config/schema_test.go`
- Modify: `internal/config/loader.go` (~-expansion)

`Embed` struct supports both managed and external:

```go
type Embed struct {
	Managed bool   `toml:"managed"`
	Host    string `toml:"host"`
	Port    int    `toml:"port"`
	Model   string `toml:"model"`
	Alias   string `toml:"alias"`
	URL     string `toml:"url"` // when Managed=false
}
```

Validation:
- If `Alias == ""` and `Model == ""` and `URL == ""` → not configured, skip.
- If `Managed`: `Port > 0`, `Model != ""`, `Alias != ""`.
- If `!Managed`: `URL != ""`, `Alias != ""`.

Add tests for valid managed, valid external, invalid both, and disabled-by-default. Implement, then commit:

```
go test ./internal/config/...
git add internal/config/
git commit -m "feat(config): embed section (managed | external)"
```

---

## Task 4: backend.External implements router.ManagedBackend

**Files:**
- Modify: `internal/backend/backend.go`

Add `UpstreamModel() string` method (no-arg, returns the configured upstream name) and `Running() bool` (always `true` — external is assumed up; future health probe lands separately).

```go
// MakeManagedBackend returns a ManagedBackend wrapping External, suitable
// for the router Registry.
func (e *External) UpstreamModelName() string { return e.UpstreamName }
```

Actually, External already has `UpstreamModel(ctx) (string, error)` — we add a wrapper that drops ctx so it matches `router.ManagedBackend`:

We have two options to bridge the signature mismatch (External's `UpstreamModel(ctx)` returns string + error; ManagedBackend wants `UpstreamModel() string`). Cleanest: add a tiny adapter in a new file `internal/backend/external_router.go`:

```go
package backend

func (e *External) AliasName2() string { return e.AliasName }
```

That's awkward. **Simpler approach:** change `router.ManagedBackend.UpstreamModel()` from no-arg to take ctx — but that's invasive. Or: rename External's method and update the few callers. Or: introduce a wrapper struct.

**Recommended:** introduce wrapper `ExternalAdapter` in `internal/supervisor/external.go`:

```go
package supervisor

import "github.com/guygrigsby/mlx-stack/internal/backend"

type ExternalAdapter struct{ *backend.External }

func (e *ExternalAdapter) BaseURL() string       { return e.External.URL() }
func (e *ExternalAdapter) UpstreamModel() string { return e.External.UpstreamName }
func (e *ExternalAdapter) Running() bool         { return true }
// Alias() comes from embedded *backend.External (returns AliasName)
```

Commit:

```
git add internal/supervisor/external.go
git commit -m "feat(supervisor): External adapter for router.ManagedBackend"
```

---

## Task 5: cmd/mlxd wires embed backend

**Files:**
- Modify: `cmd/mlxd/main.go`

After tags wiring, add:

```go
var embedBackend router.ManagedBackend
if cfg.Embed.Alias != "" {
	if cfg.Embed.Managed {
		em := supervisor.NewManaged(supervisor.ManagedOpts{
			Name: "embed", Host: cfg.Embed.Host, Port: cfg.Embed.Port,
			Alias: cfg.Embed.Alias, UpstreamModel: cfg.Embed.Model,
			Args: []string{
				"-m", "mlx_stack.launcher_shim",
				"--engine", "embed",
				"--model", cfg.Embed.Model,
				"--host", cfg.Embed.Host,
				"--port", fmt.Sprintf("%d", cfg.Embed.Port),
			},
			WorkerFactory: func(args []string) *supervisor.Worker {
				return supervisor.New(supervisor.WorkerSpec{
					Name: "embed", Command: cfg.PythonBin, Args: args, Logger: logger,
				})
			},
		})
		if err := em.Start(context.Background()); err != nil {
			logger.Error("embed start", "err", err)
		}
		embedBackend = em
	} else {
		embedBackend = &supervisor.ExternalAdapter{External: &backend.External{
			AliasName: cfg.Embed.Alias, BaseURL: cfg.Embed.URL, UpstreamName: cfg.Embed.Alias,
		}}
	}
}
```

Update registry construction to accept multiple managed backends:

In `internal/router/registry.go`, change `NewRegistry` signature to `NewRegistry(cfg, chat, managed ...ManagedBackend)`.

Wire: `router.NewRegistry(cfg, chatSwap, tagsMgr, embedBackend)` (filter nils with a helper or guard at call site).

Also: in `Catalog.List()`, include `cfg.Embed.Alias` if set.

Add `/v1/embeddings` route to router that dispatches via Registry (same as chat completions but doesn't read the model field — uses `cfg.Embed.Alias` as the resolution key directly when calling EnsureProfile-or-Proxy).

Actually, since the OpenAI embeddings request includes a `"model"` field, treat it the same as chat: extract model, resolve via registry, proxy. Add to `Server.Handler()`:

```go
mux.HandleFunc("POST /v1/embeddings", s.handleProxyByModel)
```

Refactor `handleChat` into a generic `handleProxyByModel` that all three routes call. Update tests.

Commit:

```
go test ./...
git add internal/router/ internal/supervisor/ cmd/mlxd/main.go
git commit -m "feat: wire embed backend (managed + external) through router"
```

---

## Task 6: e2e — embed managed + external

**Files:**
- Modify: `e2e/e2e_test.go`

Two new tests:
- `TestE2E_EmbedManaged` — config has `[embed] managed=true`. fake-python shim handles `--engine embed` by forwarding to a tiny fake that serves `POST /v1/embeddings` returning a canned vector.
- `TestE2E_EmbedExternal` — start a captive httptest server, point config at it with `managed=false url=...`, hit `/v1/embeddings`, verify proxy works.

This requires `fakemlx` to also accept `--engine embed` OR a second fake. Simplest: extend `testdata/fakemlx/main.go` to serve `POST /v1/embeddings` returning `{"object":"list","data":[{"embedding":[0.1,0.2]}]}`.

Implement, run, commit:

```
go test ./e2e/...
git add testdata/fakemlx/main.go e2e/e2e_test.go
git commit -m "test(e2e): embed managed + external"
```

---

## Acceptance

- `make test` green.
- `go test ./e2e/...` covers embed managed + external.
- `curl http://127.0.0.1:1231/v1/embeddings -d '{"model":"embed","input":"hi"}'` works against real mlx_embeddings (manual smoke).
