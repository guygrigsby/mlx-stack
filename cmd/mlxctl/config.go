package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	cmd.AddCommand(newConfigMigrateCmd())
	return cmd
}

var (
	reMode  = regexp.MustCompile(`^(\s*)mode\s*=\s*["'](\w+)["']\s*$`)
	reGroup = regexp.MustCompile(`^(\s*)group(\s*)=`)
)

// migrateConfigLines rewrites legacy mode/group lines to the slot vocabulary.
// It is line-local on purpose: comments, sub-tables, and every other field are
// preserved byte-for-byte. mode="swap" drops (lazy slot is the default),
// "persistent"->warm, "external"->remote, and the group key becomes slot.
func migrateConfigLines(src string) (string, bool) {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for _, l := range lines {
		if m := reMode.FindStringSubmatch(l); m != nil {
			switch indent, val := m[1], m[2]; val {
			case "swap":
				changed = true
				continue
			case "persistent":
				out = append(out, indent+"warm   = true")
				changed = true
				continue
			case "external":
				out = append(out, indent+"remote = true")
				changed = true
				continue
			}
			out = append(out, l) // unknown mode value: leave it
			continue
		}
		if reGroup.MatchString(l) {
			out = append(out, reGroup.ReplaceAllString(l, "${1}slot${2}="))
			changed = true
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n"), changed
}

func newConfigMigrateCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "migrate [path]",
		Short: "Rewrite legacy mode/group to the slot vocabulary (writes <path>.bak)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := configPath()
			if len(args) > 0 {
				path = args[0]
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out, changed := migrateConfigLines(string(src))
			if !changed {
				fmt.Println("already on the slot vocabulary; nothing to migrate")
				return nil
			}
			// Don't trust the transform blindly: the rewritten config must still
			// load before anything touches the real file.
			tmp, err := os.CreateTemp(filepath.Dir(path), ".mlxctl-migrate-*.toml")
			if err != nil {
				return err
			}
			tmpPath := tmp.Name()
			defer os.Remove(tmpPath)
			if _, err := tmp.WriteString(out); err != nil {
				tmp.Close()
				return err
			}
			tmp.Close()
			if _, err := config.Load(tmpPath); err != nil {
				return fmt.Errorf("migrated config does not load (left %s untouched): %w", path, err)
			}
			if dryRun {
				fmt.Print(out)
				return nil
			}
			if err := os.WriteFile(path+".bak", src, 0o644); err != nil {
				return fmt.Errorf("write backup: %w", err)
			}
			if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
				return err
			}
			fmt.Printf("migrated %s (backup: %s.bak)\n", path, path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the migrated config without writing")
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	var resolved bool
	cmd := &cobra.Command{
		Use:   "show [path]",
		Short: "Print the current TOML config",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Positional path wins for back-compat; otherwise the same
			// resolution every command uses (--config, env, default).
			path := configPath()
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
