package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/spf13/cobra"
)

// resolveModelName maps a backend name (as shown in `mlxctl status`, e.g. a
// normalized/lowercased HF repo id) to its on-disk model directory basename,
// which is what offload/pull key on. Backend names and dir names often differ
// (case, dots vs dashes). An arg that matches no backend passes through
// unchanged, so an exact dir name still works and an unknown name reaches the
// server (which errors).
func resolveModelName(cfg *config.Config, arg string) string {
	if cfg == nil {
		return arg
	}
	for _, b := range cfg.Backends {
		if strings.EqualFold(b.Name, arg) && b.Model != "" {
			return filepath.Base(b.Model)
		}
	}
	return arg
}

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
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if inactive {
				if len(args) > 0 {
					return fmt.Errorf("offload: pass a model or --inactive, not both")
				}
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
			if len(args) == 0 {
				return fmt.Errorf("offload: requires a model name (or --inactive)")
			}
			name := resolveModelName(loadCfg(), args[0])
			if err := postName("/v1/offload", name); err != nil {
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
			name := resolveModelName(loadCfg(), args[0])
			if err := postName("/v1/pull", name); err != nil {
				return err
			}
			return printStatus()
		},
	}
}
