package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func TestImageToURL_HTTPPassthrough(t *testing.T) {
	for _, u := range []string{"http://x/y.png", "https://x/y.jpg"} {
		got, err := imageToURL(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		if got != u {
			t.Errorf("http url should pass through unchanged, got %q", got)
		}
	}
}

func TestImageToURL_LocalFileToDataURL(t *testing.T) {
	dir := t.TempDir()
	// PNG magic so http.DetectContentType reports image/png.
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	p := filepath.Join(dir, "x.png")
	if err := os.WriteFile(p, png, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := imageToURL(p)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("want %q prefix, got %q", prefix, got)
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if string(dec) != string(png) {
		t.Error("decoded base64 does not match the file bytes")
	}
}

func TestImageToURL_MissingFile(t *testing.T) {
	if _, err := imageToURL("/no/such/file.png"); err == nil {
		t.Fatal("want an error for a missing image file")
	}
}

func TestBuildContent_NoImagesReturnsString(t *testing.T) {
	c, err := buildContent("hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := c.(string); !ok || s != "hello" {
		t.Fatalf("no images: want raw string content, got %T %v", c, c)
	}
}

func TestBuildContent_WithImages(t *testing.T) {
	c, err := buildContent("compare", []string{"https://a/x.png", "https://b/y.png"})
	if err != nil {
		t.Fatal(err)
	}
	arr, ok := c.([]map[string]any)
	if !ok {
		t.Fatalf("with images: want array content, got %T", c)
	}
	if len(arr) != 3 {
		t.Fatalf("want text + 2 image parts, got %d", len(arr))
	}
	if arr[0]["type"] != "text" || arr[0]["text"] != "compare" {
		t.Errorf("first part should be the text prompt, got %v", arr[0])
	}
	if arr[1]["type"] != "image_url" || arr[2]["type"] != "image_url" {
		t.Errorf("image parts malformed: %v", arr[1:])
	}
}

func TestBuildChatPayload_Defaults(t *testing.T) {
	p := buildChatPayload("coder", "hi", true, nil)
	if p["model"] != "coder" {
		t.Errorf("model = %v", p["model"])
	}
	if p["stream"] != true {
		t.Errorf("stream = %v", p["stream"])
	}
	if p["max_tokens"] != 512 {
		t.Errorf("default max_tokens = %v, want 512", p["max_tokens"])
	}
}

func TestBuildChatPayload_SamplerApplied(t *testing.T) {
	p := buildChatPayload("coder", "hi", false, &config.Sampler{Temperature: 0.7, MaxTokens: 100})
	if p["temperature"] != 0.7 {
		t.Errorf("temperature = %v", p["temperature"])
	}
	if p["max_tokens"] != 100 {
		t.Errorf("sampler max_tokens should override the default, got %v", p["max_tokens"])
	}
}

func TestRunModelEngine_ByNameAndGroup(t *testing.T) {
	cfg := &config.Config{Backends: []config.BackendSpec{
		{Name: "coder", Engine: "lm", Mode: "persistent"},
		{Name: "diffusiongemma", Engine: "vlm", Mode: "swap", Group: "exp"},
		{Name: "kokoro", Engine: "audio", Mode: "persistent"},
	}}
	if eng, ok := runModelEngine(cfg, "coder"); !ok || eng != "lm" {
		t.Errorf("by name: got %q %v, want lm true", eng, ok)
	}
	if eng, ok := runModelEngine(cfg, "exp"); !ok || eng != "vlm" {
		t.Errorf("by group: got %q %v, want vlm true", eng, ok)
	}
	if _, ok := runModelEngine(cfg, "nope"); ok {
		t.Error("unknown model should not resolve")
	}
}

func TestCheckRunEngine(t *testing.T) {
	for _, e := range []string{"lm", "vlm"} {
		if err := checkRunEngine(e); err != nil {
			t.Errorf("%s should be supported by run: %v", e, err)
		}
	}
	for _, e := range []string{"audio", "embed"} {
		if err := checkRunEngine(e); err == nil {
			t.Errorf("%s should be rejected by run", e)
		}
	}
}
