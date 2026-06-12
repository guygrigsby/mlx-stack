package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	renderReload(os.Stdout, res, outputJSON())
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
		Use:     "status",
		Aliases: []string{"list"},
		Short:   "Show all backend state",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printStatus()
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
			renderHealth(os.Stdout, b, outputJSON())
			return nil
		},
	}
}

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "start <name>",
		Aliases: []string{"swap"},
		Short:   "Start or load a backend; a swap member evicts its group's current one (group name loads the default member)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			body, _ := json.Marshal(map[string]string{"name": args[0]})
			resp, err := newClient().PostJSON(cx, "/v1/start", body)
			if err != nil {
				return fmt.Errorf("start failed: %v\n%s", err, resp)
			}
			return printStatus()
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

			var stopped, failed []string
			for _, name := range args {
				if resp, err := stopBackend(cx, c, name); err != nil {
					failed = append(failed, name)
					fmt.Fprintf(os.Stderr, "stop %s: failed: %v\n%s\n", name, err, resp)
					continue
				}
				stopped = append(stopped, name)
				if !outputJSON() {
					fmt.Printf("stopped %s\n", name)
				}
			}
			renderStopResult(os.Stdout, stopped, failed, outputJSON())
			if len(failed) > 0 {
				return fmt.Errorf("%d backend(s) failed to stop", len(failed))
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

	var stopped, failed []string
	for _, b := range s.Backends {
		if !b.Running || b.Mode == "external" {
			continue
		}
		if resp, err := stopBackend(cx, c, b.Name); err != nil {
			failed = append(failed, b.Name)
			fmt.Fprintf(os.Stderr, "stop %s: failed: %v\n%s\n", b.Name, err, resp)
			continue
		}
		stopped = append(stopped, b.Name)
		if !outputJSON() {
			fmt.Printf("stopped %s\n", b.Name)
		}
	}

	if !outputJSON() && len(stopped) == 0 && len(failed) == 0 {
		fmt.Println("no running backends")
	}
	renderStopResult(os.Stdout, stopped, failed, outputJSON())
	if len(failed) > 0 {
		return fmt.Errorf("%d backend(s) failed to stop", len(failed))
	}
	return nil
}

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name>",
		Short: "Stop then start a backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			body, _ := json.Marshal(map[string]string{"name": args[0]})
			resp, err := newClient().PostJSON(cx, "/v1/restart", body)
			if err != nil {
				return fmt.Errorf("restart failed: %v\n%s", err, resp)
			}
			return printStatus()
		},
	}
}
