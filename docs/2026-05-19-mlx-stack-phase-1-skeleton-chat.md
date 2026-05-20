# mlx-stack Phase 1 Implementation Plan — Skeleton + Chat Backend

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the foundational mlxd daemon + mlx CLI with a working chat backend. End of Phase 1: `mlxd` runs on port 1231, spawns the Python launcher shim → `mlx_lm.server`, hot-swaps profiles in-process on a `model`-field request, and serves `/v1/chat/completions` + `/v1/completions` + `/v1/models`. Old zsh stack on port 1230 stays untouched and untested-against.

**Architecture:** One Go binary (`mlxd`) holds an HTTP router on `:1231`, a unix-socket admin API at `~/.local/state/mlxd/admin.sock`, and a supervisor that spawns Python worker processes via `~/venvs/mlx/bin/python -m mlx_stack.launcher_shim --engine lm …`. A second Go binary (`mlx`) is a thin admin-socket client. The Python shim owns the `mlx_lm.server` monkey-patches (xtc flatten, request timing), the cache janitor, memlog, and KV-headroom execv watchdog.

**Tech Stack:** Go 1.26 stdlib (`net/http`, `os/exec`, `os/signal`, `context`, `net`) + `github.com/BurntSushi/toml`. Python 3 inside `~/venvs/mlx` with `mlx`, `mlx_lm`. Testing: `go test` stdlib + `pytest` for Python. No third-party Go logging/CLI frameworks — stdlib `log/slog` and `flag`.

**Out of scope for Phase 1 (planned separately):** tags, embed, audio, kokoro backends; `mlx config migrate`; `mlx monitor` TTY render; `mlx tail` SSE; launchd plist; cutover to ports 1230/8080; structured log file rotation.

---

## File structure (Phase 1 only)

```
mlx-stack/
├── go.mod
├── go.sum
├── Makefile
├── README.md
├── cmd/
│   ├── mlxd/main.go
│   └── mlx/main.go
├── internal/
│   ├── config/
│   │   ├── schema.go
│   │   ├── schema_test.go
│   │   ├── loader.go
│   │   └── loader_test.go
│   ├── backend/
│   │   └── backend.go
│   ├── logobs/
│   │   ├── parser.go
│   │   └── parser_test.go
│   ├── supervisor/
│   │   ├── worker.go
│   │   ├── worker_test.go
│   │   ├── chatswap.go
│   │   └── chatswap_test.go
│   ├── router/
│   │   ├── proxy.go
│   │   ├── proxy_test.go
│   │   ├── rewrite.go
│   │   ├── rewrite_test.go
│   │   ├── catalog.go
│   │   ├── catalog_test.go
│   │   ├── server.go
│   │   └── server_test.go
│   ├── admin/
│   │   ├── server.go
│   │   ├── handlers.go
│   │   └── handlers_test.go
│   └── ipc/
│       ├── client.go
│       └── client_test.go
├── python/
│   ├── pyproject.toml
│   ├── mlx_stack/
│   │   ├── __init__.py
│   │   ├── launcher_shim.py
│   │   ├── patches/
│   │   │   ├── __init__.py
│   │   │   ├── xtc.py
│   │   │   └── timing.py
│   │   └── memory/
│   │       ├── __init__.py
│   │       ├── janitor.py
│   │       ├── memlog.py
│   │       └── watchdog.py
│   └── tests/
│       ├── test_xtc.py
│       ├── test_timing.py
│       ├── test_janitor.py
│       ├── test_memlog.py
│       ├── test_watchdog.py
│       └── test_launcher_shim.py
└── testdata/
    └── fakemlx/
        └── main.go
```

**File responsibilities (one-liners):**

- `cmd/mlxd/main.go` — daemon entry: load config, wire supervisor/router/admin, run until SIGTERM.
- `cmd/mlx/main.go` — CLI: parse subcommand, call admin socket, render result.
- `internal/config` — TOML schema, parse, validate, expand `~` paths.
- `internal/backend` — `Backend` interface + `External`/`Managed`/`ChatSwap` data shapes (no behavior).
- `internal/logobs` — parse `[mlx-launch] …` stderr lines into structured events.
- `internal/supervisor/worker.go` — generic `Worker`: spawn, pipe stderr, wait, signal.
- `internal/supervisor/chatswap.go` — `ChatSwap.EnsureProfile()` with lock + probe-until-ready loop.
- `internal/router/proxy.go` — streaming reverse-proxy that doesn't buffer SSE.
- `internal/router/rewrite.go` — read JSON body, swap `model` field for upstream name.
- `internal/router/catalog.go` — `/v1/models` from config + supervisor state.
- `internal/router/server.go` — `http.Server`, route table, handler glue.
- `internal/admin/server.go` — unix-socket `http.Server`.
- `internal/admin/handlers.go` — `/v1/status`, `/v1/swap`, `/v1/health`, `/v1/start`, `/v1/stop`.
- `internal/ipc/client.go` — CLI-side admin client (unix-socket `http.Client`).
- `python/mlx_stack/launcher_shim.py` — `python -m mlx_stack.launcher_shim --engine lm …` dispatch.
- `python/mlx_stack/patches/xtc.py` — flatten `xtc_special_tokens` list-of-lists → list-of-ints.
- `python/mlx_stack/patches/timing.py` — wrap `mlx_lm.server.ResponseGenerator.generate` to emit per-request timing.
- `python/mlx_stack/memory/janitor.py` — periodic `mx.clear_cache()` background thread.
- `python/mlx_stack/memory/memlog.py` — periodic `mem: active=… cache=… peak=…` stderr lines.
- `python/mlx_stack/memory/watchdog.py` — KV-headroom check; `os.execv` self when active mem > baseline + headroom.
- `testdata/fakemlx/main.go` — fake `mlx_lm.server`: binds a port, serves `/v1/models` + `/v1/chat/completions` (canned SSE), prints `[mlx-launch] …` stderr, honors SIGTERM.

---

## Conventions

- **Commits** at end of every task. Commit message style: Conventional Commits (`feat:`, `test:`, `chore:`, `fix:`). Body optional in Phase 1.
- **Go module path:** `github.com/guygrigsby/mlx-stack`.
- **Go version:** 1.26 (set in `go.mod`).
- **No vendoring.** `go.sum` committed.
- **No global state** in Go packages except `cmd/mlxd/main.go` wiring.
- **Run all Go tests:** `go test ./...` from repo root.
- **Run all Python tests:** `~/venvs/mlx/bin/python -m pytest python/tests -v` from repo root.
- **Python imports** never trigger at module top level except in `launcher_shim.py` after `MLX_DISABLE_COMPILE=1` is set.

---

## Task 1: Initialize Go module + repo skeleton + Makefile

**Files:**
- Create: `go.mod`
- Create: `Makefile`
- Create: `README.md`
- Create: `.gitignore`

- [ ] **Step 1: Initialize git + Go module**

Run from `/Users/guygrigsby/projects/mlx-stack`:

```bash
git init
go mod init github.com/guygrigsby/mlx-stack
```

Expected: `go.mod` exists with `module github.com/guygrigsby/mlx-stack` and `go 1.26`.

- [ ] **Step 2: Add `.gitignore`**

Create `.gitignore`:

```
/bin/
*.test
*.out
.DS_Store
__pycache__/
*.pyc
.pytest_cache/
*.egg-info/
.coverage
admin.sock
```

- [ ] **Step 3: Create `Makefile`**

Create `Makefile`:

```makefile
.PHONY: build test test-go test-py install clean fakemlx

GOFLAGS    ?=
PYTHON_BIN ?= $(HOME)/venvs/mlx/bin/python
INSTALL_DIR ?= $(HOME)/.local/bin

build:
	mkdir -p bin
	go build $(GOFLAGS) -o bin/mlxd ./cmd/mlxd
	go build $(GOFLAGS) -o bin/mlx  ./cmd/mlx

fakemlx:
	mkdir -p bin
	go build $(GOFLAGS) -o bin/fakemlx ./testdata/fakemlx

test: test-go test-py

test-go:
	go test ./...

test-py:
	$(PYTHON_BIN) -m pytest python/tests -v

install: build
	mkdir -p $(INSTALL_DIR)
	cp bin/mlxd bin/mlx $(INSTALL_DIR)/
	$(PYTHON_BIN) -m pip install -e ./python

clean:
	rm -rf bin
```

- [ ] **Step 4: Create stub `README.md`**

Create `README.md`:

```markdown
# mlx-stack

Single-daemon replacement for the multi-script MLX inference setup. See `docs/2026-05-19-mlx-stack-design.md` for the design.

## Build

    make build

## Test

    make test

## Install

    make install
```

- [ ] **Step 5: Verify module is sane and commit**

Run:

```bash
go build ./...
```

Expected: succeeds silently (no source yet — `go build ./...` with no packages is fine; if it errors with "no Go files in …", that's also fine — we've only created `go.mod`).

```bash
git add go.mod Makefile README.md .gitignore
git commit -m "chore: initialize Go module + Makefile"
```

---

## Task 2: Initialize Python package

**Files:**
- Create: `python/pyproject.toml`
- Create: `python/mlx_stack/__init__.py`
- Create: `python/mlx_stack/patches/__init__.py`
- Create: `python/mlx_stack/memory/__init__.py`
- Create: `python/tests/__init__.py`

- [ ] **Step 1: Create `python/pyproject.toml`**

```toml
[build-system]
requires      = ["setuptools>=68", "wheel"]
build-backend = "setuptools.build_meta"

[project]
name            = "mlx-stack"
version         = "0.1.0"
description     = "Worker shim + patches for mlx-stack"
requires-python = ">=3.10"
dependencies    = []   # mlx, mlx_lm, mlx_vlm assumed already installed in target venv

[tool.setuptools.packages.find]
where   = ["."]
include = ["mlx_stack*"]

[tool.pytest.ini_options]
testpaths = ["tests"]
```

- [ ] **Step 2: Create empty `__init__.py` files**

Create each of:
- `python/mlx_stack/__init__.py` — empty
- `python/mlx_stack/patches/__init__.py` — empty
- `python/mlx_stack/memory/__init__.py` — empty
- `python/tests/__init__.py` — empty

- [ ] **Step 3: Install the package into the existing venv**

```bash
~/venvs/mlx/bin/pip install -e ./python
```

Expected: `Successfully installed mlx-stack-0.1.0`.

- [ ] **Step 4: Verify import works**

```bash
~/venvs/mlx/bin/python -c "import mlx_stack; print(mlx_stack.__file__)"
```

Expected: prints a path under `python/mlx_stack/__init__.py`.

- [ ] **Step 5: Commit**

```bash
git add python/
git commit -m "chore: scaffold python mlx_stack package"
```

---

## Task 3: `patches/xtc.py` — flatten `xtc_special_tokens`

**Why:** Today's `mlx-server-launch.py:_patch_xtc_special_tokens` wraps `mlx_lm.server.make_sampler` so that `xtc_special_tokens` arriving as `[[198]]` (list of single-token lists from `tokenizer.encode("\n")`) is flattened to `[198]` before reaching `apply_xtc`. Without this, Qwen2.5 crashes.

**Files:**
- Create: `python/mlx_stack/patches/xtc.py`
- Test: `python/tests/test_xtc.py`

- [ ] **Step 1: Write the failing test**

Create `python/tests/test_xtc.py`:

```python
import sys
import types

import pytest


@pytest.fixture
def fake_mlx_lm_server(monkeypatch):
    """Inject a fake mlx_lm.server with a make_sampler we can wrap."""
    fake = types.ModuleType("mlx_lm.server")
    captured = {}

    def make_sampler(*args, **kwargs):
        captured["args"] = args
        captured["kwargs"] = kwargs
        return "sampler-obj"

    fake.make_sampler = make_sampler
    monkeypatch.setitem(sys.modules, "mlx_lm", types.ModuleType("mlx_lm"))
    monkeypatch.setitem(sys.modules, "mlx_lm.server", fake)
    return fake, captured


def test_apply_flattens_list_of_lists(fake_mlx_lm_server):
    from mlx_stack.patches import xtc

    fake, captured = fake_mlx_lm_server
    xtc.apply()

    fake.make_sampler(xtc_special_tokens=[[198], [199, 200]])

    assert captured["kwargs"]["xtc_special_tokens"] == [198, 199, 200]


def test_apply_preserves_flat_list(fake_mlx_lm_server):
    from mlx_stack.patches import xtc

    fake, captured = fake_mlx_lm_server
    xtc.apply()

    fake.make_sampler(xtc_special_tokens=[1, 2, 3])

    assert captured["kwargs"]["xtc_special_tokens"] == [1, 2, 3]


def test_apply_passes_through_when_absent(fake_mlx_lm_server):
    from mlx_stack.patches import xtc

    fake, captured = fake_mlx_lm_server
    xtc.apply()

    fake.make_sampler(temp=0.7)

    assert "xtc_special_tokens" not in captured["kwargs"]
    assert captured["kwargs"]["temp"] == 0.7


def test_apply_is_idempotent(fake_mlx_lm_server):
    from mlx_stack.patches import xtc

    fake, captured = fake_mlx_lm_server
    xtc.apply()
    xtc.apply()  # second call must not double-wrap

    fake.make_sampler(xtc_special_tokens=[[1], [2]])

    assert captured["kwargs"]["xtc_special_tokens"] == [1, 2]
```

- [ ] **Step 2: Run test, verify it fails**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_xtc.py -v
```

Expected: FAIL with `ModuleNotFoundError: No module named 'mlx_stack.patches.xtc'`.

- [ ] **Step 3: Implement `patches/xtc.py`**

Create `python/mlx_stack/patches/xtc.py`:

```python
"""Flatten xtc_special_tokens before mlx_lm.make_sampler sees it.

Upstream make_sampler forwards xtc_special_tokens directly to apply_xtc,
which expects a flat list[int]. The router sometimes passes [[198]] (output
of tokenizer.encode("\n")) which crashes Qwen2.5. We wrap make_sampler to
flatten any nested lists.
"""
from __future__ import annotations

