package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cmdBootstrap creates a fresh Python venv and installs the MLX packages
// mlxd workers need (mlx, mlx_lm, mlx_vlm, mlx_embeddings, mlx_audio).
// Convenience for fresh machines; existing users with an MLX venv should
// just point python_bin at it in config.toml.
func cmdBootstrap(args []string) {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	venvPath := fs.String("path", filepath.Join(os.Getenv("HOME"), "venvs", "mlx"), "venv path to create")
	pythonBin := fs.String("python", "python3", "interpreter used to create the venv")
	upgrade := fs.Bool("upgrade", false, "skip the existing-venv check and upgrade packages in-place")
	skipMlx := fs.Bool("skip-mlx", false, "create the venv but don't install MLX packages (test runs)")
	fs.Parse(args)

	if !*upgrade {
		if _, err := os.Stat(*venvPath); err == nil {
			fmt.Fprintf(os.Stderr, "venv already exists at %s. Use --upgrade to reuse, or --path to pick another location.\n", *venvPath)
			os.Exit(1)
		}
	}

	if _, err := os.Stat(*venvPath); err != nil {
		fmt.Printf("creating venv at %s\n", *venvPath)
		create := exec.Command(*pythonBin, "-m", "venv", *venvPath)
		create.Stdout = os.Stdout
		create.Stderr = os.Stderr
		if err := create.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "venv create failed: %v\n", err)
			os.Exit(1)
		}
	}

	pip := filepath.Join(*venvPath, "bin", "pip")
	if _, err := os.Stat(pip); err != nil {
		fmt.Fprintf(os.Stderr, "pip not found at %s (venv broken?)\n", pip)
		os.Exit(1)
	}

	// Bump pip itself for clean wheel installs.
	fmt.Println("upgrading pip")
	up := exec.Command(pip, "install", "--upgrade", "pip", "wheel")
	up.Stdout = os.Stdout
	up.Stderr = os.Stderr
	_ = up.Run()

	if !*skipMlx {
		// Note: mlx_lm pulls in mlx as a transitive dep, but listing it explicitly
		// makes failures easier to diagnose if a future release breaks the chain.
		pkgs := []string{"mlx", "mlx-lm", "mlx-vlm", "mlx-embeddings", "mlx-audio"}
		fmt.Printf("installing: %s\n", strings.Join(pkgs, " "))
		install := exec.Command(pip, append([]string{"install"}, pkgs...)...)
		install.Stdout = os.Stdout
		install.Stderr = os.Stderr
		if err := install.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "\npip install failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "Retry individual packages to narrow it down:")
			for _, p := range pkgs {
				fmt.Fprintf(os.Stderr, "    %s install %s\n", pip, p)
			}
			os.Exit(1)
		}
	}

	pythonExe := filepath.Join(*venvPath, "bin", "python")
	fmt.Printf("\nDone. Set this in your config.toml:\n    python_bin = %q\n", pythonExe)
}
