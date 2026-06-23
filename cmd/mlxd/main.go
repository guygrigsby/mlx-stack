package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/admin"
	bk "github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/logobs"
	"github.com/guygrigsby/mlx-stack/internal/logrot"
	"github.com/guygrigsby/mlx-stack/internal/obsstate"
	"github.com/guygrigsby/mlx-stack/internal/router"
	"github.com/guygrigsby/mlx-stack/internal/supervisor"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:          "mlxd",
		Short:        "MLX inference supervisor daemon",
		SilenceUsage: true,
	}
	root.AddCommand(newRunCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRunCmd() *cobra.Command {
	var (
		cfgPath     string
		socketPath  string
		logLevel    string
		logJSON     bool
		logDir      string
		shimDirFlag string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			shimDir := shimDirFlag
			if shimDir == "" {
				shimDir = detectShimDir()
			}

			logger, rotator := setupLogger(logLevel, logJSON, logDir)
			slog.SetDefault(logger)
			defer func() {
				if rotator != nil {
					rotator.Close()
				}
			}()

			cfg, err := config.Load(cfgPath)
			if err != nil {
				logger.Error("config load failed", "err", err)
				return fmt.Errorf("config load failed: %w", err)
			}
			logger.Info("config loaded", "path", cfgPath, "router_port", cfg.Router.Port, "backends", len(cfg.Backends))

			broker := logobs.NewBroker()
			obsStore := obsstate.New()
			go obsStore.Run(context.Background(), broker)

			// Two-tier offload manager (nil when [offload] is absent). State
			// lives next to the admin socket. Pinned set is wired below, once
			// the backends slice exists.
			stateDir := filepath.Dir(socketPath)
			offloadMgr := buildOffloadManager(cfg, stateDir)

			// Shared deps for turning a BackendSpec into a running backend.
			// Boot and hot reload both go through this builder so there is one
			// construction path.
			builder := &backendBuilder{
				pythonBin: cfg.PythonBin,
				shimDir:   shimDir,
				defaults:  cfg.Defaults,
				broker:    broker,
				logger:    logger,
				offload:   offloadMgr,
			}

			// Build all backends at boot.
			var backends []bk.Backend
			groupBackends := map[string]*supervisor.Group{}
			aliases := map[string]string{}

			// 1. Groups (swap-mode collections).
			groups := cfg.BackendsByGroup()
			for groupName, members := range groups {
				g := builder.newGroup(groupName, members)
				groupBackends[groupName] = g
				backends = append(backends, g)
			}

			// 2. Persistents.
			var persistents []*supervisor.Persistent
			for _, spec := range cfg.Persistents() {
				pb := builder.newPersistent(spec)
				// Don't spawn at daemon start. Persistent backends load lazily:
				// the router calls EnsureLoaded on the first request, or load
				// them up front with `mlxctl start <name>`.
				logger.Info("persistent backend registered (lazy)", "name", spec.Name, "url", pb.BaseURL())
				persistents = append(persistents, pb)
				backends = append(backends, pb)
			}

			// 3. Externals.
			for _, e := range cfg.Externals() {
				ext := supervisor.NewExternal(e.Name, e.URL, e.UpstreamModel)
				backends = append(backends, ext)
				logger.Info("external backend", "name", e.Name, "url", e.URL)
			}

			// Build registry. Register each backend by Name, and each swap-group
			// member name as an alias for the Group.
			registry := router.NewRegistry(backends...)
			for groupName, members := range groups {
				g := groupBackends[groupName]
				for _, m := range members {
					if m.Name != groupName {
						registry.RegisterAlias(m.Name, g)
						aliases[m.Name] = g.Name()
					}
				}
			}

			routerSrv := router.NewServer(router.ServerOpts{
				Config:   cfg,
				Registry: registry,
			})

			httpSrv := &http.Server{
				Addr:    fmt.Sprintf("%s:%d", cfg.Router.Host, cfg.Router.Port),
				Handler: routerSrv.Handler(),
			}

			// liveState owns the mutable backend set that hot reload grows and
			// shutdown drains. Both the admin reload handler and the shutdown
			// path go through it under its lock.
			live := &liveState{
				builder:     builder,
				registry:    registry,
				cfgPath:     cfgPath,
				groups:      groupBackends,
				persistents: persistents,
				backends:    backends,
				aliases:     aliases,
				logger:      logger,
			}

			// Pin currently-loaded models so offload never evicts a model that
			// is serving. Reads the live backend set (grows on hot reload) under
			// its lock. Only meaningful when offload is enabled.
			if offloadMgr != nil {
				offloadMgr.SetPinned(func() map[string]bool {
					out := map[string]bool{}
					live.mu.Lock()
					defer live.mu.Unlock()
					for _, b := range live.backends {
						if b.Running() && b.UpstreamModel() != "" {
							out[filepath.Base(b.UpstreamModel())] = true
						}
					}
					return out
				})
			}

			handlers := &admin.Handlers{
				Config:      cfg,
				Broker:      broker,
				ObsStore:    obsStore,
				Reload:      live.reload,
				ActiveFunc:  routerSrv.Active,
			}
			if offloadMgr != nil {
				handlers.Offloader = offloadMgr
				handlers.Tierer = offloadMgr
			}
			handlers.SetState(backends, aliases)
			live.admin = handlers

			adminSrv := &admin.Server{
				SocketPath: socketPath,
				Handler:    handlers.Mux(),
			}

			if err := adminSrv.Start(); err != nil {
				logger.Error("admin server start", "err", err)
				return fmt.Errorf("admin server start: %w", err)
			}
			logger.Info("admin socket listening", "path", socketPath)

			go func() {
				logger.Info("router listening", "addr", httpSrv.Addr)
				if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					logger.Error("router serve", "err", err)
					os.Exit(1)
				}
			}()

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
			<-stop
			logger.Info("shutting down")
			shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			live.stopAll(shutCtx)
			_ = httpSrv.Shutdown(shutCtx)
			_ = adminSrv.Shutdown(shutCtx)
			logger.Info("bye")
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", defaultConfigPath(), "path to config.toml")
	cmd.Flags().StringVar(&socketPath, "socket", defaultSocketPath(), "admin unix socket path")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "debug|info|warn|error")
	cmd.Flags().BoolVar(&logJSON, "log-json", false, "emit logs as JSON")
	cmd.Flags().StringVar(&logDir, "log-dir", "", "rotated log directory")
	cmd.Flags().StringVar(&shimDirFlag, "shim-dir", "", "directory containing the mlx_stack Python package (overrides auto-detection)")
	return cmd
}

