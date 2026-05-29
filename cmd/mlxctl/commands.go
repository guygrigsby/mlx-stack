package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"github.com/guygrigsby/mlx-stack/internal/ipc"
	"github.com/spf13/cobra"
)

// reloadResult mirrors admin.ReloadResult.
type reloadResult struct {
	Added   []string `json:"added"`
	Skipped []string `json:"skipped"`
}

// callReload asks mlxd to re-read the config and register newly added
// backends. daemonDown is true when mlxd is not reachable (socket missing or
// connection refused), letting callers degrade gracefully instead of failing.
func callReload(ctx context.Context) (res reloadResult, daemonDown bool, err error) {
	resp, err := newClient().PostJSON(ctx, "/v1/reload", nil)
	if err != nil {
		if isDaemonDown(err) {
			return res, true, nil
		}
		return res, false, fmt.Errorf("reload failed: %v\n%s", err, resp)
	}
	_ = json.Unmarshal(resp, &res)
	return res, false, nil
}

// isDaemonDown reports whether err means mlxd isn't running (no socket file or
// nothing listening), as opposed to a real reload error.
func isDaemonDown(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, fs.ErrNotExist)
}

func printReload(res reloadResult) {
	if len(res.Added) == 0 {
		fmt.Println("reloaded mlxd (no new backends)")
		return
	}
	fmt.Printf("reloaded mlxd (added: %s)\n", strings.Join(res.Added, ", "))
}

func newReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Re-read the config and register newly added backends (no restart; additive only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			res, down, err := callReload(cx)
			if err != nil {
				return err
			}
			if down {
				return notRunning()
			}
			printReload(res)
			return nil
		},
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show all backend state",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newClient()
			cx, cancel := ctx()
			defer cancel()
			b, err := c.Get(cx, "/v1/status")
			if err != nil {
				return notRunning()
			}
			renderStatus(os.Stdout, b)
			return nil
		},
	}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all configured backends (alias for status)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newClient()
			cx, cancel := ctx()
			defer cancel()
			b, err := c.Get(cx, "/v1/status")
			if err != nil {
				return notRunning()
			}
			renderStatus(os.Stdout, b)
			return nil
		},
	}
}

func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Daemon liveness check",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newClient()
			cx, cancel := ctx()
			defer cancel()
			b, err := c.Get(cx, "/v1/health")
			if err != nil {
				return notRunning()
			}
			fmt.Println(string(b))
			return nil
		},
	}
}

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Start or load a backend (a group name loads its default member, e.g. 'start chat')",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _ := json.Marshal(map[string]string{"name": args[0]})
			resp, err := newClient().PostJSON(context.Background(), "/v1/start", body)
			if err != nil {
				return fmt.Errorf("start failed: %v\n%s", err, resp)
			}
			fmt.Println(string(resp))
			return nil
		},
	}
}

func newStopCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "stop <name>...",
		Short: "Stop one or more backends (or all running ones with --all)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) > 0 {
					return fmt.Errorf("stop: pass names or --all, not both")
				}
				return stopAll()
			}
			if len(args) == 0 {
				return fmt.Errorf("stop: requires at least one backend name (or --all)")
			}

			c := newClient()
			cx, cancel := ctx()
			defer cancel()

			failed := 0
			for _, name := range args {
				if resp, err := stopBackend(cx, c, name); err != nil {
					failed++
					fmt.Printf("stop %s: failed: %v\n%s\n", name, err, resp)
					continue
				}
				fmt.Printf("stopped %s\n", name)
			}
			if failed > 0 {
				return fmt.Errorf("%d backend(s) failed to stop", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "stop every running backend (skips external backends)")
	return cmd
}

// stopBackend POSTs a stop request for one backend by name.
func stopBackend(cx context.Context, c *ipc.Client, name string) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	return c.PostJSON(cx, "/v1/stop", body)
}

// stopAll stops every running, locally-managed backend. External backends are
// remote and have no process to stop, so they're skipped. It tries every
// backend even if one fails, then reports any failures.
func stopAll() error {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()

	body, err := c.Get(cx, "/v1/status")
	if err != nil {
		return notRunning()
	}
	var s statusJSON
	if err := json.Unmarshal(body, &s); err != nil {
		return fmt.Errorf("parse status: %w", err)
	}

	stopped, failed := 0, 0
	for _, b := range s.Backends {
		if !b.Running || b.Mode == "external" {
			continue
		}
		if resp, err := stopBackend(cx, c, b.Name); err != nil {
			failed++
			fmt.Printf("stop %s: failed: %v\n%s\n", b.Name, err, resp)
			continue
		}
		stopped++
		fmt.Printf("stopped %s\n", b.Name)
	}

	if stopped == 0 && failed == 0 {
		fmt.Println("no running backends")
	}
	if failed > 0 {
		return fmt.Errorf("%d backend(s) failed to stop", failed)
	}
	return nil
}

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name>",
		Short: "Stop then start a backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _ := json.Marshal(map[string]string{"name": args[0]})
			resp, err := newClient().PostJSON(context.Background(), "/v1/restart", body)
			if err != nil {
				return fmt.Errorf("restart failed: %v\n%s", err, resp)
			}
			fmt.Println(string(resp))
			return nil
		},
	}
}

func newSwapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "swap <name>",
		Short: "Alias for start",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _ := json.Marshal(map[string]string{"name": args[0]})
			resp, err := newClient().PostJSON(context.Background(), "/v1/swap", body)
			if err != nil {
				return fmt.Errorf("swap failed: %v\n%s", err, resp)
			}
			fmt.Println(string(resp))
			return nil
		},
	}
}
