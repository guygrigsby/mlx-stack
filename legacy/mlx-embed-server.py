#!/usr/bin/env python3
"""OpenAI-compatible /v1/embeddings server backed by mlx-embeddings."""

import argparse
import sys
import mlx.core as mx
from mlx_embeddings import load, generate
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import Union
import uvicorn

parser = argparse.ArgumentParser(description="MLX embedding server")
parser.add_argument("--model", required=True, help="HuggingFace repo or local path")
parser.add_argument("--host", default="127.0.0.1")
parser.add_argument("--port", type=int, default=1236)
args = parser.parse_args()

app = FastAPI()
_model = None
_tokenizer = None


class EmbedRequest(BaseModel):
    input: Union[str, list[str]]
    model: str = ""
    encoding_format: str = "float"


@app.on_event("startup")
async def startup():
    global _model, _tokenizer
    print(f"[mlx-embed] loading {args.model}", flush=True)
    _model, _tokenizer = load(args.model)
    mx.eval(_model.parameters())
    print("[mlx-embed] ready", flush=True)


@app.post("/v1/embeddings")
async def embeddings(req: EmbedRequest):
    if _model is None:
        raise HTTPException(status_code=503, detail="model not loaded")
    texts = [req.input] if isinstance(req.input, str) else list(req.input)
    if not texts:
        raise HTTPException(status_code=400, detail="input is empty")
    out = generate(_model, _tokenizer, texts)
    embs = out.text_embeds if out.text_embeds is not None else out.last_hidden_state
    mx.eval(embs)
    data = [
        {"object": "embedding", "embedding": embs[i].tolist(), "index": i}
        for i in range(len(texts))
    ]
    # best-effort token count using the tokenizer
    try:
        token_counts = [len(_tokenizer(t, add_special_tokens=True)["input_ids"]) for t in texts]
        total_tokens = sum(token_counts)
    except Exception:
        total_tokens = 0
    return {
        "object": "list",
        "data": data,
        "model": args.model,
        "usage": {"prompt_tokens": total_tokens, "total_tokens": total_tokens},
    }


@app.get("/health")
async def health():
    return {"status": "ok", "model": args.model}


if __name__ == "__main__":
    uvicorn.run(app, host=args.host, port=args.port, log_level="info")
