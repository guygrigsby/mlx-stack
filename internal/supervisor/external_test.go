package supervisor

import "testing"

func TestExternalAdapter(t *testing.T) {
	a := NewExternalAdapter("embed", "http://other:1236", "/m/embed")
	if a.Alias() != "embed" {
		t.Errorf("alias: %q", a.Alias())
	}
	if a.BaseURL() != "http://other:1236" {
		t.Errorf("url: %q", a.BaseURL())
	}
	if a.UpstreamModel() != "/m/embed" {
		t.Errorf("upstream: %q", a.UpstreamModel())
	}
	if !a.Running() {
		t.Errorf("Running() should be true")
	}
}
