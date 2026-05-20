package supervisor

import "context"

// External is a Backend impl for a URL-only proxy target. No process owned.
type External struct {
	NameValue          string
	URLValue           string
	UpstreamModelValue string
}

func NewExternal(name, url, upstreamModel string) *External {
	return &External{
		NameValue:          name,
		URLValue:           url,
		UpstreamModelValue: upstreamModel,
	}
}

func (e *External) Name() string          { return e.NameValue }
func (e *External) Group() string         { return e.NameValue }
func (e *External) Mode() string          { return "external" }
func (e *External) Engine() string        { return "" }
func (e *External) BaseURL() string       { return e.URLValue }
func (e *External) UpstreamModel() string { return e.UpstreamModelValue }
func (e *External) Running() bool         { return true }
func (e *External) PID() int              { return 0 }

func (e *External) EnsureLoaded(_ context.Context, _ string) error { return nil }
func (e *External) Start(_ context.Context) error                  { return nil }
func (e *External) Stop(_ context.Context) error                   { return nil }
