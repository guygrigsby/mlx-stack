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
		Use:          "mlxctl",
		Short:        "Control mlxd: status, swap models, send requests, manage backends",
		SilenceUsage: true,
	}
	root.AddCommand(
		newStatusCmd(),
		newListCmd(),
		newHealthCmd(),
		newMonitorCmd(),
		newTailCmd(),
		newStartCmd(),
		newStopCmd(),
		newRestartCmd(),
		newSwapCmd(),
		newChatCmd(),
		newTagsCmd(),
		newConfigCmd(),
		newBootstrapCmd(),
		newAddCmd(),
		newScanCmd(),
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
