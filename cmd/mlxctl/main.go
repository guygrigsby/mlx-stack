package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/ipc"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "mlxctl",
		Short: "Control mlxd: status, swap models, send requests, manage backends",
		Long: `mlxctl controls mlxd, the local MLX model daemon.

mlxd runs models as backends in one of two modes:

  swap        members of a group share one port; only one is resident at a
              time. Loading another member evicts the current one. Chat models
              live in the "chat" group.
  persistent  always-on, each on its own port (embeddings, audio, a dedicated
              coder, and so on).

Common workflow:

  mlxctl list                    backends and which swap member is loaded
  mlxctl scan                    model checkpoints on disk (--add registers them)
  mlxctl add <path-or-hf-repo>   register a backend (downloads HF repos)
  mlxctl swap <name>             load a swap member, evicting the current one
  mlxctl chat "hello"            chat with the loaded chat model

mlxctl talks to mlxd over a unix socket (override with MLXD_SOCK) and to the
router over HTTP (override with MLXD_ROUTER).`,
		SilenceUsage: true,
	}

	const (
		grpLifecycle = "lifecycle"
		grpModels    = "models"
		grpObserve   = "observe"
	)
	root.AddGroup(
		&cobra.Group{ID: grpLifecycle, Title: "Backend lifecycle:"},
		&cobra.Group{ID: grpModels, Title: "Models & config:"},
		&cobra.Group{ID: grpObserve, Title: "Observability:"},
	)

	grouped := func(id string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = id
			root.AddCommand(c)
		}
	}
	grouped(grpLifecycle,
		newStartCmd(),
		newStopCmd(),
		newRestartCmd(),
		newSwapCmd(),
		newStatusCmd(),
		newListCmd(),
		newHealthCmd(),
	)
	grouped(grpModels,
		newAddCmd(),
		newScanCmd(),
		newReloadCmd(),
		newChatCmd(),
		newTagsCmd(),
		newConfigCmd(),
		newBootstrapCmd(),
	)
	grouped(grpObserve,
		newMonitorCmd(),
		newTailCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newClient() *ipc.Client {
	sock := os.Getenv("MLXD_SOCK")
	if sock == "" {
		home, _ := os.UserHomeDir()
		sock = filepath.Join(home, ".local", "state", "mlxd", "admin.sock")
	}
	return ipc.New(sock)
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 120*time.Second)
}

func notRunning() error {
	return fmt.Errorf("mlxd is not running. Start with: mlxd run")
}
