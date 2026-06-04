package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/spf13/cobra"
)

// inactiveModels returns cache dir names not referenced as Model or DraftModel
// by any backend in cfg.
func inactiveModels(cfg *config.Config, cacheDirs []string) []string {
	active := map[string]bool{}
	for _, b := range cfg.Backends {
		if b.Model != "" {
			active[filepath.Base(b.Model)] = true
		}
		if b.DraftModel != "" {
			active[filepath.Base(b.DraftModel)] = true
		}
	}
	var out []string
	for _, d := range cacheDirs {
		if !active[d] {
			out = append(out, d)
		}
	}
	return out
}

// cacheDirNames returns the names of subdirectories under cfg.ModelsRoot.
func cacheDirNames(cfg *config.Config) ([]string, error) {
	if cfg == nil || cfg.ModelsRoot == "" {
		return nil, fmt.Errorf("models_root not set in config")
	}
	entries, err := os.ReadDir(cfg.ModelsRoot)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// postName POSTs {"name": <name>} to the admin path.
func postName(path, name string) error {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body, _ := json.Marshal(map[string]string{"name": name})
	_, err := c.PostJSON(cx, path, body)
	return err
}

func newOffloadCmd() *cobra.Command {
	var inactive bool
	cmd := &cobra.Command{
		Use:   "offload [model]",
		Short: "Move a model to the external library, freeing SSD cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			if inactive {
				cfg := loadCfg()
				dirs, err := cacheDirNames(cfg)
				if err != nil {
					return err
				}
				targets := inactiveModels(cfg, dirs)
				for _, name := range targets {
					if err := postName("/v1/offload", name); err != nil {
						return fmt.Errorf("offload %s: %w", name, err)
					}
					fmt.Println("offloaded", name)
				}
				if len(targets) == 0 {
					fmt.Println("nothing to offload (no inactive cached models)")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("usage: mlxctl offload <model> | --inactive")
			}
			if err := postName("/v1/offload", args[0]); err != nil {
				return err
			}
			return printStatus()
		},
	}
	cmd.Flags().BoolVar(&inactive, "inactive", false, "offload every cached model not referenced by the active config")
	return cmd
}

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <model>",
		Short: "Pre-warm a model from the external library into the SSD cache",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := postName("/v1/pull", args[0]); err != nil {
				return err
			}
			return printStatus()
		},
	}
}
