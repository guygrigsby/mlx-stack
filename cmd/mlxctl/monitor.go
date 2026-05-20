package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func cmdMonitor(args []string) {
	c := newClient()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-stop:
			fmt.Println()
			return
		default:
		}

		b, err := c.Get(context.Background(), "/v1/status")
		if err != nil {
			notRunning()
		}
		fmt.Print("\033[2J\033[H")
		fmt.Println("mlx-stack status (Ctrl-C to exit)")
		renderStatus(os.Stdout, b)
		select {
		case <-stop:
			fmt.Println()
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}
