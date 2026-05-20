"""FastAPI app serving /v1/embeddings, ported from legacy mlx-embed-server.py.

Started by launcher_shim --engine embed --model PATH --port PORT.
"""
from __future__ import annotations

from typing import Any, Union

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel


class EmbedRequest(BaseModel):
    input: Union[str, list[str]]
    model: str = ""
    encoding_format: str = "float"
    dimensions: int | None = None


def build_app(model_path: str) -> FastAPI:
    """Build a FastAPI app that serves embeddings from the given model.

    Loads the model eagerly (so the first request doesn't pay the cost).
    """
    import mlx.core as mx
    from mlx_embeddings import load, generate

    model, tokenizer = load(model_path)
    try:
        mx.eval(model.parameters())
    except Exception:
        pass  # parameters() may be absent on test stubs

    app = FastAPI()

    @app.get("/v1/models")
    def list_models() -> dict[str, Any]:
        return {
            "object": "list",
            "data": [{"id": model_path, "object": "model"}],
        }

    @app.get("/health")
    def health() -> dict[str, str]:
        return {"status": "ok", "model": model_path}

    @app.post("/v1/embeddings")
    def embeddings(req: EmbedRequest) -> dict[str, Any]:
        texts = [req.input] if isinstance(req.input, str) else list(req.input)
        if not texts:
            raise HTTPException(status_code=400, detail="input is empty")

        out = generate(model, tokenizer, texts)
        embs = out.text_embeds if getattr(out, "text_embeds", None) is not None else out.last_hidden_state
        try:
            mx.eval(embs)
        except Exception:
            pass

        data = []
        for i, _ in enumerate(texts):
            vec = embs[i].tolist() if hasattr(embs[i], "tolist") else list(embs[i])
            vec = [float(x) for x in vec]
            if req.dimensions:
                vec = vec[: req.dimensions]
            data.append({"object": "embedding", "embedding": vec, "index": i})

        try:
            token_counts = [len(tokenizer(t, add_special_tokens=True)["input_ids"]) for t in texts]
            total_tokens = sum(token_counts)
        except Exception:
            total_tokens = 0

        return {
            "object": "list",
            "data": data,
            "model": model_path,
            "usage": {"prompt_tokens": total_tokens, "total_tokens": total_tokens},
        }

    return app


def main(host: str, port: int, model_path: str) -> None:
    """Entry point called by launcher_shim --engine embed."""
    import uvicorn
    uvicorn.run(build_app(model_path), host=host, port=port, log_level="info")
