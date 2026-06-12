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
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newRootCmd builds the full command tree. Separate from main so tests can
// execute commands and inspect error behavior.
func newRootCmd() *cobra.Command {
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

  mlxctl status                  backends and which swap member is loaded
  mlxctl scan                    model checkpoints on disk (--add registers them)
  mlxctl add <path-or-hf-repo>   register a backend (downloads HF repos)
  mlxctl start <name>            load a backend or swap member (alias: swap)
  mlxctl chat "hello"            chat with the loaded chat model
  mlxctl run <model> "..."       run a prompt against any lm/vlm backend

mlxctl talks to mlxd over a unix socket (override with MLXD_SOCK) and to the
router over HTTP (override with MLXD_ROUTER). The config file defaults to
~/.config/mlx/config.toml; override with --config or MLXD_CONFIG (MLX_CONFIG
is honored for back-compat).`,
		SilenceUsage: true,
		// main() prints the error Execute returns; without this cobra prints
		// it too and every failure shows up twice.
		SilenceErrors: true,
	}

	// Output format is global so every command (and future ones) can honor
	// it. Default is the human-readable view; --output json emits the raw
	// daemon payload for scripting.
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text", `output format: "text" or "json"`)
	root.PersistentFlags().StringVar(&configFlag, "config", "", "config.toml path (default: $MLXD_CONFIG, $MLX_CONFIG, or ~/.config/mlx/config.toml)")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		switch outputFormat {
		case "text", "json":
			return nil
		default:
			return fmt.Errorf("invalid --output %q (want \"text\" or \"json\")", outputFormat)
		}
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
		newStatusCmd(),
		newHealthCmd(),
		newOffloadCmd(),
		newPullCmd(),
	)
	grouped(grpModels,
		newAddCmd(),
		newScanCmd(),
		newReloadCmd(),
		newChatCmd(),
		newRunCmd(),
		newModelsCmd(),
		newConfigCmd(),
		newBootstrapCmd(),
	)
	grouped(grpObserve,
		newMonitorCmd(),
		newTailCmd(),
	)
	return root
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

// outputFormat is the value of the global --output flag ("text" | "json").
var outputFormat string

func outputJSON() bool { return outputFormat == "json" }

// configFlag is the value of the global --config flag.
var configFlag string

// configPath resolves the config file every command uses, in precedence
// order: --config flag, MLXD_CONFIG, MLX_CONFIG (legacy), then the default
// ~/.config/mlx/config.toml.
func configPath() string {
	if configFlag != "" {
		return configFlag
	}
	if p := os.Getenv("MLXD_CONFIG"); p != "" {
		return p
	}
	if p := os.Getenv("MLX_CONFIG"); p != "" {
		return p
	}
	return defaultConfigPathLocal()
}

// printStatus prints the daemon's current status in the active output
// format: the human table by default, the raw /v1/status JSON under
// --output json. Lifecycle commands (start/restart/swap) use it so they
// show what changed instead of a bare {"ok":true}.
func printStatus() error {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	b, err := c.Get(cx, "/v1/status")
	if err != nil {
		return notRunning()
	}
	if outputJSON() {
		fmt.Println(string(b))
		return nil
	}
	renderStatus(os.Stdout, b)
	return nil
}
