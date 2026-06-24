package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"sync"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/admin"
	bk "github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/logobs"
	"github.com/guygrigsby/mlx-stack/internal/offload"
	"github.com/guygrigsby/mlx-stack/internal/router"
	"github.com/guygrigsby/mlx-stack/internal/supervisor"
)

// backendBuilder turns a BackendSpec into a running backend, capturing the
// deps shared across every backend. Boot and hot reload both use it, so there
// is a single construction path.
type backendBuilder struct {
	pythonBin string
	shimDir   string
	defaults  config.Defaults
	broker    *logobs.Broker
	logger    *slog.Logger
	offload   *offload.Manager // nil when [offload] is absent (single-tier)
}

// buildOffloadManager returns a configured offload.Manager, or nil when
// [offload] is absent (single-tier behavior). stateDir is mlxd's state dir.
func buildOffloadManager(cfg *config.Config, stateDir string) *offload.Manager {
	if cfg.Offload == nil {
		return nil
	}
	m, err := offload.New(offload.Options{
		CacheRoot:   cfg.ModelsRoot,
		LibraryRoot: cfg.Offload.ExternalRoot,
		Budget:      cfg.Offload.LocalBudgetBytes,
		StatePath:   filepath.Join(stateDir, "offload.json"),
		FS:          offload.OSStore{},
		Pinned:      func() map[string]bool { return nil }, // replaced after registry is built
	})
	if err != nil {
		slog.Error("offload: disabled", "err", err)
		return nil
	}
	return m
}

// offloadBeforeLoad returns a BeforeLoad hook that pulls a backend's model (and
// draft model) into the SSD cache before the worker spawns. nil manager => nil hook.
func offloadBeforeLoad(m *offload.Manager) func(context.Context, config.BackendSpec) error {
	if m == nil {
		return nil
	}
	return func(ctx context.Context, spec config.BackendSpec) error {
		for _, p := range []string{spec.Model, spec.DraftModel} {
			if p == "" {
				continue
			}
			if err := m.EnsurePulled(ctx, filepath.Base(p)); err != nil {
				return err
			}
		}
		return nil
	}
}

// newGroup builds a swap Group from its member specs. The default member is
// the one flagged default=true, else the first declared.
func (bd *backendBuilder) newGroup(name string, members []config.BackendSpec) *supervisor.Group {
	first := members[0]
	defaultMember := first.Name
	mm := map[string]config.BackendSpec{}
	for _, m := range members {
		mm[m.Name] = m
		if m.Default {
			defaultMember = m.Name
		}
	}
	gName := name
	return supervisor.NewGroup(supervisor.GroupOpts{
		Name:           gName,
		Host:           first.Host,
		Port:           first.Port,
		Members:        mm,
		DefaultMember:  defaultMember,
		SwapTimeoutSec: 90,
		ProbeInterval:  250 * time.Millisecond,
		BeforeLoad:     offloadBeforeLoad(bd.offload),
		WorkerFactory: func(spec config.BackendSpec) *supervisor.Worker {
			return supervisor.New(supervisor.WorkerSpec{
				Name:    fmt.Sprintf("%s[%s]", gName, spec.Name),
				Command: bd.pythonBin,
				Args:    launcherArgs(spec),
				Env:     backendEnv(spec, bd.defaults, bd.shimDir),
				Broker:  bd.broker,
				Logger:  bd.logger,
			})
		},
	})
}

// liveState owns the mutable backend set. A hot reload grows it from the admin
// goroutine while requests read it; shutdown drains it. All mutation and the
// shutdown read go through mu.
type liveState struct {
	mu sync.Mutex

	builder  *backendBuilder
	registry *router.Registry
	admin    *admin.Handlers
	cfgPath  string
	logger   *slog.Logger

	groups   map[string]*supervisor.Group
	backends []bk.Backend
	aliases  map[string]string
}

// diffNewBackends splits cfg's backends into those not yet known by name (to
// add) and those already registered (to skip). Pure, so it is unit-testable
// apart from the live mutation.
func diffNewBackends(known map[string]bool, cfg *config.Config) (add []config.BackendSpec, skip []string) {
	for _, b := range cfg.Backends {
		if known[b.Name] {
			skip = append(skip, b.Name)
			continue
		}
		add = append(add, b)
	}
	return add, skip
}

// reload re-reads the config and registers any backends not already known
// (additive). New backends become routable immediately and load lazily on
// first request. Removed or edited entries are ignored until a restart. On a
// config parse error nothing is mutated.
func (ls *liveState) reload(_ context.Context) (admin.ReloadResult, error) {
	cfg, err := config.Load(ls.cfgPath)
	if err != nil {
		return admin.ReloadResult{}, fmt.Errorf("reload: %w", err)
	}

	ls.mu.Lock()
	defer ls.mu.Unlock()

	known := map[string]bool{}
	for _, n := range ls.registry.Names() {
		known[n] = true
	}
	add, skip := diffNewBackends(known, cfg)

	var res admin.ReloadResult
	res.Skipped = skip
	for _, spec := range add {
		switch spec.Mode {
		case "external":
			ext := supervisor.NewExternal(spec.Name, spec.URL, spec.UpstreamModel)
			ls.registry.Register(ext)
			ls.backends = append(ls.backends, ext)
			ls.logger.Info("hot-loaded external backend", "name", spec.Name, "url", spec.URL)
		case "swap":
			if g, ok := ls.groups[spec.Group]; ok {
				added, mismatch := g.AddMember(spec)
				if mismatch {
					ls.logger.Warn("hot-load swap member port differs from group; using group port",
						"member", spec.Name, "group", spec.Group, "member_port", spec.Port)
				}
				if !added {
					// Member already present under the group (alias may not have
					// been in the registry yet); treat as a skip.
					res.Skipped = append(res.Skipped, spec.Name)
					continue
				}
				ls.registry.RegisterAlias(spec.Name, g)
				ls.aliases[spec.Name] = g.Name()
				ls.logger.Info("hot-loaded swap member", "name", spec.Name, "group", spec.Group)
			} else {
				g := ls.builder.newGroup(spec.Group, []config.BackendSpec{spec})
				ls.groups[spec.Group] = g
				ls.registry.Register(g)
				ls.backends = append(ls.backends, g)
				if spec.Name != spec.Group {
					ls.registry.RegisterAlias(spec.Name, g)
					ls.aliases[spec.Name] = g.Name()
				}
				ls.logger.Info("hot-loaded swap group", "group", spec.Group, "member", spec.Name)
			}
		default:
			ls.logger.Warn("hot-load skipped backend with unknown mode", "name", spec.Name, "mode", spec.Mode)
			res.Skipped = append(res.Skipped, spec.Name)
			continue
		}
		res.Added = append(res.Added, spec.Name)
	}

	// Publish the grown view to the admin layer. Hand it copies so later
	// mutations under ls.mu never race readers holding the snapshot.
	backendsCopy := append([]bk.Backend(nil), ls.backends...)
	aliasesCopy := make(map[string]string, len(ls.aliases))
	maps.Copy(aliasesCopy, ls.aliases)
	ls.admin.SetState(backendsCopy, aliasesCopy)

	return res, nil
}

// stopAll terminates every owned worker. Called on shutdown.
func (ls *liveState) stopAll(ctx context.Context) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for _, g := range ls.groups {
		_ = g.Stop(ctx)
	}
}
