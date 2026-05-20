package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// newBootstrapCmd creates a fresh Python venv and installs the MLX packages
// mlxd workers need (mlx, mlx_lm, mlx_vlm, mlx_embeddings, mlx_audio).
// Convenience for fresh machines; existing users with an MLX venv should
// just point python_bin at it in config.toml.
func newBootstrapCmd() *cobra.Command {
	var (
		venvPath  string
		pythonBin string
		upgrade   bool
		skipMlx   bool
	)
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Create a venv with mlx_lm + friends (fresh machines)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !upgrade {
				if _, err := os.Stat(venvPath); err == nil {
					return fmt.Errorf("venv already exists at %s. Use --upgrade to reuse, or --path to pick another location", venvPath)
				}
			}

			if _, err := os.Stat(venvPath); err != nil {
				fmt.Printf("creating venv at %s\n", venvPath)
				create := exec.Command(pythonBin, "-m", "venv", venvPath)
				create.Stdout = os.Stdout
				create.Stderr = os.Stderr
				if err := create.Run(); err != nil {
					return fmt.Errorf("venv create failed: %v", err)
				}
			}

			pip := filepath.Join(venvPath, "bin", "pip")
			if _, err := os.Stat(pip); err != nil {
				return fmt.Errorf("pip not found at %s (venv broken?)", pip)
			}

			// Bump pip itself for clean wheel installs.
			fmt.Println("upgrading pip")
			up := exec.Command(pip, "install", "--upgrade", "pip", "wheel")
			up.Stdout = os.Stdout
			up.Stderr = os.Stderr
			_ = up.Run()

			if !skipMlx {
				// Note: mlx_lm pulls in mlx as a transitive dep, but listing it explicitly
				// makes failures easier to diagnose if a future release breaks the chain.
				pkgs := []string{"mlx", "mlx-lm", "mlx-vlm", "mlx-embeddings", "mlx-audio"}
				fmt.Printf("installing: %s\n", strings.Join(pkgs, " "))
				install := exec.Command(pip, append([]string{"install"}, pkgs...)...)
				install.Stdout = os.Stdout
				install.Stderr = os.Stderr
				if err := install.Run(); err != nil {
					msg := fmt.Sprintf("\npip install failed: %v\nRetry individual packages to narrow it down:\n", err)
					for _, p := range pkgs {
						msg += fmt.Sprintf("    %s install %s\n", pip, p)
					}
					return fmt.Errorf("%s", msg)
				}
			}

			pythonExe := filepath.Join(venvPath, "bin", "python")
			fmt.Printf("\nDone. Set this in your config.toml:\n    python_bin = %q\n", pythonExe)
			return nil
		},
	}
	defaultVenv := filepath.Join(os.Getenv("HOME"), "venvs", "mlx")
	cmd.Flags().StringVar(&venvPath, "path", defaultVenv, "venv path to create")
	cmd.Flags().StringVar(&pythonBin, "python", "python3", "interpreter used to create the venv")
	cmd.Flags().BoolVar(&upgrade, "upgrade", false, "skip the existing-venv check and upgrade packages in-place")
	cmd.Flags().BoolVar(&skipMlx, "skip-mlx", false, "create the venv but don't install MLX packages (test runs)")
	return cmd
}
