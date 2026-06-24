# 2. Everything is lazy-loaded; remove the warm/persistent lifecycle

Status: Accepted

Supersedes the `warm` flag and the `persistent` lifecycle described in
[0001](0001-slots-replace-modes.md).

## Context

ADR 0001 kept two backend lifecycles: lazy `swap` slots and always-on `warm`
slots (internally `mode=persistent`), the latter forced on `embed`/`audio`
singletons. In practice the distinction bought almost nothing:

- **Neither auto-starts at boot.** Persistent backends were already registered
  lazily (`main.go` logged "(lazy)"); the router calls `EnsureLoaded` on the
  first request for both kinds.
- **The only real behavioral residue was a proactive respawn-while-idle loop**
  (`supervisor.Persistent.manage`): backoff-respawn a worker even when nothing
  is asking for it. The swap `Group` already self-heals — `watchExit` clears
  loaded state on worker death so the *next request* respawns. For a lazy
  service, respawning an idle backend nobody is using is wasted work, not
  durability. Recovery-on-next-request is the event-driven equivalent and
  satisfies "durable by default": the service is back the moment it is needed.

So `warm` was a second code path (a whole supervisor type) for a singleton —
which is just "a slot with one model." The user said as much: a group of one
has nothing to swap to.

## Decision

One lifecycle. Every non-remote backend is a lazy `Group` slot:

- A singleton (`embed`, `audio`, a lone `lm`/`vlm`) is a **group of one** —
  addressed by its own name, loaded on first request, reloaded on the next
  request if its worker dies.
- `remote` (external URL proxy) is unchanged.

Removed:

- `supervisor.Persistent` (the `manage` respawn loop, backoff, `probeUntilReady`).
- `BackendSpec.normalize()` no longer derives `mode=persistent`; everything
  non-remote normalizes to `swap`.
- `Config.Persistents()`, the boot/​reload `persistent` branches, the
  `liveState.persistents` set.
- The `--warm` CLI flag and the `warm` / `warm·stopped` status words.

`Validate()` now accepts any valid engine for a `swap` slot (`audio` still
needs no model). `config migrate` drops `mode=swap|persistent` and stale
`warm = true` lines; `mode=external` still becomes `remote = true`.

### Back-compat

Strict TOML decode rejects unknown keys, so the `Warm bool` field is kept,
parsed-but-inert, so existing `warm = true` configs still load (they normalize
to a lazy slot of one). `mode = "persistent"` likewise still loads. Drop the
`Warm` field once configs are migrated.

## Consequences

- One supervisor (`Group`) plus `External`. `supervisor.Persistent` is deleted
  (~340 lines).
- A crashed singleton no longer respawns while idle; it reloads on the next
  request. No user-visible difference for a service that only matters when in
  use.
- A first-load that exceeds the group swap timeout (90s) is killed rather than
  left downloading, as it was for `Persistent`. Offload pre-pulls weights into
  the SSD cache before spawn (`BeforeLoad`), so the spawn→ready window is
  RAM-load only; raise `swap_timeout_sec` if a cold first load needs longer.
