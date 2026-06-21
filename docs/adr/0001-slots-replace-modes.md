# 1. Slots replace backend modes

Status: Accepted

## Context

mlxd exposed its internal vocabulary as the user's vocabulary. A backend had a
`mode` (`swap` | `persistent` | `external`) and, for swap, a `group`. Three
names looked identical to a user — backend name, group name, swap-member name —
and you had to know which mode a model was in just to talk to it.

The router already unified inference: `POST /v1/chat/completions {"model": X}`
resolves `X` whether it is a swap member, a group, a persistent backend, or an
external URL, and loads/swaps as needed (`registry.Resolve` → `EnsureLoaded`).
The fragmentation was entirely in the surface: config authoring, `mlxctl`
verbs, and status output.

`persistent` was never a distinct user need. It was "a slot with one candidate
that stays warm." `external` was "a slot whose model lives elsewhere." Both are
properties of a slot, not separate kinds of thing.

## Decision

Collapse the user-facing model to **one noun and two flags**:

- **slot** — the named, addressable holder you type. Holds one or more models
  that share memory; one is hot at a time.
- **model** — the weights (HF id / path) a slot can load.
- **default** — the model a slot loads when addressed by its slot name.
- **warm** — the slot starts at boot and respawns on crash (replaces
  `mode=persistent`). Default is lazy: load on first request, stay resident.
- **remote** — the slot proxies an external URL (replaces `mode=external`).

Addressing rules: you always address a **slot**; it loads its default. To get a
specific model under a stable short name, give it its own slot. Forcing a
specific member of a shared slot (swap-on-request) still works at the router but
is not the headline path.

### Internal mapping (anti-corruption at the builder)

The three supervisor implementations (`Group`, `Persistent`, `External`) are
**retained as private strategy**. The config loader and the backend builder
derive which one backs a slot from the slot's *shape*:

| slot shape                | impl         |
|---------------------------|--------------|
| any member `remote`       | `External`   |
| `warm` (single member)    | `Persistent` |
| otherwise (1..N members)  | `Group`      |

`Persistent` is reused for warm slots specifically to keep its proven
respawn-with-backoff loop (`persistent.go:manage`) untouched. The user never
authors or sees these names; `Backend.Mode()` becomes a derived label consumed
only by the status snapshot.

This is deliberately *not* a rewrite of the supervisor concurrency. "Persistent
the mechanism goes away" means it leaves the config, CLI, and status — where the
user lived — not that the battle-tested respawn code is deleted.

## Migration / back-compat

The loader normalizes legacy configs at load time:

- `mode=swap` + `group=G` → slot `G`
- `mode=persistent` → `warm=true`, slot = name
- `mode=external` → `remote=true`
- `group` is read as a synonym for `slot`

So **every existing config keeps running unchanged**. `mlxctl config migrate`
rewrites a TOML file to the new vocabulary (dropping `mode`, renaming
`group`→`slot`, setting `warm`/`remote`) with a `.bak` backup, for users who
want the legacy fields physically gone. It is optional and idempotent.

`mlxctl run` becomes a hidden alias of `mlxctl chat <slot>`; `swap` stays an
alias of `start`. No CLI invocation breaks.

## Consequences

- Three modes become one concept plus two booleans. The
  `{mode ∈ swap|persistent|external}` axis disappears from the authored schema.
- A slot of one is the natural way to give a long HF id a short name; long ids
  leave daily use.
- `audio` remains the one engine that is a genuine multi-model slot (one
  process serves many TTS models, picked per request). It is documented as the
  exception rather than bent into the one-hot-model frame.
- Cost: the loader carries a normalization layer for legacy fields, and the
  builder gains a shape→impl derivation. Both are small and localized.
- Internal `Group`/`Persistent`/`External` names persist in the supervisor
  package; a later refactor may merge them, but it is not required by this
  decision.
