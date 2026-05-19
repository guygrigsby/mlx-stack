package supervisor

import (
	"github.com/guygrigsby/mlx-stack/internal/admin"
	"github.com/guygrigsby/mlx-stack/internal/router"
)

var (
	_ router.ChatSwapper   = (*ChatSwap)(nil)
	_ admin.ChatController = (*ChatSwap)(nil)
)