_APPLIED = False


def _flatten(value):
    if value is None:
        return value
    out = []
    for item in value:
        if isinstance(item, (list, tuple)):
            out.extend(int(x) for x in item)
        else:
            out.append(int(item))
    return out


def apply() -> None:
    global _APPLIED
    if _APPLIED:
        return

    from mlx_lm import server as _server

    original = _server.make_sampler

    def wrapped(*args, **kwargs):
        if "xtc_special_tokens" in kwargs:
            kwargs["xtc_special_tokens"] = _flatten(kwargs["xtc_special_tokens"])
        return original(*args, **kwargs)

    _server.make_sampler = wrapped
    _APPLIED = True
```

- [ ] **Step 4: Run tests, verify pass**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_xtc.py -v
```

Expected: 4 passed.

- [ ] **Step 5: Commit**

```bash
git add python/mlx_stack/patches/xtc.py python/tests/test_xtc.py
git commit -m "feat: xtc_special_tokens flatten patch"
```

---

## Task 4: `patches/timing.py` — wrap ResponseGenerator.generate

**Why:** Today's `_patch_request_timing` wraps `mlx_lm.server.ResponseGenerator.generate` to emit a single line per request: `[mlx-launch] req=<id> prompt=<tokens>t prefill=<ms>ms@<tps>tps decode=<ms>ms@<tps>tps`. We preserve verbatim.

**Files:**
- Create: `python/mlx_stack/patches/timing.py`
- Test: `python/tests/test_timing.py`

- [ ] **Step 1: Write the failing test**

Create `python/tests/test_timing.py`:

```python
import io
import sys
import time
import types

import pytest


@pytest.fixture
def fake_response_generator(monkeypatch):
    fake_module = types.ModuleType("mlx_lm.server")

    class ResponseGenerator:
        def __init__(self):
            self.calls = 0

        def generate(self, prompt_tokens, completion_tokens, request_id="r"):
            self.calls += 1
            # Simulate prefill and decode time
            time.sleep(0.002)
            for i in range(completion_tokens):
                yield {"token": i}
                time.sleep(0.0005)

    fake_module.ResponseGenerator = ResponseGenerator
    monkeypatch.setitem(sys.modules, "mlx_lm", types.ModuleType("mlx_lm"))
    monkeypatch.setitem(sys.modules, "mlx_lm.server", fake_module)
    return fake_module


def test_timing_emits_one_line_per_request(fake_response_generator, capsys):
    from mlx_stack.patches import timing

    timing.apply()

    rg = fake_response_generator.ResponseGenerator()
    list(rg.generate(prompt_tokens=10, completion_tokens=5, request_id="req-1"))

    captured = capsys.readouterr()
    assert "[mlx-launch] req=req-1" in captured.err
    assert "prompt=10t" in captured.err
    assert "prefill=" in captured.err
    assert "decode=" in captured.err


def test_timing_is_idempotent(fake_response_generator, capsys):
    from mlx_stack.patches import timing

    timing.apply()
    timing.apply()  # second apply must not stack wrappers

    rg = fake_response_generator.ResponseGenerator()
    list(rg.generate(prompt_tokens=1, completion_tokens=1, request_id="r"))

    captured = capsys.readouterr()
    # exactly one timing line
    assert captured.err.count("[mlx-launch] req=") == 1


def test_timing_handles_zero_completion(fake_response_generator, capsys):
    from mlx_stack.patches import timing

    timing.apply()

    rg = fake_response_generator.ResponseGenerator()
    list(rg.generate(prompt_tokens=5, completion_tokens=0, request_id="zero"))

    captured = capsys.readouterr()
    assert "decode=0" in captured.err or "decode=0.0" in captured.err
```

- [ ] **Step 2: Run test, verify it fails**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_timing.py -v
```

Expected: FAIL with `ModuleNotFoundError`.

- [ ] **Step 3: Implement `patches/timing.py`**

Create `python/mlx_stack/patches/timing.py`:

```python
"""Per-request timing wrap for mlx_lm.server.ResponseGenerator.generate.

Emits one line to stderr per request:
    [mlx-launch] req=<id> prompt=<N>t prefill=<ms>ms@<tps>tps decode=<ms>ms@<tps>tps
"""
from __future__ import annotations

import sys
import time
import functools

_APPLIED = False


def _fmt(ms: float, tokens: int) -> str:
    tps = (tokens / (ms / 1000.0)) if ms > 0 else 0.0
    return f"{ms:.1f}ms@{tps:.1f}tps"


def apply() -> None:
    global _APPLIED
    if _APPLIED:
        return

    from mlx_lm import server as _server

    original = _server.ResponseGenerator.generate

    @functools.wraps(original)
    def wrapped(self, prompt_tokens, completion_tokens, request_id="", *args, **kwargs):
        t0 = time.perf_counter()
        first_token_at = None
        produced = 0

        for token in original(self, prompt_tokens, completion_tokens, request_id=request_id, *args, **kwargs):
            if first_token_at is None:
                first_token_at = time.perf_counter()
            produced += 1
            yield token

        t_end = time.perf_counter()
        prefill_ms = ((first_token_at or t_end) - t0) * 1000.0
        decode_ms  = (t_end - (first_token_at or t_end)) * 1000.0

        print(
            f"[mlx-launch] req={request_id} prompt={prompt_tokens}t "
            f"prefill={_fmt(prefill_ms, prompt_tokens)} "
            f"decode={_fmt(decode_ms, produced)}",
            file=sys.stderr,
            flush=True,
        )

    _server.ResponseGenerator.generate = wrapped
    _APPLIED = True
```

- [ ] **Step 4: Run tests, verify pass**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_timing.py -v
```

Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add python/mlx_stack/patches/timing.py python/tests/test_timing.py
git commit -m "feat: per-request timing patch"
```

---

## Task 5: `memory/janitor.py` — periodic mx.clear_cache

**Why:** Today's launcher sets `MLX_CACHE_LIMIT_BYTES` and runs a background loop that calls `mx.clear_cache()` every N seconds when cache memory exceeds a threshold.

**Files:**
- Create: `python/mlx_stack/memory/janitor.py`
- Test: `python/tests/test_janitor.py`

- [ ] **Step 1: Write the failing test**

Create `python/tests/test_janitor.py`:

```python
import threading
import time
import types

import pytest


def test_janitor_clears_when_threshold_exceeded(monkeypatch):
    fake_mx = types.SimpleNamespace(
        get_cache_memory=lambda: 2_000_000_000,
        clear_cache=lambda: cleared.append(time.time()),
        set_cache_limit=lambda n: limits.append(n),
    )
    cleared, limits = [], []

    from mlx_stack.memory import janitor

    stop = janitor.start(
        mx=fake_mx,
        limit_bytes=2_147_483_648,
        clear_interval_sec=0.05,
        clear_threshold_bytes=1_073_741_824,
    )
    try:
        time.sleep(0.2)
    finally:
        stop()

    assert limits == [2_147_483_648]
    assert len(cleared) >= 2


def test_janitor_skips_clear_below_threshold(monkeypatch):
    fake_mx = types.SimpleNamespace(
        get_cache_memory=lambda: 100,
        clear_cache=lambda: cleared.append(1),
        set_cache_limit=lambda n: None,
    )
    cleared = []

    from mlx_stack.memory import janitor

    stop = janitor.start(
        mx=fake_mx,
        limit_bytes=2_147_483_648,
        clear_interval_sec=0.05,
        clear_threshold_bytes=1_000,
    )
    try:
        time.sleep(0.2)
    finally:
        stop()

    assert cleared == []


def test_janitor_stop_is_clean(monkeypatch):
    fake_mx = types.SimpleNamespace(
        get_cache_memory=lambda: 0,
        clear_cache=lambda: None,
        set_cache_limit=lambda n: None,
    )

    from mlx_stack.memory import janitor

    stop = janitor.start(
        mx=fake_mx,
        limit_bytes=1024,
        clear_interval_sec=0.05,
        clear_threshold_bytes=1024,
    )
    t0 = time.time()
    stop()
    assert time.time() - t0 < 0.5
```

- [ ] **Step 2: Run test, verify it fails**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_janitor.py -v
```

Expected: FAIL with `ModuleNotFoundError`.

- [ ] **Step 3: Implement `memory/janitor.py`**

Create `python/mlx_stack/memory/janitor.py`:

```python
"""Periodic mx.clear_cache when cache memory exceeds a threshold."""
from __future__ import annotations

import threading
from typing import Callable


def start(*, mx, limit_bytes: int, clear_interval_sec: float, clear_threshold_bytes: int) -> Callable[[], None]:
    """Configure cache limit and start the janitor thread. Returns a stop function."""
    if limit_bytes:
        mx.set_cache_limit(limit_bytes)

    stop_event = threading.Event()

    def loop():
        while not stop_event.wait(clear_interval_sec):
            try:
                if mx.get_cache_memory() > clear_threshold_bytes:
                    mx.clear_cache()
            except Exception:
                pass

    thread = threading.Thread(target=loop, daemon=True, name="mlx-cache-janitor")
    thread.start()

    def stop():
        stop_event.set()
        thread.join(timeout=1.0)

    return stop
```

- [ ] **Step 4: Run tests, verify pass**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_janitor.py -v
```

Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add python/mlx_stack/memory/janitor.py python/tests/test_janitor.py
git commit -m "feat: cache janitor thread"
```

---

## Task 6: `memory/memlog.py` — periodic mem snapshot lines

**Why:** Today launcher emits `[mlx-launch] mem: active=X cache=Y peak=Z` every N seconds for mlxd to parse and surface in `mlx status` / `mlx monitor`.

**Files:**
- Create: `python/mlx_stack/memory/memlog.py`
- Test: `python/tests/test_memlog.py`

- [ ] **Step 1: Write the failing test**

Create `python/tests/test_memlog.py`:

```python
import io
import sys
import time
import types


def test_memlog_emits_snapshot(capsys):
    fake_mx = types.SimpleNamespace(
        get_active_memory=lambda: 1234,
        get_cache_memory=lambda: 5678,
        get_peak_memory=lambda: 9999,
    )

    from mlx_stack.memory import memlog

    stop = memlog.start(mx=fake_mx, interval_sec=0.05)
    try:
        time.sleep(0.18)
    finally:
        stop()

    err = capsys.readouterr().err
    assert "[mlx-launch] mem: active=1234 cache=5678 peak=9999" in err
    assert err.count("[mlx-launch] mem: ") >= 2


def test_memlog_stop_is_clean():
    fake_mx = types.SimpleNamespace(
        get_active_memory=lambda: 0,
        get_cache_memory=lambda: 0,
        get_peak_memory=lambda: 0,
    )

    from mlx_stack.memory import memlog

    stop = memlog.start(mx=fake_mx, interval_sec=0.05)
    t0 = time.time()
    stop()
    assert time.time() - t0 < 0.5
```

- [ ] **Step 2: Run test, verify it fails**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_memlog.py -v
```

Expected: FAIL with `ModuleNotFoundError`.

- [ ] **Step 3: Implement `memory/memlog.py`**

Create `python/mlx_stack/memory/memlog.py`:

```python
"""Periodic memory snapshot lines on stderr."""
from __future__ import annotations

import sys
import threading
from typing import Callable


def start(*, mx, interval_sec: float) -> Callable[[], None]:
    stop_event = threading.Event()

    def loop():
        while not stop_event.wait(interval_sec):
            try:
                a = mx.get_active_memory()
                c = mx.get_cache_memory()
                p = mx.get_peak_memory()
                print(f"[mlx-launch] mem: active={a} cache={c} peak={p}", file=sys.stderr, flush=True)
            except Exception:
                pass

    thread = threading.Thread(target=loop, daemon=True, name="mlx-memlog")
    thread.start()

    def stop():
        stop_event.set()
        thread.join(timeout=1.0)

    return stop
```

- [ ] **Step 4: Run tests, verify pass**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_memlog.py -v
```

Expected: 2 passed.

- [ ] **Step 5: Commit**

```bash
git add python/mlx_stack/memory/memlog.py python/tests/test_memlog.py
git commit -m "feat: periodic mem snapshot logger"
```

---

## Task 7: `memory/watchdog.py` — KV-headroom execv-restart

**Why:** Today's launcher samples `mx.get_active_memory()` post-startup as baseline, then every interval compares current active to `baseline + KV_HEADROOM`. If exceeded, log it and `os.execv(sys.executable, [sys.executable, *sys.argv])` to fully reset state with the same PID. Tests must avoid actually calling execv.

**Files:**
- Create: `python/mlx_stack/memory/watchdog.py`
- Test: `python/tests/test_watchdog.py`

- [ ] **Step 1: Write the failing test**

Create `python/tests/test_watchdog.py`:

