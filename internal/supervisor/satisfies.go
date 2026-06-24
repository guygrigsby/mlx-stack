package supervisor

import (
	"github.com/guygrigsby/mlx-stack/internal/backend"
)

var (
	_ backend.Backend = (*Group)(nil)
	_ backend.Backend = (*External)(nil)
)
