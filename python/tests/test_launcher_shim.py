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
        trust_remote_code=False,
    ))
    assert "--model" in argv and "/tmp/m" in argv
    assert "--port" in argv and "1234" in argv
    assert "--host" in argv and "127.0.0.1" in argv
    assert "--draft-model" not in argv
    assert "--trust-remote-code" not in argv


def test_build_server_argv_lm_with_draft():
    from mlx_stack import launcher_shim

    argv = launcher_shim.build_server_argv(types.SimpleNamespace(
        engine="lm", model="/tmp/m", draft_model="/tmp/d", host="127.0.0.1", port=1234,
        trust_remote_code=False,
    ))
    i = argv.index("--draft-model")
    assert argv[i + 1] == "/tmp/d"


def test_build_server_argv_forwards_trust_remote_code():
    from mlx_stack import launcher_shim

    argv = launcher_shim.build_server_argv(types.SimpleNamespace(
        engine="lm", model="/tmp/m", draft_model="", host="127.0.0.1", port=1234,
        trust_remote_code=True,
    ))
    assert "--trust-remote-code" in argv


def test_module_sets_mlx_disable_compile_on_import(monkeypatch):
    monkeypatch.delenv("MLX_DISABLE_COMPILE", raising=False)
    sys.modules.pop("mlx_stack.launcher_shim", None)

    import mlx_stack.launcher_shim  # noqa: F401

    assert os.environ.get("MLX_DISABLE_COMPILE") == "1"


def test_parse_args_accepts_embed_engine():
    from mlx_stack import launcher_shim
    args = launcher_shim.parse_args(["--engine", "embed", "--model", "/m", "--port", "1236"])
    assert args.engine == "embed"
    assert args.model == "/m"
    assert args.port == 1236


def test_main_embed_calls_embed_server_main(monkeypatch):
    """When engine=embed, main() calls embed_server.main(host, port, model)."""
    import sys
    import types

    called = {}

    fake_app = types.ModuleType("mlx_stack.embed_server.app")
    def fake_main(host, port, model_path):
        called["host"] = host
        called["port"] = port
        called["model_path"] = model_path
    fake_app.main = fake_main

    fake_embed_pkg = types.ModuleType("mlx_stack.embed_server")
    fake_embed_pkg.app = fake_app

    monkeypatch.setitem(sys.modules, "mlx_stack.embed_server", fake_embed_pkg)
    monkeypatch.setitem(sys.modules, "mlx_stack.embed_server.app", fake_app)

    from mlx_stack import launcher_shim
    launcher_shim.main(["--engine", "embed", "--model", "/m/embed", "--port", "1236", "--host", "127.0.0.1"])

    assert called == {"host": "127.0.0.1", "port": 1236, "model_path": "/m/embed"}


def test_parse_args_accepts_audio_engine_no_model():
    from mlx_stack import launcher_shim
    args = launcher_shim.parse_args(["--engine", "audio", "--port", "1237"])
    assert args.engine == "audio"
    assert args.port == 1237
    assert args.model == ""


def test_parse_args_lm_still_requires_model():
    from mlx_stack import launcher_shim
    import pytest as _pytest
    with _pytest.raises(SystemExit):
        launcher_shim.parse_args(["--engine", "lm", "--port", "1234"])


def test_main_audio_calls_mlx_audio_server(monkeypatch):
    import sys
    import types

    called = {}

    fake_audio = types.ModuleType("mlx_audio.server")
    def fake_main():
        called["argv"] = list(sys.argv)
    fake_audio.main = fake_main

    monkeypatch.setitem(sys.modules, "mlx_audio", types.ModuleType("mlx_audio"))
    monkeypatch.setitem(sys.modules, "mlx_audio.server", fake_audio)

    from mlx_stack import launcher_shim
    launcher_shim.main(["--engine", "audio", "--port", "1237", "--host", "127.0.0.1"])

    assert "argv" in called
    assert "--host" in called["argv"] and "127.0.0.1" in called["argv"]
    assert "--port" in called["argv"] and "1237" in called["argv"]