```python
import sys
import time
import types

import pytest


def test_watchdog_logs_baseline_after_grace(monkeypatch, capsys):
    fake_mx = types.SimpleNamespace(get_active_memory=lambda: 1_000_000_000)
    execv_calls = []
    monkeypatch.setattr("os.execv", lambda *a: execv_calls.append(a))

    from mlx_stack.memory import watchdog

    stop = watchdog.start(
        mx=fake_mx,
        kv_headroom_bytes=8_000_000_000,
        check_interval_sec=0.05,
        grace_sec=0.1,
    )
    try:
        time.sleep(0.25)
    finally:
        stop()

    err = capsys.readouterr().err
    assert "WATCHDOG: armed" in err
    assert "baseline=1000000000" in err
    assert execv_calls == []


def test_watchdog_triggers_execv_when_above_headroom(monkeypatch, capsys):
    samples = iter([1_000_000_000, 1_000_000_000, 9_500_000_000, 9_500_000_000])
    fake_mx = types.SimpleNamespace(get_active_memory=lambda: next(samples))

    execv_calls = []
    monkeypatch.setattr("os.execv", lambda exe, argv: execv_calls.append((exe, list(argv))))

    from mlx_stack.memory import watchdog

    stop = watchdog.start(
        mx=fake_mx,
        kv_headroom_bytes=8_000_000_000,
        check_interval_sec=0.05,
        grace_sec=0.05,
    )
    try:
        time.sleep(0.5)
    finally:
        stop()

    err = capsys.readouterr().err
    assert "WATCHDOG: active=" in err
    assert "execv-restarting" in err
    assert len(execv_calls) == 1
    assert execv_calls[0][0] == sys.executable


def test_watchdog_stop_is_clean(monkeypatch):
    fake_mx = types.SimpleNamespace(get_active_memory=lambda: 0)
    monkeypatch.setattr("os.execv", lambda *a: None)

    from mlx_stack.memory import watchdog

    stop = watchdog.start(
        mx=fake_mx,
        kv_headroom_bytes=1,
        check_interval_sec=0.05,
        grace_sec=10.0,  # never arms during this short test
    )
    t0 = time.time()
    stop()
    assert time.time() - t0 < 0.5
```

- [ ] **Step 2: Run test, verify it fails**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_watchdog.py -v
```

Expected: FAIL with `ModuleNotFoundError`.

- [ ] **Step 3: Implement `memory/watchdog.py`**

Create `python/mlx_stack/memory/watchdog.py`:

```python
"""KV-headroom watchdog: execv self when active memory grows beyond baseline + headroom."""
from __future__ import annotations

import os
import sys
import threading
import time
from typing import Callable


def start(*, mx, kv_headroom_bytes: int, check_interval_sec: float, grace_sec: float) -> Callable[[], None]:
    stop_event = threading.Event()

    def loop():
        t_start = time.monotonic()
        baseline = None
        while not stop_event.is_set():
            now = time.monotonic()
            if baseline is None:
                if now - t_start >= grace_sec:
                    baseline = mx.get_active_memory()
                    trigger = baseline + kv_headroom_bytes
                    print(
                        f"[mlx-launch] WATCHDOG: armed. baseline={baseline} trigger={trigger}",
                        file=sys.stderr,
                        flush=True,
                    )
            else:
                active = mx.get_active_memory()
                trigger = baseline + kv_headroom_bytes
                if active > trigger:
                    print(
                        f"[mlx-launch] WATCHDOG: active={active} > trigger={trigger} — execv-restarting",
                        file=sys.stderr,
                        flush=True,
                    )
                    os.execv(sys.executable, [sys.executable, *sys.argv])
                    return
            if stop_event.wait(check_interval_sec):
                return

    thread = threading.Thread(target=loop, daemon=True, name="mlx-watchdog")
    thread.start()

    def stop():
        stop_event.set()
        thread.join(timeout=1.0)

    return stop
```

- [ ] **Step 4: Run tests, verify pass**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_watchdog.py -v
```

Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add python/mlx_stack/memory/watchdog.py python/tests/test_watchdog.py
git commit -m "feat: KV-headroom execv watchdog"
```

---

## Task 8: `launcher_shim.py` — engine dispatch + integration

**Why:** Single entry point Go spawns. Sets `MLX_DISABLE_COMPILE=1` before importing `mlx.core`, applies patches conditionally, starts the memory threads, then hands off to `mlx_lm.server.main()` / `mlx_vlm.server.main()`. Phase 1 only implements `--engine lm` and `--engine vlm` dispatch (audio + embed land in later phases).

**Files:**
- Create: `python/mlx_stack/launcher_shim.py`
- Test: `python/tests/test_launcher_shim.py`

- [ ] **Step 1: Write the failing test**

Create `python/tests/test_launcher_shim.py`:

```python
import os
import sys
import types

import pytest


def test_parse_args_required_flags():
    from mlx_stack import launcher_shim

    args = launcher_shim.parse_args([
        "--engine", "lm",
        "--model", "/tmp/m",
        "--port", "1234",
    ])
    assert args.engine == "lm"
    assert args.model == "/tmp/m"
    assert args.port == 1234
    assert args.host == "127.0.0.1"


def test_parse_args_with_draft():
    from mlx_stack import launcher_shim

    args = launcher_shim.parse_args([
        "--engine", "lm",
        "--model", "/tmp/m",
        "--draft-model", "/tmp/d",
        "--port", "1234",
    ])
    assert args.draft_model == "/tmp/d"


def test_parse_args_rejects_unknown_engine():
    from mlx_stack import launcher_shim

    with pytest.raises(SystemExit):
        launcher_shim.parse_args([
            "--engine", "bogus",
            "--model", "/tmp/m",
            "--port", "1234",
        ])


def test_build_server_argv_lm_basic():
    from mlx_stack import launcher_shim

    argv = launcher_shim.build_server_argv(types.SimpleNamespace(
        engine="lm", model="/tmp/m", draft_model="", host="127.0.0.1", port=1234,
    ))
    assert "--model" in argv and "/tmp/m" in argv
    assert "--port" in argv and "1234" in argv
    assert "--host" in argv and "127.0.0.1" in argv
    assert "--draft-model" not in argv


def test_build_server_argv_lm_with_draft():
    from mlx_stack import launcher_shim

    argv = launcher_shim.build_server_argv(types.SimpleNamespace(
        engine="lm", model="/tmp/m", draft_model="/tmp/d", host="127.0.0.1", port=1234,
    ))
    i = argv.index("--draft-model")
    assert argv[i + 1] == "/tmp/d"


def test_module_sets_mlx_disable_compile_on_import(monkeypatch):
    monkeypatch.delenv("MLX_DISABLE_COMPILE", raising=False)
    # Force re-import
    sys.modules.pop("mlx_stack.launcher_shim", None)

    import mlx_stack.launcher_shim  # noqa: F401

    assert os.environ.get("MLX_DISABLE_COMPILE") == "1"
```

- [ ] **Step 2: Run test, verify it fails**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_launcher_shim.py -v
```

Expected: FAIL with `ModuleNotFoundError` for `mlx_stack.launcher_shim`.

- [ ] **Step 3: Implement `launcher_shim.py`**

Create `python/mlx_stack/launcher_shim.py`:

```python
"""Universal MLX worker entry point.

Sets MLX_DISABLE_COMPILE=1 before any mlx import, applies engine-specific
patches, starts memory threads, dispatches to the engine's server.main().

Usage:
    python -m mlx_stack.launcher_shim --engine lm --model PATH --port PORT [--draft-model PATH] [--host HOST]
"""
from __future__ import annotations

import argparse
import os
import sys

# CRITICAL: must run before any module imports mlx.core.
os.environ.setdefault("MLX_DISABLE_COMPILE", "1")


def parse_args(argv=None) -> argparse.Namespace:
    p = argparse.ArgumentParser(prog="mlx_stack.launcher_shim")
    p.add_argument("--engine", required=True, choices=["lm", "vlm"])
    p.add_argument("--model", required=True)
    p.add_argument("--draft-model", default="", dest="draft_model")
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", required=True, type=int)
    return p.parse_args(argv)


def build_server_argv(args) -> list[str]:
    argv = [
        "mlx_lm.server" if args.engine == "lm" else "mlx_vlm.server",
        "--model", args.model,
        "--host", args.host,
        "--port", str(args.port),
    ]
    if args.draft_model:
        argv += ["--draft-model", args.draft_model]
    return argv


def _env_int(name: str, default: int = 0) -> int:
    try:
        return int(os.environ.get(name, default))
    except ValueError:
        return default


def _env_float(name: str, default: float = 0.0) -> float:
    try:
        return float(os.environ.get(name, default))
    except ValueError:
        return default


def main(argv=None) -> int:
    args = parse_args(argv)

    # Apply patches before engine import.
    if args.engine == "lm":
        from mlx_stack.patches import xtc, timing
        xtc.apply()
        timing.apply()

    # Import mlx.core; safe now that MLX_DISABLE_COMPILE is set.
    import mlx.core as mx

    # Start memory threads (config from env).
    stops = []

    cache_limit = _env_int("MLX_CACHE_LIMIT_BYTES")
    if cache_limit:
        from mlx_stack.memory import janitor
        stops.append(janitor.start(
            mx=mx,
            limit_bytes=cache_limit,
            clear_interval_sec=_env_float("MLX_CACHE_CLEAR_INTERVAL_SEC", 60.0),
            clear_threshold_bytes=_env_int("MLX_CACHE_CLEAR_THRESHOLD_BYTES", cache_limit // 2),
        ))

    memlog_interval = _env_float("MLX_MEMLOG_INTERVAL_SEC", 0.0)
    if memlog_interval > 0:
        from mlx_stack.memory import memlog
        stops.append(memlog.start(mx=mx, interval_sec=memlog_interval))

    kv_headroom = _env_int("MLX_KV_HEADROOM_BYTES")
    if kv_headroom > 0:
        from mlx_stack.memory import watchdog
        stops.append(watchdog.start(
            mx=mx,
            kv_headroom_bytes=kv_headroom,
            check_interval_sec=_env_float("MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC", 30.0),
            grace_sec=_env_float("MLX_ACTIVE_MEMORY_GRACE_SEC", 90.0),
        ))

    print(f"[mlx-launch] starting engine={args.engine} model={args.model} port={args.port}", file=sys.stderr, flush=True)

    # Hand off to engine's server.main(). It calls sys.exit().
    if args.engine == "lm":
        from mlx_lm.server import main as server_main
    else:
        from mlx_vlm.server import main as server_main

    # Splice our argv in so the engine sees its own flags.
    sys.argv = build_server_argv(args)
    try:
        server_main()
    finally:
        for stop in stops:
            try:
                stop()
            except Exception:
                pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 4: Run tests, verify pass**

```bash
~/venvs/mlx/bin/python -m pytest python/tests/test_launcher_shim.py -v
```

Expected: 6 passed.

- [ ] **Step 5: Commit**

```bash
git add python/mlx_stack/launcher_shim.py python/tests/test_launcher_shim.py
git commit -m "feat: launcher_shim engine dispatch"
```

---

## Task 9: `config/schema.go` — TOML structs + validation (chat-only)

**Files:**
- Create: `internal/config/schema.go`
- Test: `internal/config/schema_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/config/schema_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

func TestValidate_OK(t *testing.T) {
	c := &Config{
		LogDir:     "/tmp/logs",
		ModelsRoot: "/tmp/models",
		PythonBin:  "/usr/bin/python",
		Router: Router{Host: "127.0.0.1", Port: 1231, ExtraPorts: []int{}},
		Chat: Chat{
			DefaultProfile: "p1",
			Host:           "127.0.0.1",
			Port:           1234,
			SwapTimeoutSec: 30,
			Profiles: map[string]Profile{
				"p1": {Model: "/tmp/models/p1", Engine: "lm"},
			},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_MissingPythonBin(t *testing.T) {
	c := &Config{Router: Router{Host: "127.0.0.1", Port: 1231}, Chat: minChat()}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "python_bin") {
		t.Fatalf("expected python_bin error, got: %v", err)
	}
}

func TestValidate_DefaultProfileMustExist(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat: Chat{
			DefaultProfile: "ghost",
			Host:           "127.0.0.1",
			Port:           1234,
			Profiles:       map[string]Profile{"p1": {Model: "/tmp", Engine: "lm"}},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "default_profile") {
		t.Fatalf("expected default_profile error, got: %v", err)
	}
}

func TestValidate_ProfileEngineMustBeLmOrVlm(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat: Chat{
			DefaultProfile: "p1",
			Host:           "127.0.0.1",
			Port:           1234,
			Profiles:       map[string]Profile{"p1": {Model: "/tmp", Engine: "audio"}},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "engine") {
		t.Fatalf("expected engine error, got: %v", err)
	}
}

func TestValidate_PortRange(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 0},
		Chat:      minChat(),
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "router.port") {
		t.Fatalf("expected router.port error, got: %v", err)
	}
}

