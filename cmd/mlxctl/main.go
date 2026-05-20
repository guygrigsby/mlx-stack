package main

import (
	"context"
	"encoding/json"
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
	case "list":
		cmdStatus(os.Args[2:])
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
	case "bootstrap":
		cmdBootstrap(os.Args[2:])
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
  list                      list all configured backends
  status                    show all backend state
  start <name>              start/load a backend
  stop <name>               stop a backend
  restart <name>            stop then start
  swap <name>               alias for start
  monitor                   live-refresh status
  tail [--worker name]      stream stderr events (optional worker filter)
  chat "..."                send a chat request via the router
  tags                      list available models
  health                    daemon liveness
  config migrate|show       config helpers
  bootstrap [--path P]      create a venv with mlx_lm + friends (fresh machines)`)
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
	fmt.Fprintln(os.Stderr, "mlxd is not running. Start with: `mlxd run` or `launchctl load ...`")
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
	renderStatus(os.Stdout, b)
}

func cmdSwap(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl swap <name>")
		os.Exit(2)
	}
	body, _ := json.Marshal(map[string]string{"name": args[0]})
	resp, err := newClient().PostJSON(context.Background(), "/v1/swap", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swap failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}

func cmdStart(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl start <name>")
		os.Exit(2)
	}
	body, _ := json.Marshal(map[string]string{"name": args[0]})
	resp, err := newClient().PostJSON(context.Background(), "/v1/start", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}

func cmdStop(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl stop <name>")
		os.Exit(2)
	}
	body, _ := json.Marshal(map[string]string{"name": args[0]})
	resp, err := newClient().PostJSON(context.Background(), "/v1/stop", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stop failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}

func cmdRestart(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl restart <name>")
		os.Exit(2)
	}
	body, _ := json.Marshal(map[string]string{"name": args[0]})
	resp, err := newClient().PostJSON(context.Background(), "/v1/restart", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restart failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}
