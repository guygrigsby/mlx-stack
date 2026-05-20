package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

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
		Short: "Start or load a backend",
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
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _ := json.Marshal(map[string]string{"name": args[0]})
			resp, err := newClient().PostJSON(context.Background(), "/v1/stop", body)
			if err != nil {
				return fmt.Errorf("stop failed: %v\n%s", err, resp)
			}
			fmt.Println(string(resp))
			return nil
		},
	}
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
