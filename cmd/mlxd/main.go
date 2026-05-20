package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/admin"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/logobs"
	"github.com/guygrigsby/mlx-stack/internal/logrot"
	"github.com/guygrigsby/mlx-stack/internal/obsstate"
	"github.com/guygrigsby/mlx-stack/internal/router"
	"github.com/guygrigsby/mlx-stack/internal/supervisor"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: mlxd run [--config path] [--socket path] [--log-level lvl] [--log-json] [--log-dir dir]")
		os.Exit(2)
	}

	cmdRun := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := cmdRun.String("config", defaultConfigPath(), "path to config.toml")
	socketPath := cmdRun.String("socket", defaultSocketPath(), "admin unix socket path")
	logLevel := cmdRun.String("log-level", "info", "debug|info|warn|error")
	logJSON := cmdRun.Bool("log-json", false, "emit logs as JSON")
	logDir := cmdRun.String("log-dir", "", "directory for rotating mlxd-YYYY-MM-DD.log files")
	cmdRun.Parse(os.Args[2:])

	logger, rotator := setupLogger(*logLevel, *logJSON, *logDir)
	defer func() {
		if rotator != nil {
			rotator.Close()
		}
	}()
	slog.SetDefault(logger)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	logger.Info("config loaded", "path", *cfgPath, "router_port", cfg.Router.Port, "chat_port", cfg.Chat.Port)

	broker := logobs.NewBroker()
	obsStore := obsstate.New()
	go obsStore.Run(context.Background(), broker)

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
				Broker:  broker,
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
					Broker:  broker,
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

	var embedBackend router.ManagedBackend
	if cfg.Embed.Alias != "" {
		if cfg.Embed.Managed {
			em := supervisor.NewManaged(supervisor.ManagedOpts{
				Name:          "embed",
				Host:          cfg.Embed.Host,
				Port:          cfg.Embed.Port,
				Alias:         cfg.Embed.Alias,
				UpstreamModel: cfg.Embed.Model,
				Args: []string{
					"-m", "mlx_stack.launcher_shim",
					"--engine", "embed",
					"--model", cfg.Embed.Model,
					"--host", cfg.Embed.Host,
					"--port", fmt.Sprintf("%d", cfg.Embed.Port),
				},
				WorkerFactory: func(args []string) *supervisor.Worker {
					return supervisor.New(supervisor.WorkerSpec{
						Name: "embed", Command: cfg.PythonBin, Args: args, Logger: logger, Broker: broker,
					})
				},
			})
			if err := em.Start(context.Background()); err != nil {
				logger.Error("embed start failed", "err", err)
			} else {
				logger.Info("embed backend up", "alias", cfg.Embed.Alias, "url", em.URL())
				embedBackend = em
			}
		} else {
			embedBackend = supervisor.NewExternalAdapter(cfg.Embed.Alias, cfg.Embed.URL, cfg.Embed.Alias)
			logger.Info("embed backend (external)", "alias", cfg.Embed.Alias, "url", cfg.Embed.URL)
		}
	}

	ttsMgr := buildAudioManaged("tts", cfg.TTS, cfg.PythonBin, logger, broker)
	if ttsMgr != nil {
		if err := ttsMgr.Start(context.Background()); err != nil {
			logger.Error("tts start failed", "err", err)
			ttsMgr = nil
		} else {
			logger.Info("tts backend up", "alias", cfg.TTS.Alias, "url", ttsMgr.URL())
		}
	}

	kokoroMgr := buildAudioManaged("kokoro", cfg.Kokoro, cfg.PythonBin, logger, broker)
	if kokoroMgr != nil {
		if err := kokoroMgr.Start(context.Background()); err != nil {
			logger.Error("kokoro start failed", "err", err)
			kokoroMgr = nil
		} else {
			logger.Info("kokoro backend up", "alias", cfg.Kokoro.Alias, "url", kokoroMgr.URL())
		}
	}

	var managedBackends []router.ManagedBackend
	if tagsMgr != nil {
		managedBackends = append(managedBackends, tagsMgr)
	}
	if embedBackend != nil {
		managedBackends = append(managedBackends, embedBackend)
	}
	if ttsMgr != nil {
		managedBackends = append(managedBackends, ttsMgr)
	}
	if kokoroMgr != nil {
		managedBackends = append(managedBackends, kokoroMgr)
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
		Handler:    (&admin.Handlers{Config: cfg, Chat: chatSwap, Tags: tagsMgr, Broker: broker, ObsStore: obsStore}).Mux(),
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
	if em, ok := embedBackend.(*supervisor.Managed); ok {
		_ = em.Stop(ctx)
	}
	if tagsMgr != nil {
		_ = tagsMgr.Stop(ctx)
	}
	for _, m := range []*supervisor.Managed{ttsMgr, kokoroMgr} {
		if m != nil {
			_ = m.Stop(ctx)
		}
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

func buildAudioManaged(name string, ai config.AudioInstance, pythonBin string, logger *slog.Logger, broker *logobs.Broker) *supervisor.Managed {
	if ai.Alias == "" {
		return nil
	}
	return supervisor.NewManaged(supervisor.ManagedOpts{
		Name:          name,
		Host:          ai.Host,
		Port:          ai.Port,
		Alias:         ai.Alias,
		UpstreamModel: "", // mlx_audio.server multiplexes via the per-request "model" field
		Args: []string{
			"-m", "mlx_stack.launcher_shim",
			"--engine", "audio",
			"--host", ai.Host,
			"--port", fmt.Sprintf("%d", ai.Port),
		},
		WorkerFactory: func(args []string) *supervisor.Worker {
			return supervisor.New(supervisor.WorkerSpec{
				Name: name, Command: pythonBin, Args: args, Logger: logger, Broker: broker,
			})
		},
	})
}

func setupLogger(level string, jsonOut bool, logDir string) (*slog.Logger, *logrot.Rotator) {
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
