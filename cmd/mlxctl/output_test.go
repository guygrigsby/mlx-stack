package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderHealth(t *testing.T) {
	body := []byte(`{"ok":true}`)
	var buf bytes.Buffer
	renderHealth(&buf, body, false)
	if got := strings.TrimSpace(buf.String()); got != "ok" {
		t.Errorf("text mode: want ok, got %q", got)
	}
	buf.Reset()
	renderHealth(&buf, body, true)
	if got := strings.TrimSpace(buf.String()); got != `{"ok":true}` {
		t.Errorf("json mode: want raw body, got %q", got)
	}
}

func TestRenderModelIDs(t *testing.T) {
	body := []byte(`{"data":[{"id":"chat"},{"id":"exp"}]}`)
	var buf bytes.Buffer
	renderModelIDs(&buf, body, false)
	if got := buf.String(); got != "chat\nexp\n" {
		t.Errorf("text mode: got %q", got)
	}
	buf.Reset()
	renderModelIDs(&buf, body, true)
	if got := strings.TrimSpace(buf.String()); got != `{"data":[{"id":"chat"},{"id":"exp"}]}` {
		t.Errorf("json mode: want raw body, got %q", got)
	}
}

func TestRenderReload(t *testing.T) {
	res := reloadResult{Added: []string{"a"}, Skipped: []string{"b"}}
	var buf bytes.Buffer
	renderReload(&buf, res, false)
	if !strings.Contains(buf.String(), "added: a") {
		t.Errorf("text mode: got %q", buf.String())
	}
	buf.Reset()
	renderReload(&buf, res, true)
	var parsed reloadResult
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json mode should emit valid JSON: %v\n%s", err, buf.String())
	}
	if len(parsed.Added) != 1 || parsed.Added[0] != "a" {
		t.Errorf("json mode: got %+v", parsed)
	}
}

func TestRenderStopResult(t *testing.T) {
	var buf bytes.Buffer
	renderStopResult(&buf, []string{"x"}, []string{"y"}, true)
	var parsed struct {
		Stopped []string `json:"stopped"`
		Failed  []string `json:"failed"`
	}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(parsed.Stopped) != 1 || len(parsed.Failed) != 1 {
		t.Errorf("got %+v", parsed)
	}
	// Empty slices must encode as [], not null, for stable consumers.
	buf.Reset()
	renderStopResult(&buf, nil, nil, true)
	if s := strings.TrimSpace(buf.String()); s != `{"stopped":[],"failed":[]}` {
		t.Errorf("empty: got %s", s)
	}
}

func TestRenderScanJSON(t *testing.T) {
	cands := []scanCandidate{{Path: "/m/a", Name: "a", Engine: "lm", InConfig: true}}
	var buf bytes.Buffer
	renderScanJSON(&buf, cands)
	var parsed []scanCandidate
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(parsed) != 1 || parsed[0].Name != "a" || !parsed[0].InConfig {
		t.Errorf("got %+v", parsed)
	}
}