func launcherArgs(spec config.BackendSpec) []string {
	args := []string{
		"-m", "mlx_stack.launcher_shim",
		"--engine", spec.Engine,
		"--host", spec.Host,
		"--port", fmt.Sprintf("%d", spec.Port),
	}
	if spec.Engine != "audio" {
		args = append(args, "--model", spec.Model)
	}
	if spec.DraftModel != "" {
		args = append(args, "--draft-model", spec.DraftModel)
	}
	if spec.TrustRemoteCode {
		args = append(args, "--trust-remote-code")
	}
	return args
}

// detectShimDir locates the mlx_stack Python package without requiring
// `pip install -e`. Checked in order:
//  1. $MLX_STACK_SHIM_DIR env var.
//  2. Sibling to the binary: <exe-dir>/../share/mlx-stack/python (brew + make install layout).
//  3. Repo-local: <cwd>/python (dev mode: `./bin/mlxd run` from repo root).
//
// Returns "" if nothing found — workers fall back to whatever's on the venv's sys.path.
func detectShimDir() string {
	if v := os.Getenv("MLX_STACK_SHIM_DIR"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "..", "share", "mlx-stack", "python")
		if _, err := os.Stat(filepath.Join(candidate, "mlx_stack", "__init__.py")); err == nil {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "python")
		if _, err := os.Stat(filepath.Join(candidate, "mlx_stack", "__init__.py")); err == nil {
			return candidate
		}
	}
	return ""
}

func backendEnv(spec config.BackendSpec, d config.Defaults, shimDir string) []string {
	cache := spec.EffectiveCache(d)
	wd := spec.EffectiveWatchdog(d)
	ml := spec.EffectiveMemlog(d)
	env := []string{}
	if cache.LimitBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_LIMIT_BYTES=%d", cache.LimitBytes))
	}
	if cache.ClearIntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_INTERVAL_SEC=%d", cache.ClearIntervalSec))
	}
	if cache.ClearThresholdBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_THRESHOLD_BYTES=%d", cache.ClearThresholdBytes))
	}
	if wd.KVHeadroomBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_KV_HEADROOM_BYTES=%d", wd.KVHeadroomBytes))
	}
	if wd.CheckIntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC=%d", wd.CheckIntervalSec))
	}
	if wd.GraceSec > 0 {
		env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_GRACE_SEC=%d", wd.GraceSec))
	}
	if ml.IntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_MEMLOG_INTERVAL_SEC=%d", ml.IntervalSec))
	}
	if shimDir != "" {
		if existing := os.Getenv("PYTHONPATH"); existing != "" {
			env = append(env, "PYTHONPATH="+shimDir+":"+existing)
		} else {
			env = append(env, "PYTHONPATH="+shimDir)
		}
	}
	return env
}

func setupLogger(level string, jsonOut bool, logDir string) (*slog.Logger, *logrot.Rotator) {
	lvl := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}

	var out io.Writer = os.Stderr
	var rotator *logrot.Rotator
	if logDir != "" {
		rotator = logrot.New(logDir, "mlxd")
		out = rotator
	}

	var h slog.Handler
	if jsonOut {
		h = slog.NewJSONHandler(out, opts)
	} else {
		h = slog.NewTextHandler(out, opts)
	}
	return slog.New(h), rotator
}

func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mlx", "config.toml")
}

func defaultSocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "mlxd", "admin.sock")
}
