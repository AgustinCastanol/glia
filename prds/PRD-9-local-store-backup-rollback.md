# PRD-9 — Local store backup & rollback

**Status:** Draft
**Owner:** @agustincastanol
**Last updated:** 2026-05-29
**Depends on:** PRD-0 (canonical schema), PRD-5 (config/identity/install)
**Target release:** next minor (v1.x)

---

## 1. Context

glia keeps its source-of-truth memory in a local store under `.glia/`:

| File | Role |
|------|------|
| `memory.jsonl` | append-only canonical record log (the actual memories) |
| `index.json` | derived index over the log |
| `schema.json` | store schema version |
| `.lock` | concurrency lock |

A glia upgrade with a bad migration, or a buggy sync, can corrupt **glia's own store**. When that
happens today there is no first-class way to return to a known-good state. The package
`internal/store` already ships recovery primitives (`recover.go`, `rebuild.go`) that rebuild the
index from the log, but those do not help if `memory.jsonl` itself is damaged or rewritten.

This PRD adds explicit, restorable snapshots of glia's local store so the user can roll back if
a version breaks their memories.

## 2. Goals

- Capture a point-in-time snapshot of glia's local store (`memory.jsonl`, `index.json`,
  `schema.json`, and the sync-state / IDMap — exact set resolved in design) that can be fully
  restored later.
- Provide a CLI surface to create, list, and restore snapshots.
- Make restore **atomic**: a crash mid-restore must never leave a half-written store.
- Make backup cheap enough to run automatically before risky operations (sync / upgrade).
- Refuse to restore a snapshot whose schema is incompatible with the running binary, with a
  clear message (reuse existing `schema_too_new` handling).

## 3. Non-Goals

- **Backing up provider-native data is OUT OF SCOPE.** glia does NOT snapshot claude-mem's SQLite
  or engram's store. (Decided 2026-05-29.)
- **No undo of provider writes.** If a buggy sync already pushed bad records into claude-mem or
  engram, restoring glia's local store does NOT remove them from the providers. This PRD protects
  glia's own store only; the limitation must be documented in user-facing help.
- No remote/cloud backup destinations. Snapshots live on the local filesystem.
- No incremental/diff backups — each snapshot is a full copy (the log is small; ~2 MB today).

### 3.1 Why glia does NOT back up each provider (decision record, 2026-05-30)

The question came up: should glia snapshot each provider's data (claude-mem's SQLite, engram's
store) so a sync that corrupts memories can be rolled back? **No.** Three reasons, in order of
weight:

1. **A backup you cannot restore from is theater.** glia writes to claude-mem via
   `POST /api/memory/save`, and that worker exposes **no DELETE and no PATCH** (PRD-7 §2.4). Even
   with a snapshot of claude-mem's SQLite, glia has no API path to return the provider to a prior
   state. Restoring would require writing directly to the provider's database — explicitly
   rejected by PRD-2 §3. The snapshot would be unusable for its only purpose.

2. **Each provider owns its own durability.** claude-mem and gentle-ai already create their own
   pre-upgrade backups; engram keeps its own store on `:7437`. glia is a **sync** tool, not a
   backup system for other tools' databases. Snapshotting a provider's internals would couple
   glia to each provider's storage format and break whenever a provider changes its schema —
   guaranteed maintenance debt.

3. **glia's canonical log is already the useful recovery source — via re-push, not raw restore.**
   `memory.jsonl` is a canonical mirror of synced records. If a provider loses data, glia can
   **re-push** through the normal sync path — a real, supported rollback, not a hack on provider
   files.

   **Caveat that bounds reason 3:** glia's mirror only contains what it has *pulled*. Under the
   current config (`sync.mirror_engram: false`, observed asymmetric flow ~760 pushed / 0 pulled),
   the local log is **not** a complete copy of each provider. For glia's store to function as a
   re-hydration source for the providers, the sync must be full bidirectional
   (`mirror_engram: true`, pull from both sides). That is a sync-configuration concern, **not** a
   backup feature — and explicitly out of scope for this PRD.

**Conclusion:** providers back themselves up; glia backs up only its own store (this PRD); and
provider rollback, where desired, is achieved by completing bidirectional sync, not by
snapshotting provider databases.

## 4. What gets backed up

