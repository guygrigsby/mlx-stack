package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/admin"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/router"
	"github.com/guygrigsby/mlx-stack/internal/supervisor"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: mlxd run [--config path] [--socket path] [--log-level lvl] [--log-json]")
		os.Exit(2)
	}

	cmdRun := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := cmdRun.String("config", defaultConfigPath(), "path to config.toml")
	socketPath := cmdRun.String("socket", defaultSocketPath(), "admin unix socket path")
	logLevel := cmdRun.String("log-level", "info", "debug|info|warn|error")
	logJSON := cmdRun.Bool("log-json", false, "emit logs as JSON")
	cmdRun.Parse(os.Args[2:])

	logger := setupLogger(*logLevel, *logJSON)
	slog.SetDefault(logger)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	logger.Info("config loaded", "path", *cfgPath, "router_port", cfg.Router.Port, "chat_port", cfg.Chat.Port)

	chatSwap := supervisor.NewChatSwap(supervisor.ChatSwapOpts{
		Host:           cfg.Chat.Host,
		Port:           cfg.Chat.Port,
		Profiles:       cfg.Chat.Profiles,
		DefaultProfile: cfg.Chat.DefaultProfile,
		SwapTimeoutSec: cfg.Chat.SwapTimeoutSec,
		WorkerFactory: func(name string, args []string) *supervisor.Worker {
			return supervisor.New(supervisor.WorkerSpec{
				Name:    name,
				Command: cfg.PythonBin,
				Args:    args,
				Env:     workerEnv(cfg),
				Logger:  logger,
			})
		},
		WorkerEnv: workerEnv(cfg),
	})

	var tagsMgr *supervisor.Managed
	if cfg.Tags.Model != "" {
		tagsMgr = supervisor.NewManaged(supervisor.ManagedOpts{
			Name:          "tags",
			Host:          cfg.Tags.Host,
			Port:          cfg.Tags.Port,
			Alias:         cfg.Tags.Alias,
			UpstreamModel: cfg.Tags.Model,
			Args: []string{
				"-m", "mlx_stack.launcher_shim",
				"--engine", cfg.Tags.Engine,
				"--model", cfg.Tags.Model,
				"--host", cfg.Tags.Host,
				"--port", fmt.Sprintf("%d", cfg.Tags.Port),
			},
			Env: tagsEnv(cfg),
			WorkerFactory: func(args []string) *supervisor.Worker {
				return supervisor.New(supervisor.WorkerSpec{
					Name:    "tags",
					Command: cfg.PythonBin,
					Args:    args,
					Env:     tagsEnv(cfg),
					Logger:  logger,
				})
			},
		})
		if err := tagsMgr.Start(context.Background()); err != nil {
			logger.Error("tags start failed", "err", err)
			// don't exit — chat still works
		} else {
			logger.Info("tags backend up", "alias", cfg.Tags.Alias, "url", tagsMgr.URL())
		}
	}

	var managedBackends []router.ManagedBackend
	if tagsMgr != nil {
		managedBackends = append(managedBackends, tagsMgr)
	}
	registry := router.NewRegistry(cfg, chatSwap, managedBackends...)
	routerSrv := router.NewServer(router.ServerOpts{
		Config:   cfg,
		Chat:     chatSwap,
		Registry: registry,
	})

	httpSrv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Router.Host, cfg.Router.Port),
		Handler: routerSrv.Handler(),
	}

	adminSrv := &admin.Server{
		SocketPath: *socketPath,
		Handler:    (&admin.Handlers{Config: cfg, Chat: chatSwap, Tags: tagsMgr}).Mux(),
	}

	if err := adminSrv.Start(); err != nil {
		logger.Error("admin server start", "err", err)
		os.Exit(1)
	}
	logger.Info("admin socket listening", "path", *socketPath)

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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if tagsMgr != nil {
		_ = tagsMgr.Stop(ctx)
	}
	_ = chatSwap.Stop(ctx)
	_ = httpSrv.Shutdown(ctx)
	_ = adminSrv.Shutdown(ctx)
	logger.Info("bye")
}

func workerEnv(cfg *config.Config) []string {
	env := []string{}
	c := cfg.Chat
	if c.Cache.LimitBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_LIMIT_BYTES=%d", c.Cache.LimitBytes))
	}
	if c.Cache.ClearIntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_INTERVAL_SEC=%d", c.Cache.ClearIntervalSec))
	}
	if c.Cache.ClearThresholdBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_THRESHOLD_BYTES=%d", c.Cache.ClearThresholdBytes))
	}
	if c.Watchdog.KVHeadroomBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_KV_HEADROOM_BYTES=%d", c.Watchdog.KVHeadroomBytes))
	}
	if c.Watchdog.CheckIntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC=%d", c.Watchdog.CheckIntervalSec))
	}
	if c.Watchdog.GraceSec > 0 {
		env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_GRACE_SEC=%d", c.Watchdog.GraceSec))
	}
	if c.Memlog.IntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_MEMLOG_INTERVAL_SEC=%d", c.Memlog.IntervalSec))
	}
	return env
}

func tagsEnv(cfg *config.Config) []string {
	env := []string{}
	t := cfg.Tags
	if t.Cache.LimitBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_LIMIT_BYTES=%d", t.Cache.LimitBytes))
	}
	if t.Cache.ClearIntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_INTERVAL_SEC=%d", t.Cache.ClearIntervalSec))
	}
	if t.Cache.ClearThresholdBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_THRESHOLD_BYTES=%d", t.Cache.ClearThresholdBytes))
	}
	if t.Watchdog.KVHeadroomBytes > 0 {
		env = append(env, fmt.Sprintf("MLX_KV_HEADROOM_BYTES=%d", t.Watchdog.KVHeadroomBytes))
	}
	if t.Watchdog.CheckIntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC=%d", t.Watchdog.CheckIntervalSec))
	}
	if t.Watchdog.GraceSec > 0 {
		env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_GRACE_SEC=%d", t.Watchdog.GraceSec))
	}
	if t.Memlog.IntervalSec > 0 {
		env = append(env, fmt.Sprintf("MLX_MEMLOG_INTERVAL_SEC=%d", t.Memlog.IntervalSec))
	}
	return env
}

func setupLogger(level string, jsonOut bool) *slog.Logger {
	lvl := slog.LevelInfo
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if jsonOut {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mlx", "config.toml")
}

func defaultSocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "mlxd", "admin.sock")
}
