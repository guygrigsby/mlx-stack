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
