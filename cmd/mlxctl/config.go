package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Config helpers",
	}
	cmd.AddCommand(newConfigShowCmd())
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	var resolved bool
	cmd := &cobra.Command{
		Use:   "show [path]",
		Short: "Print the current TOML config",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := os.ExpandEnv("$HOME/.config/mlx/config.toml")
			if len(args) > 0 {
				path = args[0]
			}

			if !resolved {
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				fmt.Print(string(b))
				return nil
			}

			c, err := config.Load(path)
			if err != nil {
				return err
			}
			return toml.NewEncoder(os.Stdout).Encode(c)
		},
	}
	cmd.Flags().BoolVar(&resolved, "resolved", false, "load + re-encode with paths expanded")
	return cmd
}
