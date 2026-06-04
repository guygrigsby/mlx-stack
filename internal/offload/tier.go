package offload

import "path/filepath"

type Tier string

const (
	TierUnknown   Tier = "unknown"
	TierOffloaded Tier = "offloaded"
	TierHot       Tier = "hot"
	TierLocalOnly Tier = "local-only"
)

func (m *Manager) cachePath(name string) string { return filepath.Join(m.opt.CacheRoot, name) }
func (m *Manager) libPath(name string) string   { return filepath.Join(m.opt.LibraryRoot, name) }

// Tier classifies a model by name from the filesystem. Caller need not hold mu.
func (m *Manager) Tier(name string) Tier {
	inCache := m.opt.FS.Exists(m.cachePath(name))
	inLib := m.opt.FS.Exists(m.libPath(name))
	switch {
	case inCache && inLib:
		return TierHot
	case inCache:
		return TierLocalOnly
	case inLib:
		return TierOffloaded
	default:
		return TierUnknown
	}
}
