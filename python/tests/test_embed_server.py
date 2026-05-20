import sys
import types

import pytest


@pytest.fixture
def fake_mlx_embeddings(monkeypatch):
    fake_emb = types.ModuleType("mlx_embeddings")
    fake_mx_core = types.ModuleType("mlx.core")

    class FakeTokenizer:
        def __call__(self, text, **kw):
            return {"input_ids": [list(range(len(text)))]}

    class FakeArray(list):
        def tolist(self):
            return list(self)

    class FakeOut:
        def __init__(self):
            self.text_embeds = [FakeArray([0.1, 0.2, 0.3]), FakeArray([0.4, 0.5, 0.6])]
            self.last_hidden_state = None

    def load(path):
        class FakeModel:
            def parameters(self): return []
        return FakeModel(), FakeTokenizer()

    def generate(model, tokenizer, texts):
        out = FakeOut()
        out.text_embeds = [FakeArray([0.1] * 3) for _ in texts]
        return out

    fake_emb.load = load
    fake_emb.generate = generate

    def eval_(*args, **kw):
        return None

    fake_mx_core.eval = eval_

    monkeypatch.setitem(sys.modules, "mlx_embeddings", fake_emb)
    monkeypatch.setitem(sys.modules, "mlx", types.ModuleType("mlx"))
    monkeypatch.setitem(sys.modules, "mlx.core", fake_mx_core)
    return fake_emb


def test_build_app_exposes_routes(fake_mlx_embeddings):
    from mlx_stack.embed_server import app as embed_app
    application = embed_app.build_app(model_path="/tmp/fake")
    paths = {r.path for r in application.routes}
    assert "/v1/embeddings" in paths
    assert "/v1/models" in paths


def test_embeddings_returns_openai_shape(fake_mlx_embeddings):
    from fastapi.testclient import TestClient
    from mlx_stack.embed_server import app as embed_app
    application = embed_app.build_app(model_path="/tmp/fake")
    client = TestClient(application)
    resp = client.post("/v1/embeddings", json={"input": ["hello", "world"], "model": "embed"})
    assert resp.status_code == 200
    body = resp.json()
    assert body["object"] == "list"
    assert len(body["data"]) == 2
    assert body["data"][0]["object"] == "embedding"
    assert isinstance(body["data"][0]["embedding"], list)
    assert body["data"][0]["index"] == 0
    assert "usage" in body


def test_embeddings_accepts_string_input(fake_mlx_embeddings):
    from fastapi.testclient import TestClient
    from mlx_stack.embed_server import app as embed_app
    application = embed_app.build_app(model_path="/tmp/fake")
    client = TestClient(application)
    resp = client.post("/v1/embeddings", json={"input": "hi", "model": "embed"})
    assert resp.status_code == 200
    assert len(resp.json()["data"]) == 1


def test_models_endpoint_returns_loaded_model(fake_mlx_embeddings):
    from fastapi.testclient import TestClient
    from mlx_stack.embed_server import app as embed_app
    application = embed_app.build_app(model_path="/tmp/fake-model")
    client = TestClient(application)
    resp = client.get("/v1/models")
    assert resp.status_code == 200
    data = resp.json()
    assert data["object"] == "list"
    assert data["data"][0]["id"] == "/tmp/fake-model"
