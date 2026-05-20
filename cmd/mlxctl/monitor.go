package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func newMonitorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "monitor",
		Short: "Live-refresh backend status",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newClient()
			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

			for {
				select {
				case <-stop:
					fmt.Println()
					return nil
				default:
				}

				b, err := c.Get(context.Background(), "/v1/status")
				if err != nil {
					return notRunning()
				}
				fmt.Print("\033[2J\033[H")
				fmt.Println("mlx-stack status (Ctrl-C to exit)")
				renderStatus(os.Stdout, b)
				select {
				case <-stop:
					fmt.Println()
					return nil
				case <-time.After(500 * time.Millisecond):
				}
			}
		},
	}
}