A snapshot is a full copy of glia's local store data files. The canonical log
(`memory.jsonl`) is the authoritative artifact; `index.json` is derived and MAY be omitted from
the snapshot and rebuilt on restore (see §8 open question). `schema.json` is included so restore
can validate compatibility. The sync-state / IDMap (location resolved in design — likely within
`index.json` or a dedicated file) MUST be included so a restore does not desync the IDMap from
the log.

The `.lock` file is never part of a snapshot.

## 5. Backup triggers

- **Manual:** `glia backup create` — explicit, on demand.
- **Pre-sync (opt-in):** when `backup.before_sync: true`, `glia sync` creates a snapshot before
  the push phase. Off by default to keep sync fast.
- **Pre-upgrade (documented):** before upgrading the glia binary, the user runs
  `glia backup create --note "pre-upgrade vX.Y.Z"`. (glia does not manage its own upgrade, so
  this stays a manual, documented step rather than an automatic hook.)

## 6. CLI surface

```
glia backup create [--note "<text>"]    # snapshot now; prints snapshot id
glia backup list                        # table: id, created_at, record_count, size, note
glia backup restore <id> [--yes]        # restore snapshot <id> (confirmation required)
glia backup prune [--keep N]            # drop old snapshots beyond retention
```

`restore` without `--yes` prompts for confirmation and shows what will change (current record
count → snapshot record count).

## 7. Storage format & location

- Snapshots live under `.glia/backups/`.
- Each snapshot is a single compressed tarball: `.glia/backups/<id>.tar.gz`, where `<id>` is a
  sortable timestamp (ULID or RFC3339-derived). Compression keeps the ~2 MB log small and makes
  retention/space management trivial.
- A snapshot embeds a small manifest (created_at, schema_version, record_count, optional note)
  for fast `list` without unpacking the whole archive.

## 8. Restore semantics

1. Acquire the store lock (`.glia/.lock`).
2. Validate the snapshot manifest's `schema_version` against the running binary. If the snapshot
   schema is newer than supported → **refuse** with a clear error (reuse `schema_too_new`).
3. Write restored files to a temp location, then atomically swap them into `.glia/` (rename),
   so a crash leaves either the old store or the new one — never a partial mix.
4. If `index.json` was not part of the snapshot, rebuild it from `memory.jsonl` via the existing
   `rebuild.go` path.
5. Release the lock. Report what was restored (record count, snapshot id, note).

## 9. Retention

- Config `backup.keep: N` (default 10). `glia backup prune` and the pre-sync trigger drop the
  oldest snapshots beyond `N`.
- `glia backup create` never deletes implicitly except via the configured retention on the
  triggered paths.

## 10. Config

`.glia/config.yaml`:

```yaml
backup:
  before_sync: false   # NEW. Snapshot before each sync push. Default false.
  keep: 10             # NEW. Retain this many snapshots; prune older.
```

Absent block → backups disabled for auto triggers; the `glia backup` commands still work manually.

## 11. Status / observability

`glia status` gains a backups line:

```
backups:  12 snapshots, latest 2026-05-29T01:36 (pre-upgrade v1.2.0)
```

If backups directory is empty:

```
backups:  none
```

## 12. Open Questions

1. **IDMap location:** is the canonical_id → native_id IDMap stored inside `index.json`,
   `syncstate`, or a dedicated file? Design phase must pin this so the snapshot set is complete.
   (Likely `internal/store/syncstate.go` — to be confirmed.)
2. **Snapshot `index.json` or rebuild on restore?** Rebuilding from the log guarantees the index
   is consistent with the data but is slower; snapshotting it is faster but risks restoring a
   stale/corrupt index. Leaning toward: snapshot the log + sync-state, rebuild the index on
   restore.
3. **Compression:** gzip vs none. Leaning gzip (log is highly compressible JSONL).

## 13. Acceptance Criteria

- `glia backup create` produces a restorable snapshot under `.glia/backups/`.
- After mutating the store and running `glia backup restore <id>`, the store matches the
  snapshot exactly (record count and per-record canonical hashes match).
- Restore is atomic: simulating a crash mid-restore leaves a valid store (old or new, never
  partial).
- Restoring a snapshot whose schema is newer than the binary supports is refused with a clear
  error and the current store is left untouched.
- `backup.keep: N` prunes snapshots beyond `N`; `glia backup list` reflects retention.
- `backup.before_sync: true` creates a snapshot before each sync; `false`/absent does not.
- `glia status` shows snapshot count and latest snapshot metadata.
