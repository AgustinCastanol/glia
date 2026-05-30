# PRD-3 — Sync Engine

**Status:** Draft
**Owner:** @agustincastanol
**Last updated:** 2026-05-12
**Depends on:** PRD-0 (canonical schema), PRD-1 (adapter contract + engram), PRD-2 (claude-mem adapter)

---

## 1. Context

PRDs 0-2 established WHAT the data looks like and HOW each provider speaks. PRD-3 defines the orchestration: when and how observations move between providers and the canonical JSONL store in the project repo.

The sync engine is the only component that:

- Decides direction of flow (pull vs push).
- Tracks per-provider sync watermarks.
- Resolves collisions when the same canonical record is updated by multiple authors.
- Decides whether to mirror with `engram sync`.
- Reports degraded states (provider down, worker not running) without aborting.

## 2. Goals

- A simple, predictable CLI: `glia sync [pull|push]` covers 95% of usage.
- Append-only writes to `memory.jsonl` so git merges stay sane.
- Graceful degradation: a missing or broken provider produces a warning, not a crash, and the rest of the sync continues.
- Idempotence: re-running `sync` with no changes is a no-op (no spurious file writes, no spurious commits).
- A `--dry-run` flag that shows exactly what WOULD happen.
- No surprise git actions. The user owns commits and pushes by default.

## 3. Non-Goals (v1)