func minChat() Chat {
	return Chat{
		DefaultProfile: "p1",
		Host:           "127.0.0.1",
		Port:           1234,
		Profiles:       map[string]Profile{"p1": {Model: "/tmp", Engine: "lm"}},
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/config/...
```

Expected: FAIL — no `config` package yet.

- [ ] **Step 3: Implement `config/schema.go`**

Create `internal/config/schema.go`:

```go
package config

import "fmt"

type Config struct {
	LogDir     string `toml:"log_dir"`
	ModelsRoot string `toml:"models_root"`
	PythonBin  string `toml:"python_bin"`
	Router     Router `toml:"router"`
	Chat       Chat   `toml:"chat"`
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
	return nil
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/config/...
```

Expected: 5 passed.

- [ ] **Step 5: Commit**

```bash
git add internal/config/schema.go internal/config/schema_test.go
git commit -m "feat(config): chat-scope schema + validation"
```

---

## Task 10: `config/loader.go` — TOML load + `~` expansion

**Files:**
- Create: `internal/config/loader.go`
- Test: `internal/config/loader_test.go`
- Modify: `go.mod` (add `github.com/BurntSushi/toml`)

- [ ] **Step 1: Write the failing test**

Create `internal/config/loader_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	contents := `
log_dir     = "~/logs"
models_root = "~/models"
python_bin  = "/usr/bin/python"

[router]
host        = "127.0.0.1"
port        = 1231
extra_ports = [8080]

[chat]
default_profile  = "p1"
host             = "127.0.0.1"
port             = 1234
swap_timeout_sec = 30

  [chat.profiles.p1]
  model  = "~/models/p1"
  engine = "lm"
`
	if err := os.WriteFile(cfgPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(c.LogDir, home) {
		t.Errorf("LogDir not ~-expanded: %q", c.LogDir)
	}
	if !strings.HasPrefix(c.ModelsRoot, home) {
		t.Errorf("ModelsRoot not ~-expanded: %q", c.ModelsRoot)
	}
	if !strings.HasPrefix(c.Chat.Profiles["p1"].Model, home) {
		t.Errorf("profile model not ~-expanded: %q", c.Chat.Profiles["p1"].Model)
	}
	if c.Router.Port != 1231 {
		t.Errorf("Router.Port: want 1231, got %d", c.Router.Port)
	}
}

func TestLoad_ValidatesAfterParse(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.toml")
	contents := `
python_bin = "/usr/bin/python"
[router]
host = "127.0.0.1"
port = 1231
[chat]
default_profile = "missing"
host = "127.0.0.1"
port = 1234
  [chat.profiles.p1]
  model = "/tmp"
  engine = "lm"
`
	os.WriteFile(cfgPath, []byte(contents), 0o644)

	_, err := Load(cfgPath)
	if err == nil || !strings.Contains(err.Error(), "default_profile") {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		in, want string
	}{
		{"~/foo", filepath.Join(home, "foo")},
		{"~", home},
		{"/abs/path", "/abs/path"},
		{"", ""},
		{"relative", "relative"},
	}
	for _, c := range cases {
		got := expandHome(c.in)
		if got != c.want {
			t.Errorf("expandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/config/...
```

Expected: FAIL — `Load`, `expandHome` undefined.

- [ ] **Step 3: Add the TOML dependency**

```bash
go get github.com/BurntSushi/toml@v1.4.0
```

- [ ] **Step 4: Implement `config/loader.go`**

Create `internal/config/loader.go`:

```go
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
		// Warn-via-error on unknown keys to catch typos.
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}

	c.LogDir = expandHome(c.LogDir)
	c.ModelsRoot = expandHome(c.ModelsRoot)
	c.PythonBin = expandHome(c.PythonBin)
	for name, prof := range c.Chat.Profiles {
		prof.Model = expandHome(prof.Model)
		prof.Draft = expandHome(prof.Draft)
		c.Chat.Profiles[name] = prof
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
```

- [ ] **Step 5: Run tests, verify pass**

```bash
go test ./internal/config/...
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/config/loader.go internal/config/loader_test.go
git commit -m "feat(config): TOML loader with ~ expansion"
```

---

## Task 11: `backend/backend.go` — interface + data types

**Why:** Pure data shapes. No I/O. Used by router and supervisor.

**Files:**
- Create: `internal/backend/backend.go`

- [ ] **Step 1: Write the file directly (no test — pure types)**

Create `internal/backend/backend.go`:

```go
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
	AliasName     string
	BaseURL       string
	UpstreamName  string
}

func (e *External) Alias() string { return e.AliasName }
func (e *External) URL() string   { return e.BaseURL }
func (e *External) UpstreamModel(_ context.Context) (string, error) {
	return e.UpstreamName, nil
}

// Health: trivial GET /v1/models with short timeout.
func (e *External) Health(ctx context.Context) error { return nil } // detailed impl lands in supervisor

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

func (c *ChatSwap) URL() string { return chatURL(c.Host, c.Port) }

// chatURL is exported for tests in other packages that want the canonical URL.
func ChatURL(host string, port int) string { return chatURL(host, port) }

func chatURL(host string, port int) string {
	return "http://" + host + ":" + itoa(port)
}

func itoa(n int) string {
	// avoid strconv import for the tiny case
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
```

- [ ] **Step 2: Verify it builds**

```bash
go build ./internal/backend/...
```

Expected: succeeds silently.

- [ ] **Step 3: Commit**

```bash
git add internal/backend/backend.go
git commit -m "feat(backend): types + interface"
```

---

## Task 12: `logobs/parser.go` — parse `[mlx-launch]` stderr lines

**Why:** Worker stderr is mixed: arbitrary `mlx_lm.server` log lines + our `[mlx-launch]` structured prefix. mlxd routes both to the same file but extracts structured events from the latter for `/v1/status`.

**Files:**
- Create: `internal/logobs/parser.go`
- Test: `internal/logobs/parser_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/logobs/parser_test.go`:

```go
package logobs

import (
	"testing"
)

func TestParse_MemSnapshot(t *testing.T) {
	ev, ok := Parse("[mlx-launch] mem: active=1234 cache=5678 peak=9999")
	if !ok || ev.Kind != KindMem {
		t.Fatalf("want mem kind, got: %+v ok=%v", ev, ok)
	}
	if ev.Mem.Active != 1234 || ev.Mem.Cache != 5678 || ev.Mem.Peak != 9999 {
		t.Errorf("mem fields wrong: %+v", ev.Mem)
	}
}

func TestParse_Timing(t *testing.T) {
	ev, ok := Parse("[mlx-launch] req=r-1 prompt=42t prefill=120.5ms@123.4tps decode=850.2ms@45.6tps")
	if !ok || ev.Kind != KindTiming {
		t.Fatalf("want timing kind, got: %+v ok=%v", ev, ok)
	}
	if ev.Timing.RequestID != "r-1" {
		t.Errorf("RequestID: %q", ev.Timing.RequestID)
	}
	if ev.Timing.PromptTokens != 42 {
		t.Errorf("PromptTokens: %d", ev.Timing.PromptTokens)
	}
	if ev.Timing.PrefillMs < 120.4 || ev.Timing.PrefillMs > 120.6 {
		t.Errorf("PrefillMs: %v", ev.Timing.PrefillMs)
	}
}

func TestParse_WatchdogArmed(t *testing.T) {
	ev, ok := Parse("[mlx-launch] WATCHDOG: armed. baseline=1000000000 trigger=9000000000")
	if !ok || ev.Kind != KindWatchdogArmed {
		t.Fatalf("want watchdog-armed, got: %+v ok=%v", ev, ok)
	}
	if ev.Watchdog.Baseline != 1_000_000_000 || ev.Watchdog.Trigger != 9_000_000_000 {
		t.Errorf("watchdog fields: %+v", ev.Watchdog)
	}
}

func TestParse_WatchdogTrigger(t *testing.T) {
	ev, ok := Parse("[mlx-launch] WATCHDOG: active=9500000000 > trigger=9000000000 — execv-restarting")
	if !ok || ev.Kind != KindWatchdogTrigger {
		t.Fatalf("want watchdog-trigger, got: %+v ok=%v", ev, ok)
	}
}

func TestParse_Starting(t *testing.T) {
	ev, ok := Parse("[mlx-launch] starting engine=lm model=/tmp/m port=1234")
	if !ok || ev.Kind != KindStarting {
		t.Fatalf("want starting, got: %+v ok=%v", ev, ok)
	}
}

func TestParse_NonMatching(t *testing.T) {
	_, ok := Parse("loading model from /tmp/m")
	if ok {
		t.Errorf("expected ok=false for non-mlx-launch line")
	}
}

func TestParse_UnknownTag(t *testing.T) {
	ev, ok := Parse("[mlx-launch] something we don't recognize yet")
	if !ok || ev.Kind != KindUnknown {
		t.Fatalf("want unknown, got: %+v ok=%v", ev, ok)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/logobs/...
```

Expected: FAIL — package missing.

- [ ] **Step 3: Implement `logobs/parser.go`**

Create `internal/logobs/parser.go`:

```go
package logobs

import (
	"regexp"
	"strconv"
	"strings"
)

type Kind int

const (
	KindUnknown Kind = iota
	KindMem
	KindTiming
	KindWatchdogArmed
	KindWatchdogTrigger
	KindStarting
)

type MemSnapshot struct {
	Active int64
	Cache  int64
	Peak   int64
}

type Timing struct {
	RequestID    string
	PromptTokens int
	PrefillMs    float64
	PrefillTPS   float64
	DecodeMs     float64
	DecodeTPS    float64
}

type WatchdogEvent struct {
	Baseline int64
	Trigger  int64
	Active   int64
}

type Event struct {
	Raw      string
	Kind     Kind
	Mem      MemSnapshot
	Timing   Timing
	Watchdog WatchdogEvent
}

const prefix = "[mlx-launch] "

var (
	reMem     = regexp.MustCompile(`^mem: active=(\d+) cache=(\d+) peak=(\d+)`)
	reTiming  = regexp.MustCompile(`^req=(\S+) prompt=(\d+)t prefill=([\d.]+)ms@([\d.]+)tps decode=([\d.]+)ms@([\d.]+)tps`)
	reWdArm   = regexp.MustCompile(`^WATCHDOG: armed\. baseline=(\d+) trigger=(\d+)`)
	reWdTrig  = regexp.MustCompile(`^WATCHDOG: active=(\d+) > trigger=(\d+)`)
)

func Parse(line string) (Event, bool) {
	if !strings.HasPrefix(line, prefix) {
		return Event{}, false
	}
	body := strings.TrimPrefix(line, prefix)
	ev := Event{Raw: line, Kind: KindUnknown}

	if m := reMem.FindStringSubmatch(body); m != nil {
		ev.Kind = KindMem
		ev.Mem.Active = mustAtoi64(m[1])
		ev.Mem.Cache = mustAtoi64(m[2])
		ev.Mem.Peak = mustAtoi64(m[3])
		return ev, true
	}
	if m := reTiming.FindStringSubmatch(body); m != nil {
		ev.Kind = KindTiming
		ev.Timing.RequestID = m[1]
		ev.Timing.PromptTokens, _ = strconv.Atoi(m[2])
		ev.Timing.PrefillMs, _ = strconv.ParseFloat(m[3], 64)
		ev.Timing.PrefillTPS, _ = strconv.ParseFloat(m[4], 64)
		ev.Timing.DecodeMs, _ = strconv.ParseFloat(m[5], 64)
		ev.Timing.DecodeTPS, _ = strconv.ParseFloat(m[6], 64)
		return ev, true
	}
	if m := reWdArm.FindStringSubmatch(body); m != nil {
		ev.Kind = KindWatchdogArmed
		ev.Watchdog.Baseline = mustAtoi64(m[1])
		ev.Watchdog.Trigger = mustAtoi64(m[2])
		return ev, true
	}
	if m := reWdTrig.FindStringSubmatch(body); m != nil {
		ev.Kind = KindWatchdogTrigger
		ev.Watchdog.Active = mustAtoi64(m[1])
		ev.Watchdog.Trigger = mustAtoi64(m[2])
		return ev, true
	}
	if strings.HasPrefix(body, "starting ") {
		ev.Kind = KindStarting
		return ev, true
	}
	return ev, true // matched prefix but no body pattern — KindUnknown but still ok=true
}

func mustAtoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/logobs/...
```

Expected: 7 passed.

- [ ] **Step 5: Commit**

```bash
git add internal/logobs/parser.go internal/logobs/parser_test.go
git commit -m "feat(logobs): parse [mlx-launch] stderr events"
```

---

## Task 13: `supervisor/worker.go` — generic Worker

**Why:** Spawns one Python launcher_shim process. Pipes stderr line-by-line into the logobs parser. Owns lifecycle (`Start`, `Signal`, `Wait`). Notifies via channels.

**Files:**
- Create: `internal/supervisor/worker.go`
- Test: `internal/supervisor/worker_test.go`

- [ ] **Step 1: Write the failing test (uses `/bin/sh -c` fixtures)**

Create `internal/supervisor/worker_test.go`:

```go
package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWorker_StartAndExitNaturally(t *testing.T) {
	w := New(WorkerSpec{
		Name:    "test-1",
		Command: "/bin/sh",
		Args:    []string{"-c", "echo '[mlx-launch] starting engine=lm model=/x port=1' 1>&2; exit 0"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case res := <-w.Done():
		if res.ExitCode != 0 {
			t.Errorf("exit code: want 0 got %d", res.ExitCode)
		}
	case <-ctx.Done():
		t.Fatal("worker didn't exit in time")
	}

	lines := w.StderrLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "starting engine=lm") {
		t.Errorf("expected starting line in stderr, got: %q", joined)
	}
}

func TestWorker_StreamingStderr(t *testing.T) {
	w := New(WorkerSpec{
		Name:    "test-2",
		Command: "/bin/sh",
		Args:    []string{"-c", "for i in 1 2 3 4 5; do echo \"[mlx-launch] mem: active=$i cache=0 peak=0\" 1>&2; done; exit 0"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := []string{}
	go func() {
		for ev := range w.Events() {
			events = append(events, ev.Raw)
		}
	}()

	<-w.Done()
	time.Sleep(50 * time.Millisecond)
	if len(events) < 5 {
		t.Errorf("want >=5 events, got %d: %v", len(events), events)
	}
}

func TestWorker_Signal(t *testing.T) {
	w := New(WorkerSpec{
		Name:    "test-3",
		Command: "/bin/sh",
		Args:    []string{"-c", "trap 'exit 0' TERM; while true; do sleep 0.1; done"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if err := w.Signal("TERM"); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	select {
	case res := <-w.Done():
		if res.ExitCode != 0 {
			t.Errorf("exit code after TERM: %d", res.ExitCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker didn't exit after TERM")
	}
}

func TestWorker_PIDExposed(t *testing.T) {
	w := New(WorkerSpec{
		Name:    "test-4",
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 1"},
	})
	ctx := context.Background()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if w.PID() <= 0 {
		t.Errorf("expected PID > 0, got %d", w.PID())
	}
	w.Signal("KILL")
	<-w.Done()
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/supervisor/...
```

Expected: FAIL — package missing.

- [ ] **Step 3: Implement `supervisor/worker.go`**

Create `internal/supervisor/worker.go`:

```go
package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"

	"github.com/guygrigsby/mlx-stack/internal/logobs"
)

type WorkerSpec struct {
	Name    string            // label for logs
	Command string            // python_bin
	Args    []string          // ["-m", "mlx_stack.launcher_shim", ...]
	Env     []string          // appended to os.Environ()
	Logger  *slog.Logger      // optional; defaults to slog.Default()
}

type WorkerResult struct {
	ExitCode int
	Err      error
}

type Worker struct {
	spec WorkerSpec
	cmd  *exec.Cmd
	pid  int

	stderrMu    sync.Mutex
	stderrLines []string

	events chan logobs.Event
	done   chan WorkerResult

	startOnce sync.Once
}

func New(spec WorkerSpec) *Worker {
	if spec.Logger == nil {
		spec.Logger = slog.Default()
	}
	return &Worker{
		spec:   spec,
		events: make(chan logobs.Event, 256),
		done:   make(chan WorkerResult, 1),
	}
}

func (w *Worker) Start(ctx context.Context) error {
	var startErr error
	w.startOnce.Do(func() {
		cmd := exec.CommandContext(ctx, w.spec.Command, w.spec.Args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = append(cmd.Environ(), w.spec.Env...)

		stderr, err := cmd.StderrPipe()
		if err != nil {
			startErr = fmt.Errorf("stderr pipe: %w", err)
			return
		}

		if err := cmd.Start(); err != nil {
			startErr = fmt.Errorf("start: %w", err)
			return
		}
		w.cmd = cmd
		w.pid = cmd.Process.Pid

		go w.consumeStderr(stderr)
		go w.wait()
	})
	return startErr
}

func (w *Worker) consumeStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		w.stderrMu.Lock()
		w.stderrLines = append(w.stderrLines, line)
		if len(w.stderrLines) > 500 {
			w.stderrLines = w.stderrLines[len(w.stderrLines)-500:]
		}
		w.stderrMu.Unlock()

		w.spec.Logger.Info("worker.stderr", "name", w.spec.Name, "pid", w.pid, "line", line)

		if ev, ok := logobs.Parse(line); ok {
			select {
			case w.events <- ev:
			default:
				// channel full — drop oldest by spinning a receive
				select {
				case <-w.events:
				default:
				}
				select {
				case w.events <- ev:
				default:
				}
			}
		}
	}
}

func (w *Worker) wait() {
	err := w.cmd.Wait()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	close(w.events)
	w.done <- WorkerResult{ExitCode: code, Err: err}
}

func (w *Worker) Done() <-chan WorkerResult { return w.done }
func (w *Worker) Events() <-chan logobs.Event { return w.events }
func (w *Worker) PID() int { return w.pid }

func (w *Worker) StderrLines() []string {
	w.stderrMu.Lock()
	defer w.stderrMu.Unlock()
	out := make([]string, len(w.stderrLines))
	copy(out, w.stderrLines)
	return out
}

func (w *Worker) Signal(name string) error {
	if w.cmd == nil || w.cmd.Process == nil {
		return fmt.Errorf("worker not started")
	}
	sig, err := signalFor(name)
	if err != nil {
		return err
	}
	// Signal the process group so children also receive it.
	return syscall.Kill(-w.cmd.Process.Pid, sig)
}

func signalFor(name string) (syscall.Signal, error) {
	switch name {
	case "TERM":
		return syscall.SIGTERM, nil
	case "KILL":
		return syscall.SIGKILL, nil
	case "INT":
		return syscall.SIGINT, nil
	case "HUP":
		return syscall.SIGHUP, nil
	default:
		return 0, fmt.Errorf("unknown signal %q", name)
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/supervisor/... -v -run TestWorker
```

Expected: 4 passed.

- [ ] **Step 5: Commit**

```bash
git add internal/supervisor/worker.go internal/supervisor/worker_test.go
git commit -m "feat(supervisor): generic Worker with stderr piping"
```

---

## Task 14: `supervisor/chatswap.go` — EnsureProfile with lock + probe loop

**Why:** Replaces the shell-out `mlx restart chat --chat-model X`. In-process: hold a lock, kill old worker, spawn new worker, poll `GET /v1/models` until 200 or timeout.

**Files:**
- Create: `internal/supervisor/chatswap.go`
- Test: `internal/supervisor/chatswap_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/supervisor/chatswap_test.go`:

```go
package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func TestChatSwap_NoSwapWhenAlreadyCurrent(t *testing.T) {
	cs, started := newTestSwap(t)
	st := cs.State()
	st.CurrentProfile = "p1"
	st.WorkerURL = "http://127.0.0.1:1"
	// And cs.current must be non-nil for EnsureProfile to short-circuit; the
	// factory doesn't run here. Plant a dummy worker via direct field write
	// (test-only access; the production path always assigns through the lock).
	cs.current = &Worker{spec: WorkerSpec{Name: "dummy"}}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := cs.EnsureProfile(ctx, "p1"); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if atomic.LoadInt32(started) != 0 {
		t.Errorf("expected zero spawns, got %d", *started)
	}
}

func TestChatSwap_FirstStartProbesUntilReady(t *testing.T) {
	cs, started := newTestSwap(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(200)
			w.Write([]byte(`{"data":[{"id":"x"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	cs.upstreamURLOverride = upstream.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cs.EnsureProfile(ctx, "p1"); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if atomic.LoadInt32(started) != 1 {
		t.Errorf("expected 1 spawn, got %d", *started)
	}
	if cs.State().CurrentProfile != "p1" {
		t.Errorf("CurrentProfile: %q", cs.State().CurrentProfile)
	}
}

func TestChatSwap_SwapKillsOldSpawnsNew(t *testing.T) {
	cs, started := newTestSwap(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()
	cs.upstreamURLOverride = upstream.URL

	ctx := context.Background()
	if err := cs.EnsureProfile(ctx, "p1"); err != nil {
		t.Fatalf("first EnsureProfile: %v", err)
	}
	firstPID := cs.State().WorkerPID

	if err := cs.EnsureProfile(ctx, "p2"); err != nil {
		t.Fatalf("second EnsureProfile: %v", err)
	}
	if cs.State().CurrentProfile != "p2" {
		t.Errorf("CurrentProfile after swap: %q", cs.State().CurrentProfile)
	}
	if cs.State().WorkerPID == firstPID {
		t.Errorf("expected different PID after swap")
	}
	if n := atomic.LoadInt32(started); n != 2 {
		t.Errorf("expected 2 spawns, got %d", n)
	}
}

func TestChatSwap_UnknownProfile(t *testing.T) {
	cs, _ := newTestSwap(t)
	ctx := context.Background()
	err := cs.EnsureProfile(ctx, "ghost")
	if err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("expected unknown profile error, got: %v", err)
	}
}

func TestChatSwap_ConcurrentRequestsSwapOnce(t *testing.T) {
	cs, started := newTestSwap(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()
	cs.upstreamURLOverride = upstream.URL

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cs.EnsureProfile(context.Background(), "p1")
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt32(started); n != 1 {
		t.Errorf("expected 1 spawn under concurrency, got %d", n)
	}
}

// newTestSwap builds a ChatSwap whose worker spawner is a tiny sh script
// that just sleeps. Probe goes against the test's httptest server.
func newTestSwap(t *testing.T) (*ChatSwap, *int32) {
	t.Helper()
	var started int32
	profiles := map[string]config.Profile{
		"p1": {Model: "/tmp/p1", Engine: "lm"},
		"p2": {Model: "/tmp/p2", Engine: "lm"},
	}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	cs := NewChatSwap(ChatSwapOpts{
		Host:           "127.0.0.1",
		Port:           port,
		Profiles:       profiles,
		DefaultProfile: "p1",
		SwapTimeoutSec: 5,
		ProbeInterval:  20 * time.Millisecond,
		// override the worker factory for tests
		WorkerFactory: func(name string, args []string) *Worker {
			atomic.AddInt32(&started, 1)
			// Sleep for a few seconds — we only need it alive until the probe loop sees 200 from the httptest server.
			return New(WorkerSpec{
				Name:    name,
				Command: "/bin/sh",
				Args:    []string{"-c", "sleep 2"},
			})
		},
	})
	// Ensure the file location info points to this file in t.Errorf output.
	_, file, _, _ := runtime.Caller(0)
	if filepath.Base(file) != "chatswap_test.go" {
		t.Fatal("test file misnamed")
	}
	return cs, &started
}

func freePort() (int, error) {
	// crude: bind :0, read port, close. Race-prone but fine for tests.
	l, err := newListener()
	if err != nil {
		return 0, err
	}
	defer l.Close()
	_, ps, _ := strings.Cut(l.Addr().String(), ":")
	p, _ := strconv.Atoi(ps)
	return p, nil
}
```

The `newListener()` is a thin wrapper — add this helper file:

Create `internal/supervisor/testhelpers_test.go`:

```go
package supervisor

import "net"

func newListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/supervisor/... -run TestChatSwap -v
```

Expected: FAIL — `ChatSwap`, `NewChatSwap`, `ChatSwapOpts` undefined.

- [ ] **Step 3: Implement `supervisor/chatswap.go`**

Create `internal/supervisor/chatswap.go`:

```go
package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
)

type ChatSwapOpts struct {
	Host           string
	Port           int
	Profiles       map[string]config.Profile
	DefaultProfile string
	SwapTimeoutSec int
	ProbeInterval  time.Duration
	// WorkerFactory lets tests inject fake workers. Production wires this
	// to the real launcher_shim spawn.
	WorkerFactory func(name string, args []string) *Worker
	// Env applied to every spawned worker (cache/watchdog/memlog knobs).
	WorkerEnv []string
}

type ChatSwap struct {
	host           string
	port           int
	profiles       map[string]config.Profile
	defaultProfile string
	swapTimeout    time.Duration
	probeInterval  time.Duration
	factory        func(name string, args []string) *Worker
	env            []string

	mu      sync.Mutex
	state   *backend.ChatState
	current *Worker

	// Set in tests to redirect HTTP probes to a captive server.
	upstreamURLOverride string
}

func NewChatSwap(opts ChatSwapOpts) *ChatSwap {
	pi := opts.ProbeInterval
	if pi == 0 {
		pi = 250 * time.Millisecond
	}
	st := opts.SwapTimeoutSec
	if st == 0 {
		st = 90
	}
	return &ChatSwap{
		host:           opts.Host,
		port:           opts.Port,
		profiles:       opts.Profiles,
		defaultProfile: opts.DefaultProfile,
		swapTimeout:    time.Duration(st) * time.Second,
		probeInterval:  pi,
		factory:        opts.WorkerFactory,
		env:            opts.WorkerEnv,
		state:          &backend.ChatState{},
	}
}

func (c *ChatSwap) State() *backend.ChatState { return c.state }

func (c *ChatSwap) URL() string {
	return fmt.Sprintf("http://%s:%d", c.host, c.port)
}

func (c *ChatSwap) probeURL() string {
	if c.upstreamURLOverride != "" {
		return c.upstreamURLOverride + "/v1/models"
	}
	return c.URL() + "/v1/models"
}

func (c *ChatSwap) Profiles() map[string]config.Profile { return c.profiles }

func (c *ChatSwap) EnsureProfile(ctx context.Context, name string) error {
	prof, ok := c.profiles[name]
	if !ok {
		return fmt.Errorf("unknown profile %q", name)
	}

	c.mu.Lock()
	if c.state.CurrentProfile == name && c.current != nil {
		c.mu.Unlock()
		return nil
	}

	// Kill old worker (if any).
	if c.current != nil {
		_ = c.current.Signal("TERM")
		// Drain Done in background; don't block the swap on graceful exit.
		old := c.current
		go func() {
			select {
			case <-old.Done():
			case <-time.After(10 * time.Second):
				_ = old.Signal("KILL")
				<-old.Done()
			}
		}()
		c.current = nil
		c.state.CurrentProfile = ""
		c.state.WorkerPID = 0
		c.state.WorkerURL = ""
	}

	args := buildLauncherArgs(prof, c.host, c.port)
	w := c.factory(fmt.Sprintf("chat[%s]", name), args)
	if err := w.Start(ctx); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("spawn chat worker: %w", err)
	}
	c.current = w
	c.state.WorkerPID = w.PID()
	c.state.WorkerURL = c.URL()
	c.mu.Unlock()

	// Probe outside the lock so a slow load doesn't block readers of State()
	// (they only read fields that are written above).
	if err := c.probeReady(ctx); err != nil {
		c.mu.Lock()
		_ = c.current.Signal("KILL")
		c.current = nil
		c.state.WorkerPID = 0
		c.state.WorkerURL = ""
		c.mu.Unlock()
		return fmt.Errorf("probe chat[%s]: %w", name, err)
	}

	c.mu.Lock()
	c.state.CurrentProfile = name
	c.mu.Unlock()
	return nil
}

func (c *ChatSwap) probeReady(ctx context.Context) error {
	deadline := time.Now().Add(c.swapTimeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", c.probeURL(), nil)
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(c.probeInterval)
	}
	return fmt.Errorf("not ready within %s", c.swapTimeout)
}

func buildLauncherArgs(p config.Profile, host string, port int) []string {
	args := []string{
		"-m", "mlx_stack.launcher_shim",
		"--engine", p.Engine,
		"--model", p.Model,
		"--host", host,
		"--port", fmt.Sprintf("%d", port),
	}
	if p.Draft != "" {
		args = append(args, "--draft-model", p.Draft)
	}
	return args
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/supervisor/... -v
```

Expected: all pass (4 Worker + 5 ChatSwap).

- [ ] **Step 5: Commit**

```bash
git add internal/supervisor/chatswap.go internal/supervisor/chatswap_test.go internal/supervisor/testhelpers_test.go
git commit -m "feat(supervisor): in-process chat profile swap"
```

---

## Task 15: `router/rewrite.go` — model field rewriting

**Why:** Client sends `{"model":"valkyrie", ...}`. Upstream expects `{"model":"/Users/.../mlx-models/valkyrie", ...}`. Rewrite without re-serializing the body when we don't have to (preserve key order, trailing whitespace, etc.).

**Files:**
- Create: `internal/router/rewrite.go`
- Test: `internal/router/rewrite_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/router/rewrite_test.go`:

```go
package router

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestExtractModel(t *testing.T) {
	body := []byte(`{"model":"valkyrie","messages":[{"role":"user","content":"hi"}]}`)
	m, err := ExtractModel(body)
	if err != nil {
		t.Fatal(err)
	}
	if m != "valkyrie" {
		t.Errorf("ExtractModel: want valkyrie, got %q", m)
	}
}

func TestExtractModel_Missing(t *testing.T) {
	_, err := ExtractModel([]byte(`{"messages":[]}`))
	if err == nil {
		t.Error("expected error for missing model field")
	}
}

func TestRewriteModel(t *testing.T) {
	body := []byte(`{"model":"valkyrie","stream":true}`)
	out, err := RewriteModel(body, "/abs/path/to/valkyrie")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["model"] != "/abs/path/to/valkyrie" {
		t.Errorf("rewrite: %v", parsed["model"])
	}
	if parsed["stream"] != true {
		t.Errorf("stream lost during rewrite: %v", parsed["stream"])
	}
}

func TestRewriteModel_PreservesNonModelFields(t *testing.T) {
	body := []byte(`{"model":"x","temperature":0.7,"max_tokens":256}`)
	out, _ := RewriteModel(body, "y")
	if !bytes.Contains(out, []byte(`"temperature":0.7`)) {
		t.Errorf("temperature dropped: %s", out)
	}
	if !bytes.Contains(out, []byte(`"max_tokens":256`)) {
		t.Errorf("max_tokens dropped: %s", out)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/router/...
```

Expected: FAIL — package missing.

- [ ] **Step 3: Implement `router/rewrite.go`**

Create `internal/router/rewrite.go`:

```go
package router

import (
	"encoding/json"
	"fmt"
)

func ExtractModel(body []byte) (string, error) {
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", fmt.Errorf("parse body: %w", err)
	}
	if probe.Model == "" {
		return "", fmt.Errorf("model field missing")
	}
	return probe.Model, nil
}

func RewriteModel(body []byte, newModel string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}
	enc, _ := json.Marshal(newModel)
	m["model"] = enc
	return json.Marshal(m)
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/router/...
```

Expected: 4 passed.

- [ ] **Step 5: Commit**

```bash
git add internal/router/rewrite.go internal/router/rewrite_test.go
git commit -m "feat(router): model field extract/rewrite"
```

---

## Task 16: `router/proxy.go` — streaming reverse proxy

**Why:** OpenAI chat completions are SSE chunks. We must forward them as they arrive, never buffer. `httputil.ReverseProxy` works but we need a wrapper that hands it a rewritten body.

**Files:**
- Create: `internal/router/proxy.go`
- Test: `internal/router/proxy_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/router/proxy_test.go`:

```go
package router

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxy_NonStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"model":"upstream-name"`)) {
			t.Errorf("model not rewritten: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer upstream.Close()

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"alias","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	err := ProxyJSON(rr, req, upstream.URL, "upstream-name")
	if err != nil {
		t.Fatalf("ProxyJSON: %v", err)
	}
	resp := rr.Result()
	if resp.StatusCode != 200 {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestProxy_StreamsChunks(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			w.Write([]byte("data: chunk-" + string(rune('0'+i)) + "\n\n"))
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"x","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	if err := ProxyJSON(rr, req, upstream.URL, "upstream-name"); err != nil {
		t.Fatalf("ProxyJSON: %v", err)
	}
	body := rr.Body.String()
	if strings.Count(body, "data: chunk-") != 5 {
		t.Errorf("want 5 data chunks, got body: %q", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("missing [DONE]")
	}
}

func TestProxy_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer upstream.Close()

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	_ = ProxyJSON(rr, req, upstream.URL, "upstream-name")
	if rr.Code != 500 {
		t.Errorf("status: %d", rr.Code)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/router/...
```

Expected: FAIL — `ProxyJSON` undefined.

- [ ] **Step 3: Implement `router/proxy.go`**

Create `internal/router/proxy.go`:

```go
package router

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ProxyJSON forwards a JSON request to upstreamBase, rewriting the "model"
// field to upstreamModel. Streams the response body through as-is.
func ProxyJSON(w http.ResponseWriter, r *http.Request, upstreamBase, upstreamModel string) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return err
	}
	rewritten, err := RewriteModel(body, upstreamModel)
	if err != nil {
		http.Error(w, "rewrite: "+err.Error(), 400)
		return err
	}

	u, err := url.Parse(upstreamBase)
	if err != nil {
		http.Error(w, "bad upstream url", 500)
		return err
	}
	u.Path = r.URL.Path
	u.RawQuery = r.URL.RawQuery

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), bytes.NewReader(rewritten))
	if err != nil {
		http.Error(w, "build req: "+err.Error(), 500)
		return err
	}
	copyHeaders(outReq.Header, r.Header)
	outReq.ContentLength = int64(len(rewritten))
	outReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(rewritten)))

	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), 502)
		return err
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return nil
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		// drop hop-by-hop
		switch k {
		case "Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding", "Te", "Trailer", "Upgrade":
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/router/...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/router/proxy.go internal/router/proxy_test.go
git commit -m "feat(router): streaming JSON reverse proxy"
```

---

## Task 17: `router/catalog.go` — /v1/models aggregation

**Files:**
- Create: `internal/router/catalog.go`
- Test: `internal/router/catalog_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/router/catalog_test.go`:

```go
package router

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func TestCatalog_AggregatesChatProfiles(t *testing.T) {
	cfg := &config.Config{
		Chat: config.Chat{
			Profiles: map[string]config.Profile{
				"valkyrie": {Model: "/m/v", Engine: "lm"},
				"scout":    {Model: "/m/s", Engine: "vlm"},
			},
		},
	}
	c := NewCatalog(cfg)
	out := c.List()
	ids := []string{}
	for _, m := range out {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "scout" || ids[1] != "valkyrie" {
		t.Errorf("got: %v", ids)
	}
}

func TestCatalog_JSONShape(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"p": {Model: "/m", Engine: "lm"}}}}
	c := NewCatalog(cfg)
	b, _ := json.Marshal(c.OpenAIResponse())
	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	json.Unmarshal(b, &resp)
	if resp.Object != "list" {
		t.Errorf("object: %q", resp.Object)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "p" || resp.Data[0].Object != "model" {
		t.Errorf("data: %+v", resp.Data)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/router/...
```

Expected: FAIL — `NewCatalog`, `Catalog.List`, etc. undefined.

- [ ] **Step 3: Implement `router/catalog.go`**

Create `internal/router/catalog.go`:

```go
package router

import (
	"time"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

type Model struct {
	ID string `json:"id"`
}

type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type OpenAIList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

type Catalog struct {
	cfg *config.Config
}

func NewCatalog(cfg *config.Config) *Catalog { return &Catalog{cfg: cfg} }

func (c *Catalog) List() []Model {
	out := []Model{}
	for name := range c.cfg.Chat.Profiles {
		out = append(out, Model{ID: name})
	}
	return out
}

func (c *Catalog) OpenAIResponse() OpenAIList {
	models := c.List()
	now := time.Now().Unix()
	data := make([]OpenAIModel, 0, len(models))
	for _, m := range models {
		data = append(data, OpenAIModel{ID: m.ID, Object: "model", Created: now, OwnedBy: "mlx-stack"})
	}
	return OpenAIList{Object: "list", Data: data}
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/router/...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/router/catalog.go internal/router/catalog_test.go
git commit -m "feat(router): /v1/models catalog from config"
```

---

## Task 18: `router/server.go` — http.Server + handlers

**Files:**
- Create: `internal/router/server.go`
- Test: `internal/router/server_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/router/server_test.go`:

```go
package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

type fakeSwap struct {
	ensureErr error
	ensureCalls []string
	upstream  string
}

func (f *fakeSwap) EnsureProfile(ctx context.Context, name string) error {
	f.ensureCalls = append(f.ensureCalls, name)
	return f.ensureErr
}
func (f *fakeSwap) UpstreamModel(name string) string { return "/abs/" + name }
func (f *fakeSwap) BaseURL() string                  { return f.upstream }

func TestServer_ChatCompletionsFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"/abs/valkyrie"`) {
			t.Errorf("upstream got body: %s", body)
		}
		w.Write([]byte(`{"id":"x"}`))
	}))
	defer upstream.Close()

	swap := &fakeSwap{upstream: upstream.URL}
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"valkyrie": {Model: "/m", Engine: "lm"}}}}
	srv := NewServer(ServerOpts{Config: cfg, Chat: swap})

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"valkyrie"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if len(swap.ensureCalls) != 1 || swap.ensureCalls[0] != "valkyrie" {
		t.Errorf("ensure calls: %v", swap.ensureCalls)
	}
}

func TestServer_ListModels(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"a": {Model: "/m", Engine: "lm"}, "b": {Model: "/m", Engine: "lm"}}}}
	srv := NewServer(ServerOpts{Config: cfg, Chat: &fakeSwap{}})

	req := httptest.NewRequest("GET", "/v1/models", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	var resp OpenAIList
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Object != "list" || len(resp.Data) != 2 {
		t.Errorf("resp: %+v", resp)
	}
}

func TestServer_UnknownModelReturns400(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"valkyrie": {Model: "/m", Engine: "lm"}}}}
	swap := &fakeSwap{}
	srv := NewServer(ServerOpts{Config: cfg, Chat: swap})

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"ghost"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/router/...
```

Expected: FAIL — `NewServer`, `ServerOpts`, etc. undefined.

- [ ] **Step 3: Implement `router/server.go`**

Create `internal/router/server.go`:

```go
package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

// ChatSwapper is the subset of supervisor.ChatSwap that the router uses.
type ChatSwapper interface {
	EnsureProfile(ctx context.Context, name string) error
	UpstreamModel(name string) string
	BaseURL() string
}

type ServerOpts struct {
	Config *config.Config
	Chat   ChatSwapper
}

type Server struct {
	cfg     *config.Config
	chat    ChatSwapper
	catalog *Catalog
}

func NewServer(opts ServerOpts) *Server {
	return &Server{
		cfg:     opts.Config,
		chat:    opts.Chat,
		catalog: NewCatalog(opts.Config),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("POST /v1/completions", s.handleChat)
	mux.HandleFunc("GET /v1/models", s.handleListModels)
	mux.HandleFunc("GET /health", s.handleHealth)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(204)
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.catalog.OpenAIResponse())
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body)) // restore for downstream

	model, err := ExtractModel(body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, ok := s.cfg.Chat.Profiles[model]; !ok {
		http.Error(w, fmt.Sprintf("unknown model %q", model), 400)
		return
	}

	if err := s.chat.EnsureProfile(r.Context(), model); err != nil {
		http.Error(w, "ensure profile: "+err.Error(), 502)
		return
	}

	_ = ProxyJSON(w, r, s.chat.BaseURL(), s.chat.UpstreamModel(model))
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/router/...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/router/server.go internal/router/server_test.go
git commit -m "feat(router): chat completions handler + routing"
```

---

## Task 19: `supervisor.ChatSwap` adapter methods for router

**Why:** `router.ChatSwapper` expects `UpstreamModel(name) string` and `BaseURL() string`. Add to `supervisor.ChatSwap`.

**Files:**
- Modify: `internal/supervisor/chatswap.go`

- [ ] **Step 1: Add adapter methods**

Edit `internal/supervisor/chatswap.go` to append at the end of the file (above `buildLauncherArgs`):

```go
// UpstreamModel returns the absolute model path the upstream server expects
// in the "model" field. This is the path mlx_lm.server registers under in
// its /v1/models, which is the absolute model directory.
func (c *ChatSwap) UpstreamModel(name string) string {
	p, ok := c.profiles[name]
	if !ok {
		return ""
	}
	return p.Model
}

// BaseURL is what the router proxies to.
func (c *ChatSwap) BaseURL() string { return c.URL() }
```

- [ ] **Step 2: Verify with a small extra test**

Append to `internal/supervisor/chatswap_test.go`:

```go
func TestChatSwap_UpstreamModelAndBaseURL(t *testing.T) {
	cs, _ := newTestSwap(t)
	if got := cs.UpstreamModel("p1"); got != "/tmp/p1" {
		t.Errorf("UpstreamModel: %q", got)
	}
	if got := cs.UpstreamModel("ghost"); got != "" {
		t.Errorf("UpstreamModel ghost: %q", got)
	}
	if !strings.Contains(cs.BaseURL(), "127.0.0.1:") {
		t.Errorf("BaseURL: %q", cs.BaseURL())
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/supervisor/... -v
```

Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add internal/supervisor/chatswap.go internal/supervisor/chatswap_test.go
git commit -m "feat(supervisor): ChatSwap upstream-model + base-url adapters"
```

---

## Task 20: `admin/server.go` + `admin/handlers.go` — unix-socket API

**Files:**
- Create: `internal/admin/server.go`
- Create: `internal/admin/handlers.go`
- Test: `internal/admin/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/admin/handlers_test.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
)

type fakeChat struct {
	state    *backend.ChatState
	ensureErr error
}

func (f *fakeChat) State() *backend.ChatState                    { return f.state }
func (f *fakeChat) EnsureProfile(ctx context.Context, n string) error { return f.ensureErr }
func (f *fakeChat) Stop(ctx context.Context) error               { return nil }

func newTestHandlers() *Handlers {
	return &Handlers{
		Config: &config.Config{
			Chat: config.Chat{
				DefaultProfile: "p1",
				Profiles:       map[string]config.Profile{"p1": {Model: "/m", Engine: "lm"}, "p2": {Model: "/m", Engine: "lm"}},
			},
		},
		Chat: &fakeChat{state: &backend.ChatState{CurrentProfile: "p1", WorkerPID: 12345}},
	}
}

func TestHandler_Health(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("GET", "/v1/health", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: %d", rr.Code)
	}
}

func TestHandler_Status(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("GET", "/v1/status", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	var resp StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Chat.CurrentProfile != "p1" || resp.Chat.PID != 12345 {
		t.Errorf("chat status: %+v", resp.Chat)
	}
	if len(resp.Chat.Profiles) != 2 {
		t.Errorf("profile count: %d", len(resp.Chat.Profiles))
	}
}

func TestHandler_Swap(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("POST", "/v1/swap", strings.NewReader(`{"profile":"p2"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_SwapUnknownProfile(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("POST", "/v1/swap", strings.NewReader(`{"profile":"ghost"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: %d", rr.Code)
	}
}

func TestHandler_Stop(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("POST", "/v1/stop", strings.NewReader(`{"backend":"chat"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: %d", rr.Code)
	}
}

func _ = http.StatusOK // silence unused import if test edits churn
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/admin/...
```

Expected: FAIL — `Handlers`, `StatusResponse`, etc. undefined.

- [ ] **Step 3: Implement `admin/handlers.go`**

Create `internal/admin/handlers.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
)

type ChatController interface {
	State() *backend.ChatState
	EnsureProfile(ctx context.Context, name string) error
	Stop(ctx context.Context) error
}

type Handlers struct {
	Config *config.Config
	Chat   ChatController
}

type ChatStatus struct {
	CurrentProfile string   `json:"current_profile"`
	PID            int      `json:"pid"`
	URL            string   `json:"url"`
	Profiles       []string `json:"profiles"`
}

type StatusResponse struct {
	Chat ChatStatus `json:"chat"`
}

type swapReq struct {
	Profile string `json:"profile"`
}

type backendReq struct {
	Backend string `json:"backend"`
}

func (h *Handlers) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", h.health)
	mux.HandleFunc("GET /v1/status", h.status)
	mux.HandleFunc("POST /v1/swap", h.swap)
	mux.HandleFunc("POST /v1/start", h.start)
	mux.HandleFunc("POST /v1/stop", h.stop)
	mux.HandleFunc("POST /v1/restart", h.restart)
	return mux
}

func (h *Handlers) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handlers) status(w http.ResponseWriter, r *http.Request) {
	st := h.Chat.State()
	profiles := make([]string, 0, len(h.Config.Chat.Profiles))
	for name := range h.Config.Chat.Profiles {
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)
	resp := StatusResponse{
		Chat: ChatStatus{
			CurrentProfile: st.CurrentProfile,
			PID:            st.WorkerPID,
			URL:            st.WorkerURL,
			Profiles:       profiles,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handlers) swap(w http.ResponseWriter, r *http.Request) {
	var req swapReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, ok := h.Config.Chat.Profiles[req.Profile]; !ok {
		http.Error(w, fmt.Sprintf("unknown profile %q", req.Profile), 400)
		return
	}
	if err := h.Chat.EnsureProfile(r.Context(), req.Profile); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) start(w http.ResponseWriter, r *http.Request) {
	var req backendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Backend != "chat" {
		http.Error(w, "only 'chat' supported in phase 1", 400)
		return
	}
	if err := h.Chat.EnsureProfile(r.Context(), h.Config.Chat.DefaultProfile); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) stop(w http.ResponseWriter, r *http.Request) {
	var req backendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Backend != "chat" {
		http.Error(w, "only 'chat' supported in phase 1", 400)
		return
	}
	if err := h.Chat.Stop(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) restart(w http.ResponseWriter, r *http.Request) {
	var req backendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Backend != "chat" {
		http.Error(w, "only 'chat' supported in phase 1", 400)
		return
	}
	if err := h.Chat.Stop(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := h.Chat.EnsureProfile(r.Context(), h.Config.Chat.DefaultProfile); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}
```

- [ ] **Step 4: Implement `admin/server.go`**

Create `internal/admin/server.go`:

```go
package admin

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

type Server struct {
	SocketPath string
	Handler    http.Handler

	listener net.Listener
	server   *http.Server
}

func (s *Server) Start() error {
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(s.SocketPath)
	ln, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.SocketPath, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.listener = ln
	s.server = &http.Server{Handler: s.Handler}
	go func() {
		err := s.server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Best-effort logging via slog.Default would be nicer; keep dep-free here.
		}
	}()
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	err := s.server.Shutdown(ctx)
	_ = os.Remove(s.SocketPath)
	return err
}
```

- [ ] **Step 5: Run tests, verify pass**

```bash
go test ./internal/admin/...
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/admin/
git commit -m "feat(admin): unix-socket API for status/swap/start/stop/restart"
```

---

## Task 21: `supervisor.ChatSwap.Stop` method

**Why:** `admin.ChatController` interface needs `Stop`. Add it.

**Files:**
- Modify: `internal/supervisor/chatswap.go`

- [ ] **Step 1: Add `Stop` method**

Append to `internal/supervisor/chatswap.go`:

```go
func (c *ChatSwap) Stop(ctx context.Context) error {
	c.mu.Lock()
	if c.current == nil {
		c.mu.Unlock()
		return nil
	}
	old := c.current
	c.current = nil
	c.state.CurrentProfile = ""
	c.state.WorkerPID = 0
	c.state.WorkerURL = ""
	c.mu.Unlock()

	_ = old.Signal("TERM")
	select {
	case <-old.Done():
	case <-time.After(30 * time.Second):
		_ = old.Signal("KILL")
		<-old.Done()
	case <-ctx.Done():
		_ = old.Signal("KILL")
		return ctx.Err()
	}
	return nil
}
```

- [ ] **Step 2: Add a test**

Append to `internal/supervisor/chatswap_test.go`:

```go
func TestChatSwap_Stop(t *testing.T) {
	cs, _ := newTestSwap(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200); w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()
	cs.upstreamURLOverride = upstream.URL

	if err := cs.EnsureProfile(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if err := cs.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if cs.State.CurrentProfile != "" || cs.State.WorkerPID != 0 {
		t.Errorf("state not cleared: %+v", cs.State)
	}
}
```

Note: in the test file, `cs.State` is the field name. The struct embeds it as `state *backend.ChatState`; the public accessor is `cs.State()`. **Update the test** to use the accessor:

Replace the assertion lines with:

```go
	st := cs.State()
	if st.CurrentProfile != "" || st.WorkerPID != 0 {
		t.Errorf("state not cleared: %+v", st)
	}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/supervisor/...
```

Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add internal/supervisor/chatswap.go internal/supervisor/chatswap_test.go
git commit -m "feat(supervisor): ChatSwap.Stop graceful shutdown"
```

---

## Task 22: `ipc/client.go` — CLI-side admin client

**Files:**
- Create: `internal/ipc/client.go`
- Test: `internal/ipc/client_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/ipc/client_test.go`:

```go
package ipc

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClient_RoundTripsOverUnixSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "admin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"got":"` + r.URL.Path + `"}`))
	})}).Serve(ln)

	c := New(sock)
	body, err := c.Get(context.Background(), "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "/v1/status") {
		t.Errorf("body: %s", body)
	}
}

func TestClient_PostJSON(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "admin.sock")
	ln, _ := net.Listen("unix", sock)
	defer ln.Close()

	go (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		w.Write(buf[:n])
	})}).Serve(ln)

	c := New(sock)
	body, err := c.PostJSON(context.Background(), "/v1/swap", []byte(`{"profile":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "p1") {
		t.Errorf("body: %s", body)
	}
}

func TestClient_ConnectionRefused(t *testing.T) {
	c := New("/nonexistent.sock")
	_, err := c.Get(context.Background(), "/v1/health")
	if err == nil {
		t.Fatal("expected error")
	}
}

func _ = os.Remove // silence unused
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/ipc/...
```

Expected: FAIL — `New`, `Client.Get`, `Client.PostJSON` undefined.

- [ ] **Step 3: Implement `ipc/client.go`**

Create `internal/ipc/client.go`:

```go
package ipc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
)

type Client struct {
	sockPath string
	http     *http.Client
}

func New(sockPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	return &Client{
		sockPath: sockPath,
		http:     &http.Client{Transport: tr},
	}
}

func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	return c.do(ctx, "GET", path, nil)
}

func (c *Client) PostJSON(ctx context.Context, path string, body []byte) ([]byte, error) {
	return c.do(ctx, "POST", path, body)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://mlxd"+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return b, fmt.Errorf("admin %s %s: %d: %s", method, path, resp.StatusCode, b)
	}
	return b, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/ipc/...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/
git commit -m "feat(ipc): unix-socket admin client"
```

---

## Task 23: `testdata/fakemlx/main.go` — fake mlx_lm.server

**Why:** Phase-1 integration tests can't spawn the real `mlx_lm.server` (no models, no GPU on CI). `fakemlx` mimics it: binds a port, serves `/v1/models` + `/v1/chat/completions` with canned SSE, prints `[mlx-launch]` stderr, honors SIGTERM.

**Files:**
- Create: `testdata/fakemlx/main.go`

- [ ] **Step 1: Implement `fakemlx`**

Create `testdata/fakemlx/main.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	model := flag.String("model", "", "")
	host := flag.String("host", "127.0.0.1", "")
	port := flag.Int("port", 0, "")
	streamChunks := flag.Int("chunks", 5, "number of SSE chunks per chat completion")
	flag.Parse()

	if *model == "" || *port == 0 {
		fmt.Fprintln(os.Stderr, "fakemlx: --model and --port required")
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "[mlx-launch] starting engine=fake model=%s port=%d\n", *model, *port)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": *model, "object": "model"}},
		})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		streaming := strings.Contains(string(body), `"stream":true`)
		if !streaming {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":      "fake-1",
				"object":  "chat.completion",
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < *streamChunks; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok-%d \"}}]}\n\n", i)
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	})

	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", *host, *port), Handler: mux}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	fmt.Fprintln(os.Stderr, "[mlx-launch] mem: active=1000000 cache=0 peak=1000000")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "fakemlx: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Verify it builds**

```bash
make fakemlx
./bin/fakemlx --help 2>&1 | head -10
```

Expected: prints flag usage.

- [ ] **Step 3: Smoke-test it by hand**

```bash
./bin/fakemlx --model /tmp/x --port 9999 &
sleep 0.3
curl -s http://127.0.0.1:9999/v1/models
kill %1; wait 2>/dev/null
```

Expected: JSON `{"object":"list","data":[{"id":"/tmp/x","object":"model"}]}`.

- [ ] **Step 4: Commit**

```bash
git add testdata/fakemlx/main.go
git commit -m "test: fakemlx fixture binary"
```

---

## Task 24: `cmd/mlxd/main.go` — daemon entry point

**Files:**
- Create: `cmd/mlxd/main.go`

- [ ] **Step 1: Implement `mlxd`**

Create `cmd/mlxd/main.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/admin"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/router"
	"github.com/guygrigsby/mlx-stack/internal/supervisor"
)

func main() {
	cmdRun := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := cmdRun.String("config", defaultConfigPath(), "path to config.toml")
	socketPath := cmdRun.String("socket", defaultSocketPath(), "admin unix socket path")
	logLevel := cmdRun.String("log-level", "info", "debug|info|warn|error")
	logJSON := cmdRun.Bool("log-json", false, "emit logs as JSON")

	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: mlxd run [--config path] [--socket path] [--log-level lvl] [--log-json]")
		os.Exit(2)
	}
	cmdRun.Parse(os.Args[2:])

	logger := setupLogger(*logLevel, *logJSON)
	slog.SetDefault(logger)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	logger.Info("config loaded", "path", *cfgPath, "router_port", cfg.Router.Port, "chat_port", cfg.Chat.Port)

	chatSwap := supervisor.NewChatSwap(supervisor.ChatSwapOpts{
		Host:           cfg.Chat.Host,
		Port:           cfg.Chat.Port,
		Profiles:       cfg.Chat.Profiles,
		DefaultProfile: cfg.Chat.DefaultProfile,
		SwapTimeoutSec: cfg.Chat.SwapTimeoutSec,
		WorkerFactory: func(name string, args []string) *supervisor.Worker {
			return supervisor.New(supervisor.WorkerSpec{
				Name:    name,
				Command: cfg.PythonBin,
				Args:    args,
				Env:     workerEnv(cfg),
				Logger:  logger,
			})
		},
		WorkerEnv: workerEnv(cfg),
	})

	routerSrv := router.NewServer(router.ServerOpts{
		Config: cfg,
		Chat:   chatSwap,
	})

	httpSrv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Router.Host, cfg.Router.Port),
		Handler: routerSrv.Handler(),
	}

	adminSrv := &admin.Server{
		SocketPath: *socketPath,
		Handler:    (&admin.Handlers{Config: cfg, Chat: chatSwap}).Mux(),
	}

	// Start admin socket first; if it fails we don't want a half-up daemon.
	if err := adminSrv.Start(); err != nil {
		logger.Error("admin server start", "err", err)
		os.Exit(1)
	}
	logger.Info("admin socket listening", "path", *socketPath)

	go func() {
		logger.Info("router listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("router serve", "err", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = chatSwap.Stop(ctx)
	_ = httpSrv.Shutdown(ctx)
	_ = adminSrv.Shutdown(ctx)
	logger.Info("bye")
}

func workerEnv(cfg *config.Config) []string {
	env := []string{}
	c := cfg.Chat
	if c.Cache.LimitBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_LIMIT_BYTES=%d", c.Cache.LimitBytes))
	}
	if c.Cache.ClearIntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_INTERVAL_SEC=%d", c.Cache.ClearIntervalSec))
	}
	if c.Cache.ClearThresholdBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_THRESHOLD_BYTES=%d", c.Cache.ClearThresholdBytes))
	}
	if c.Watchdog.KVHeadroomBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_KV_HEADROOM_BYTES=%d", c.Watchdog.KVHeadroomBytes))
	}
	if c.Watchdog.CheckIntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC=%d", c.Watchdog.CheckIntervalSec))
	}
	if c.Watchdog.GraceSec > 0 {
		env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_GRACE_SEC=%d", c.Watchdog.GraceSec))
	}
	if c.Memlog.IntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_MEMLOG_INTERVAL_SEC=%d", c.Memlog.IntervalSec))
	}
	return env
}

func setupLogger(level string, jsonOut bool) *slog.Logger {
	lvl := slog.LevelInfo
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if jsonOut {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mlx", "config.toml")
}

func defaultSocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "mlxd", "admin.sock")
}
```

- [ ] **Step 2: Verify it builds**

```bash
go build ./cmd/mlxd
```

Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add cmd/mlxd/main.go
git commit -m "feat(mlxd): daemon entry point"
```

---

## Task 25: `cmd/mlx/main.go` — CLI entry point

**Files:**
- Create: `cmd/mlx/main.go`

- [ ] **Step 1: Implement `mlx` CLI**

Create `cmd/mlx/main.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/ipc"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "status":
		cmdStatus(os.Args[2:])
	case "swap":
		cmdSwap(os.Args[2:])
	case "start":
		cmdStart(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "restart":
		cmdRestart(os.Args[2:])
	case "health":
		cmdHealth(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: mlx <subcommand>
  status                       show current backend state
  swap <profile>               swap chat profile
  start chat                   start chat backend (default profile)
  stop chat                    stop chat backend
  restart chat                 restart chat backend
  health                       daemon liveness check`)
}

func newClient() *ipc.Client {
	sock := os.Getenv("MLXD_SOCK")
	if sock == "" {
		home, _ := os.UserHomeDir()
		sock = filepath.Join(home, ".local", "state", "mlxd", "admin.sock")
	}
	return ipc.New(sock)
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 120*time.Second)
}

func notRunning() {
	fmt.Fprintln(os.Stderr, "mlxd is not running. Start with: `mlxd run` or `launchctl load …`")
	os.Exit(2)
}

func cmdHealth(args []string) {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	b, err := c.Get(cx, "/v1/health")
	if err != nil {
		notRunning()
	}
	fmt.Println(string(b))
}

func cmdStatus(args []string) {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	b, err := c.Get(cx, "/v1/status")
	if err != nil {
		notRunning()
	}
	var parsed map[string]any
	json.Unmarshal(b, &parsed)
	pretty, _ := json.MarshalIndent(parsed, "", "  ")
	fmt.Println(string(pretty))
}

func cmdSwap(args []string) {
	fs := flag.NewFlagSet("swap", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlx swap <profile>")
		os.Exit(2)
	}
	profile := fs.Arg(0)
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body, _ := json.Marshal(map[string]string{"profile": profile})
	resp, err := c.PostJSON(cx, "/v1/swap", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swap failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}

func cmdStart(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlx start <backend>")
		os.Exit(2)
	}
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body, _ := json.Marshal(map[string]string{"backend": args[0]})
	resp, err := c.PostJSON(cx, "/v1/start", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}

func cmdStop(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlx stop <backend>")
		os.Exit(2)
	}
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body, _ := json.Marshal(map[string]string{"backend": args[0]})
	resp, err := c.PostJSON(cx, "/v1/stop", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stop failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}

func cmdRestart(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlx restart <backend>")
		os.Exit(2)
	}
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body, _ := json.Marshal(map[string]string{"backend": args[0]})
	resp, err := c.PostJSON(cx, "/v1/restart", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restart failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}
```

- [ ] **Step 2: Verify it builds**

```bash
go build ./cmd/mlx
```

Expected: succeeds.

- [ ] **Step 3: Verify the CLI prints usage**

```bash
go run ./cmd/mlx
```

Expected: prints `usage: mlx <subcommand>` etc.

- [ ] **Step 4: Commit**

```bash
git add cmd/mlx/main.go
git commit -m "feat(mlx): cli with status/swap/start/stop/restart/health"
```

---

## Task 26: End-to-end test — mlxd + fakemlx + HTTP chat completion

**Why:** Wire all the pieces. Spawn mlxd pointed at a config that names `fakemlx` as the worker (via `python_bin` pointing to fakemlx instead of real Python). Hit `/v1/chat/completions`, verify the response flowed through.

**Files:**
- Create: `e2e/e2e_test.go`

- [ ] **Step 1: Write the failing test**

Create `e2e/e2e_test.go`:

```go
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2E_ChatCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	root := repoRoot(t)
	buildAll(t, root)

	dir := t.TempDir()
	routerPort := freePort(t)
	chatPort := freePort(t)
	sockPath := filepath.Join(dir, "admin.sock")

	// Make fakemlx pretend to be python: the launcher_shim arg list will become
	// fakemlx's flags. We use a shim shell script that strips the `-m mlx_stack.launcher_shim --engine X`
	// prefix and forwards `--model`, `--host`, `--port`.
	fakePython := filepath.Join(dir, "fake-python")
	if err := os.WriteFile(fakePython, []byte(fmt.Sprintf(`#!/bin/sh
# Drop "-m mlx_stack.launcher_shim --engine lm"; forward the rest to fakemlx.
shift 4
exec "%s/bin/fakemlx" "$@"
`, root)), 0o755); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf(`
log_dir     = "%s"
models_root = "%s"
python_bin  = "%s"

[router]
host = "127.0.0.1"
port = %d
extra_ports = []

[chat]
default_profile  = "p1"
host             = "127.0.0.1"
port             = %d
swap_timeout_sec = 5

  [chat.profiles.p1]
  model  = "/tmp/p1"
  engine = "lm"

  [chat.profiles.p2]
  model  = "/tmp/p2"
  engine = "lm"
`, dir, dir, fakePython, routerPort, chatPort)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	mlxd := exec.Command(filepath.Join(root, "bin", "mlxd"), "run",
		"--config", cfgPath,
		"--socket", sockPath,
		"--log-level", "debug",
	)
	mlxd.Stdout = os.Stdout
	mlxd.Stderr = os.Stderr
	if err := mlxd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		mlxd.Process.Signal(os.Interrupt)
		mlxd.Wait()
	}()

	waitPort(t, "127.0.0.1", routerPort, 5*time.Second)

	// First chat completion triggers swap to p1
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", routerPort),
		bytes.NewReader([]byte(`{"model":"p1","messages":[{"role":"user","content":"hi"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("body: %s", body)
	}

	// /v1/models lists both profiles
	resp2, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/models", routerPort))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct{ ID string } `json:"data"`
	}
	json.NewDecoder(resp2.Body).Decode(&list)
	if len(list.Data) != 2 {
		t.Errorf("expected 2 models, got %+v", list.Data)
	}
}

func TestE2E_HotSwap(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	root := repoRoot(t)
	buildAll(t, root)

	dir := t.TempDir()
	routerPort := freePort(t)
	chatPort := freePort(t)
	sockPath := filepath.Join(dir, "admin.sock")

	fakePython := filepath.Join(dir, "fake-python")
	os.WriteFile(fakePython, []byte(fmt.Sprintf(`#!/bin/sh
shift 4
exec "%s/bin/fakemlx" "$@"
`, root)), 0o755)

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf(`
log_dir     = "%s"
models_root = "%s"
python_bin  = "%s"

[router]
host = "127.0.0.1"
port = %d

[chat]
default_profile  = "p1"
host             = "127.0.0.1"
port             = %d
swap_timeout_sec = 5

  [chat.profiles.p1]
  model  = "/tmp/p1"
  engine = "lm"

  [chat.profiles.p2]
  model  = "/tmp/p2"
  engine = "lm"
`, dir, dir, fakePython, routerPort, chatPort)
	os.WriteFile(cfgPath, []byte(cfg), 0o644)

	mlxd := exec.Command(filepath.Join(root, "bin", "mlxd"), "run",
		"--config", cfgPath, "--socket", sockPath, "--log-level", "debug")
	mlxd.Stdout = os.Stdout
	mlxd.Stderr = os.Stderr
	mlxd.Start()
	defer func() { mlxd.Process.Signal(os.Interrupt); mlxd.Wait() }()
	waitPort(t, "127.0.0.1", routerPort, 5*time.Second)

	// Trigger p1
	do(t, routerPort, `{"model":"p1"}`)
	// Trigger p2 (swap)
	do(t, routerPort, `{"model":"p2"}`)
	// Back to p1 (swap)
	do(t, routerPort, `{"model":"p1"}`)
}

func do(t *testing.T, port int, payload string) {
	t.Helper()
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port),
		"application/json",
		strings.NewReader(payload),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func buildAll(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command("make", "build", "fakemlx")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitPort(t *testing.T, host string, port int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", host, port))
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("port %d not listening within %s", port, d)
}

func _ = context.Background // silence unused if test churn
```

- [ ] **Step 2: Run the e2e test**

```bash
go test ./e2e/... -v -timeout 60s
```

Expected: 2 passed (TestE2E_ChatCompletion, TestE2E_HotSwap).

If the test fails, the most likely causes:
- `bin/mlxd` or `bin/fakemlx` not built — `make build fakemlx` first.
- Port already in use — re-run; `freePort()` should pick a new one.
- `fake-python` not executable — verify `chmod 755`.

- [ ] **Step 3: Commit**

```bash
git add e2e/e2e_test.go
git commit -m "test(e2e): chat completion + hot-swap with fakemlx"
```

---

## Task 27: Manual smoke test against real `mlx_lm.server`

**Why:** Phase 1 acceptance criterion ("Test against real models") requires confirming that the full stack — Go daemon → Python launcher_shim → real mlx_lm.server → real model — works end to end on one model.

This task has no test code; the engineer executes the steps and confirms output.

**Files:** none (manual verification).

- [ ] **Step 1: Pick one existing model from `~/mlx-models/`**

```bash
ls ~/mlx-models/ | head -5
```

Pick one and note its name; for the rest of the steps assume `~/mlx-models/SMOKE_MODEL`.

- [ ] **Step 2: Write a smoke config**

Create `/tmp/mlx-smoke.toml`:

```toml
log_dir     = "~/logs/mlx-smoke"
models_root = "~/mlx-models"
python_bin  = "~/venvs/mlx/bin/python"

[router]
host = "127.0.0.1"
port = 1231

[chat]
default_profile  = "smoke"
host             = "127.0.0.1"
port             = 1244
swap_timeout_sec = 120

  [chat.cache]
  limit_bytes        = 2147483648
  clear_interval_sec = 60

  [chat.watchdog]
  kv_headroom_bytes  = 8000000000
  check_interval_sec = 30
  grace_sec          = 90

  [chat.memlog]
  interval_sec = 60

  [chat.profiles.smoke]
  model  = "~/mlx-models/SMOKE_MODEL"
  engine = "lm"
```

Replace `SMOKE_MODEL` with the directory name from step 1.

- [ ] **Step 3: Start mlxd in foreground**

In one terminal:

```bash
mkdir -p ~/logs/mlx-smoke
./bin/mlxd run --config /tmp/mlx-smoke.toml --log-level debug
```

Expected: prints `router listening addr=127.0.0.1:1231`, `admin socket listening …`.

- [ ] **Step 4: Send a chat completion request from another terminal**

```bash
curl -sS http://127.0.0.1:1231/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"smoke","messages":[{"role":"user","content":"Say hi in 3 words."}],"max_tokens":16}'
```

Expected: JSON response with a `choices[0].message.content` field. The first request will take longer (model load). Subsequent requests are fast.

- [ ] **Step 5: Verify `/v1/models`**

```bash
curl -sS http://127.0.0.1:1231/v1/models | head
```

Expected: `{"object":"list","data":[{"id":"smoke",…}]}`.

- [ ] **Step 6: Verify `mlx status`**

```bash
./bin/mlx --help    # smoke-test the CLI is present
MLXD_SOCK=~/.local/state/mlxd/admin.sock ./bin/mlx status
```

Expected: JSON showing `current_profile: smoke`, a positive `pid`, and `profiles: ["smoke"]`.

- [ ] **Step 7: Ctrl-C mlxd, confirm clean shutdown**

In the mlxd terminal, press Ctrl-C. Expected: `shutting down`, `bye`. Worker exits within ~30s.

- [ ] **Step 8: Commit a short SMOKE_NOTES.md (optional)**

Only if you want a record. Otherwise skip.

```bash
# Optional record:
# git add SMOKE_NOTES.md
# git commit -m "docs: phase 1 smoke test notes"
```

If everything above passed, **Phase 1 is complete.**

---

## Self-review checklist

Before declaring Phase 1 done, run this once more:

```bash
make test                                            # all unit tests
go test ./e2e/... -timeout 60s                       # e2e
go build ./...                                       # everything compiles
~/venvs/mlx/bin/python -m pytest python/tests -v     # python tests
```

Expected: all green.

---

## What's NOT in Phase 1 (separate plans to follow)

Each becomes its own plan, written when Phase 1 lands:

- **Phase 2 — Tags backend.** Always-on managed worker. Generalizes `supervisor.Managed` from `ChatSwap`.
- **Phase 3 — Embed backend.** Lift `mlx-embed-server.py` into `mlx_stack/embed_server/`. Both managed and external paths.
- **Phase 4 — Audio + Kokoro.** Add `--engine audio` to launcher_shim. Two managed instances.
- **Phase 5 — CLI parity.** `mlx monitor`, `mlx tail`, `mlx chat`, `mlx tag`, `mlx tags`, launchd plist.
- **Phase 6 — TOML migration + port swap.** `mlx config migrate`, cut over to ports 1230 + 8080, archive legacy/.
- **Phase 7 — Observability polish.** Status table render, SSE log tail, mem snapshots in status.

---

**Plan complete and saved to `docs/2026-05-19-mlx-stack-phase-1-skeleton-chat.md`.**

Execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
