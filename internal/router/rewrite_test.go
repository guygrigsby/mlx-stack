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
