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
    sys.modules.pop("mlx_stack.launcher_shim", None)

    import mlx_stack.launcher_shim  # noqa: F401

    assert os.environ.get("MLX_DISABLE_COMPILE") == "1"
