"""Flatten xtc_special_tokens before mlx_lm.make_sampler sees it.

Upstream make_sampler forwards xtc_special_tokens directly to apply_xtc,
which expects a flat list[int]. The router sometimes passes [[198]] (output
of tokenizer.encode("\n")) which crashes Qwen2.5. We wrap make_sampler to
flatten any nested lists.
"""
from __future__ import annotations

_WRAPPED_MARKER = "__xtc_wrapped__"


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
    from mlx_lm import server as _server

    # Skip if already wrapped
    if hasattr(_server.make_sampler, _WRAPPED_MARKER):
        return

    original = _server.make_sampler

    def wrapped(*args, **kwargs):
        if "xtc_special_tokens" in kwargs:
            kwargs["xtc_special_tokens"] = _flatten(kwargs["xtc_special_tokens"])
        return original(*args, **kwargs)

    setattr(wrapped, _WRAPPED_MARKER, True)
    _server.make_sampler = wrapped
