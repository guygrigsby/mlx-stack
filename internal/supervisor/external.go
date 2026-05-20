package supervisor

import "github.com/guygrigsby/mlx-stack/internal/backend"

// ExternalAdapter wraps *backend.External so it satisfies router.ManagedBackend.
// External backends have a fixed URL and upstream model; we always report
// Running()=true (Phase 3 doesn't health-probe externals yet).
type ExternalAdapter struct {
	*backend.External
}

func NewExternalAdapter(alias, baseURL, upstreamName string) *ExternalAdapter {
	return &ExternalAdapter{
		External: &backend.External{
			AliasName:    alias,
			BaseURL:      baseURL,
			UpstreamName: upstreamName,
		},
	}
}

func (e *ExternalAdapter) Alias() string         { return e.External.AliasName }
func (e *ExternalAdapter) BaseURL() string       { return e.External.BaseURL }
func (e *ExternalAdapter) UpstreamModel() string { return e.External.UpstreamName }
func (e *ExternalAdapter) Running() bool         { return true }