- Real-time sync (no daemons, no file watchers).
- Conflict UI (the TUI in PRD-4 will surface conflicts; the engine just detects and records them).
- Pull/push from remote backends (we're git-based; remote = `git pull`).
- Automatic merging of logically-duplicate observations from different providers (deferred to v2; see §10.3).

## 4. Command Surface

```
glia init                          # create .glia/, schema.json, .gitignore entries
glia sync                          # pull then push, all enabled providers
glia sync pull                     # canonical JSONL -> local providers
glia sync push                     # local providers -> canonical JSONL
glia sync --dry-run                # show changes without writing
glia sync --provider engram        # restrict to one provider
glia sync --mirror-engram          # also run `engram sync` on push (PRD-1 §5.6)
glia sync --commit                 # also `git add .glia/ && git commit` after success
glia status                        # health of all providers + last sync timestamps
glia show [--kind K] [--type T]    # render memory.jsonl in human-friendly form
```

**Default behavior of `glia sync` (no args):** pull then push, all enabled providers, no mirror, no commit. Writes only to `.glia/`.

## 5. State and Watermarks

Sync state lives in `.glia/index.json` (gitignored, per PRD-0 §10.4). New top-level field:

```jsonc
{
  "schema_version": 1,
  "canonical": { /* ... */ },
  "by_provider": { /* ... */ },
  "sync_state": {
    "engram":     { "last_pushed_at": "2026-05-12T10:00:00Z", "last_pulled_at": "2026-05-12T09:00:00Z" },
    "claude-mem": { "last_pushed_at": null,                   "last_pulled_at": "2026-05-12T09:00:00Z" }
  }
}
```

- `last_pushed_at` = the `updated_at` of the most recent record successfully pushed FROM this provider TO canonical.
- `last_pulled_at` = the most recent `updated_at` we tried to pull FROM canonical INTO this provider.
- `claude-mem.last_pushed_at` is always `null` in v1 (read-only).

Watermarks are advisory, not authoritative. They're used to skip work in the common path but the engine also handles records older than the watermark (idempotent re-runs).

## 6. Sync Algorithms

### 6.1 Push (provider → canonical)

For each enabled adapter `A` (in deterministic order — engram first, then claude-mem):

```
since = sync_state[A.Name].last_pushed_at  // or epoch if null
nativeIDs = A.ListNative(ctx, project, since)
new_records = []
max_updated_at = since

for id in nativeIDs:
    native = A.ReadNative(ctx, id)
    canonical_id, found = idmap[A.Name].CanonicalFromNative(id)

    if not found:
        canonical_id = ulid.New()                    // first time entering canonical
        canonical = A.ToCanonical(native, idmap)     // adapter assigns canonical_id internally
        canonical.revision = 1
        canonical.supersedes = null
    else:
        prior = read_latest_revision_from_jsonl(canonical_id)
        if records_equal_ignoring_metadata(native, prior):
            continue                                  // no change, skip
        canonical = A.ToCanonical(native, idmap)
        canonical.revision = prior.revision + 1
        canonical.supersedes = prior.canonical_id

    new_records.append(canonical)
    if native.updated_at > max_updated_at:
        max_updated_at = native.updated_at

if new_records:
    append_to_memory_jsonl(new_records)
    rebuild_index_for(new_records)

sync_state[A.Name].last_pushed_at = max_updated_at
```

### 6.2 Pull (canonical → provider)

For each enabled adapter `A` that supports writes (engram only in v1):

```
since = sync_state[A.Name].last_pulled_at  // or epoch if null
candidates = scan_memory_jsonl(updated_at >= since)

// Group by canonical_id, keep latest revision only (handles merge collisions, see §7)
latest_per_id = collapse_to_latest_revision(candidates)

for canonical in latest_per_id:
    if canonical.origin.provider == A.Name():
        // Don't re-import what came from this provider in the first place,
        // unless the local provider lost it (e.g. fresh install).
        if A_has_native(canonical):
            continue

    if canonical.kind not in A.SupportedKinds():
        continue   // e.g. claude-mem doesn't accept relations

    if canonical.deleted:
        // v1: deletes are not propagated INTO providers. The canonical record
        // is the source of truth; providers keep their copy. Documented limit.
        continue

    native = A.FromCanonical(canonical)
    native_id, err = A.WriteNative(ctx, native)
    if err == adapter.ErrUnsupported:
        continue   // adapter is read-only
    if err != nil:
        log_warning(err); continue

    idmap[A.Name].Bind(native_id, canonical.canonical_id)

sync_state[A.Name].last_pulled_at = now()
```

### 6.3 Full sync (default)

```
pull()
push()
```

In this order so that records appended by `push` are not immediately fed back through `pull` in the same run.

## 7. Conflict and Collision Handling

`memory.jsonl` is append-only, so git merges on the file itself rarely create textual conflicts (it's always-append, multiple appenders merge cleanly). What CAN happen is **revision collisions**:

> Person A and Person B both update `canonical_id = X` in parallel branches. Both append a line with `revision = N+1`. After merge, the JSONL contains TWO lines with `revision = N+1` for `canonical_id = X`.

### 7.1 Detection

When rebuilding `index.json.canonical[id].latest_revision`, if more than one line has the same `(canonical_id, revision)`, it's a collision.

### 7.2 Resolution — deterministic tiebreaker

Pick the winner by:

1. Greater `updated_at` wins.
2. If tied, greater `canonical_id` (lexicographic ULID) wins.

The loser is **NOT** removed from `memory.jsonl` (append-only). It's recorded in `index.json.conflicts` for the TUI (PRD-4) to surface:

```jsonc
{
  "conflicts": [
    {
      "canonical_id": "01HXY...",
      "revision": 3,
      "winner_line": 1487,
      "loser_line": 1492,
      "detected_at": "2026-05-12T14:00:00Z"
    }
  ]
}
```

### 7.3 Manual override

`glia sync resolve <canonical_id> --keep <line_number>` lets a user force a different winner by appending a `revision = N+2` superseding line that mirrors the chosen one. The conflict entry is then cleared.

## 8. Mirror with `engram sync`

Per PRD-1 §5.6, opt-in via `--mirror-engram` or project config:

```yaml
# .glia/config.yaml
mirror_engram: true
```

When enabled:

- After a successful `push`, run `engram sync --project <P>`. This updates `.engram/` chunks.
- Before a `pull`, run `engram sync --import` so the local engram DB ingests any new `.engram/` chunks pulled via git, BEFORE the canonical → engram pull stage runs (avoids duplicate writes of the same logical record via two paths).

Disabled by default. The engram sub-team that uses `engram sync` standalone keeps doing so.

## 9. Failure Modes

| Scenario                                       | Behavior                                                       |
|------------------------------------------------|----------------------------------------------------------------|
| Provider's `Health()` fails                    | Warn ("provider X unavailable, skipping"); continue with others; exit code 0 if any provider succeeded, else 2. |
| `ReadNative` fails on one record               | Warn (`record id, error`); skip that record; continue.         |
| `WriteNative` fails on one record              | Warn; skip; continue. `last_pulled_at` is NOT advanced past the failed record's timestamp, so we retry next run. |
| `memory.jsonl` corrupt (invalid JSON on a line)| Refuse to run; emit a `glia doctor` recommendation.    |
| `memory.jsonl` and `schema.json` mismatch      | Refuse to run; emit clear error.                               |
| Network/IO error mid-append                    | The partial file is detected on next run (last line incomplete) and truncated to last newline before continuing. |

`glia status` exit codes:

- `0` — all providers healthy
- `1` — at least one provider degraded
- `2` — glia itself misconfigured (no `.glia/`, invalid schema)

## 10. Open Questions

1. **Delete propagation**: v1 does NOT propagate canonical deletes (tombstones) into providers. The canonical record is marked deleted, but the local engram still has its copy. Acceptable for v1 (deletes are rare); revisit in v1.1 with a `--prune` flag.
2. **Batch size for pull/push**: should there be a max records per run? Currently unbounded. If a fresh install pulls 10k records, the first run will be long. Acceptable for v1.
3. **Cross-provider logical dedup**: an observation captured both in engram (by user A) and in claude-mem (by user B, as part of a session summary) will land as two distinct canonical records. v1 does not try to dedup. v2 might offer a "merge candidates" UI in the TUI.
4. **Concurrency on the same machine**: if two `glia sync` run simultaneously (e.g. user runs it manually while a cron fires), `memory.jsonl` could interleave appends. Solution: lockfile at `.glia/.lock` (PID + acquired_at). Refuse to run if locked. Confirm this is enough for v1.
5. **What does `glia sync` do when run OUTSIDE a repo with `.glia/`?** Refuse with a clear message: "no canonical store found — run `glia init` first."

## 11. Decision Required Before PRD-4 (TUI)

None blocking. PRD-4 builds a TUI on top of the commands defined here. The TUI needs:

- A read view of `memory.jsonl` (already available via `glia show`).
- A status view (already via `glia status`).
- A conflict resolution flow (already specced as `glia sync resolve` in §7.3).
- A "tail" / live-update view of new sync activity (TBD — likely just polling `index.json`).
