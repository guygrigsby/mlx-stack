package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func cmdTail(args []string) {
	fs := flag.NewFlagSet("tail", flag.ExitOnError)
	worker := fs.String("worker", "", "filter to events from a single worker (e.g. qwen-tags or chat[valkyrie])")
	fs.Parse(args)

	c := newClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		cancel()
	}()

	path := "/v1/logs/tail"
	if *worker != "" {
		path += "?worker=" + url.QueryEscape(*worker)
	}

	rc, err := c.GetStream(ctx, path)
	if err != nil {
		notRunning()
	}
	defer rc.Close()

	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			fmt.Println(strings.TrimPrefix(line, "data: "))
		}
	}
}
