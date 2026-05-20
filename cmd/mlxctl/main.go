package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/ipc"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "status":
		cmdStatus(os.Args[2:])
	case "swap":
		cmdSwap(os.Args[2:])
	case "start":
		cmdStart(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "restart":
		cmdRestart(os.Args[2:])
	case "health":
		cmdHealth(os.Args[2:])
	case "monitor":
		cmdMonitor(os.Args[2:])
	case "tail":
		cmdTail(os.Args[2:])
	case "chat":
		cmdChat(os.Args[2:])
	case "tags":
		cmdTagsList(os.Args[2:])
	case "config":
		cmdConfig(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: mlxctl <subcommand>
  status                       show current backend state
  monitor                      live-refresh status (every 500ms)
  tail                         stream structured stderr events from all workers
  swap <profile>               swap chat profile
  start chat                   start chat backend (default profile)
  stop chat                    stop chat backend
  restart chat                 restart chat backend
  chat "..."                   send a chat request via the router
  tags                         list available models
  health                       daemon liveness check
  config migrate [src]         migrate ~/.config/mlx.conf to TOML on stdout
  config show [path]           print config file (--resolved: expand + validate)`)
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

func notRunning() {
	fmt.Fprintln(os.Stderr, "mlxd is not running. Start with: `mlxd run` or `launchctl load …`")
	os.Exit(2)
}

func cmdHealth(args []string) {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	b, err := c.Get(cx, "/v1/health")
	if err != nil {
		notRunning()
	}
	fmt.Println(string(b))
}

func cmdStatus(args []string) {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	b, err := c.Get(cx, "/v1/status")
	if err != nil {
		notRunning()
	}
	var parsed map[string]any
	json.Unmarshal(b, &parsed)
	pretty, _ := json.MarshalIndent(parsed, "", "  ")
	fmt.Println(string(pretty))
}

func cmdSwap(args []string) {
	fs := flag.NewFlagSet("swap", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl swap <profile>")
		os.Exit(2)
	}
	profile := fs.Arg(0)
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body, _ := json.Marshal(map[string]string{"profile": profile})
	resp, err := c.PostJSON(cx, "/v1/swap", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swap failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}

func cmdStart(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl start <backend>")
		os.Exit(2)
	}
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body, _ := json.Marshal(map[string]string{"backend": args[0]})
	resp, err := c.PostJSON(cx, "/v1/start", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}

func cmdStop(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl stop <backend>")
		os.Exit(2)
	}
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body, _ := json.Marshal(map[string]string{"backend": args[0]})
	resp, err := c.PostJSON(cx, "/v1/stop", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stop failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}

func cmdRestart(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl restart <backend>")
		os.Exit(2)
	}
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body, _ := json.Marshal(map[string]string{"backend": args[0]})
	resp, err := c.PostJSON(cx, "/v1/restart", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restart failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}
