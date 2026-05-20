package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/guygrigsby/mlx-stack/internal/config"
)

func cmdConfig(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl config show [--resolved] [path]")
		os.Exit(2)
	}
	switch args[0] {
	case "show":
		cmdConfigShow(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown: mlxctl config %s\n", args[0])
		os.Exit(2)
	}
}

func cmdConfigShow(args []string) {
	path := os.ExpandEnv("$HOME/.config/mlx/config.toml")
	resolved := false

	for _, a := range args {
		if a == "--resolved" {
			resolved = true
		} else if !strings.HasPrefix(a, "-") {
			path = a
		}
	}

	if !resolved {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(string(b))
		return
	}

	c, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := toml.NewEncoder(os.Stdout).Encode(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
