package supervisor

import (
	"context"
	"testing"
)

func TestExternal(t *testing.T) {
	e := NewExternal("embed", "http://other:1236", "/m/embed")
	if e.Name() != "embed" || e.Mode() != "external" {
		t.Errorf("name/mode: %q %q", e.Name(), e.Mode())
	}
	if e.BaseURL() != "http://other:1236" {
		t.Errorf("url: %q", e.BaseURL())
	}
	if e.UpstreamModel() != "/m/embed" {
		t.Errorf("upstream: %q", e.UpstreamModel())
	}
	if !e.Running() {
		t.Errorf("Running should be true")
	}
	if err := e.EnsureLoaded(context.Background(), "embed"); err != nil {
		t.Errorf("EnsureLoaded: %v", err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Errorf("Start: %v", err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
