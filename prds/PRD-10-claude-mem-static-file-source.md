# PRD-10 — claude-mem static file source (file transport)

**Status:** Deferred — blocked upstream
**Owner:** @agustincastanol
**Last updated:** 2026-06-13
**Depends on:** PRD-0 (canonical schema), PRD-1 (adapter contract), PRD-2 (claude-mem adapter, read-only HTTP)
**Target release:** unscheduled (pending upstream export surface)

---

## 0. Deferral note (2026-06-13)

This PRD is **deferred — blocked upstream**. The `static-file-sources` SDD change
shipped PRD-11 (openspec source) only; PRD-10 was not implemented.

**Reason:** the file transport requires claude-mem to emit its data as static files,
but no such surface exists. Confirmed during the `static-file-sources` exploration
against claude-mem v13.2.0:

- No `exports/` or `archive/` directory exists under `~/.claude-mem/` — only the live
  SQLite database, `supervisor.json`, `chroma/`, `logs/`, and `backups/`.
- The `server export` command referenced in §3 is **unimplemented**: inspection of the
  worker bundle (`worker-service.cjs`) found no export route handlers.
- Direct SQLite reads remain rejected (§3): fragile across claude-mem versions.

With nothing to read, the `file` transport has no source. **Revisit when claude-mem
ships a stable export/archive surface** (e.g. a real `server export`). The design below
is preserved as-is for that future work.

---

## 1. Context

PRD-2 implemented the claude-mem adapter as **read-only over HTTP**: glia reads observations
from the running worker at `http://localhost:37701` via `GET /api/observations`. That design has
one hard operational dependency — **the worker must be up**. PRD-2 §10 even requires `glia sync`
to check `Health()` and skip claude-mem if the daemon is down.

That dependency hurts in real situations:

- **Offline / cold machine.** No worker running → no claude-mem memories sync, silently skipped.
- **CI and ephemeral environments.** Spinning up the claude-mem daemon just to ingest its history
  is heavy and often impossible.
- **Forensics / one-shot import.** A user with an exported claude-mem dump (from another machine,
  a backup, a teammate) has no way to feed it into glia.

claude-mem can emit its data as **static files** on disk (its `server export` surface, and its
local archive directory). This PRD adds a **second, file-based transport** for the existing
claude-mem adapter: glia reads those static files directly into the canonical store, with **no
worker and no HTTP**. It is the same read-only philosophy as PRD-2 — only the transport changes.

This is one of two sibling PRDs introducing on-disk read-only sources; PRD-11 (openspec) is the
other. Both should share a common "static file source" mechanism, pinned in the design phase.

## 2. Goals

- Add a **file-based read transport** to the claude-mem adapter that ingests static export files
  into glia's canonical store, requiring no running worker and no HTTP.
- **Reuse the existing field mapping** (PRD-2 §6) so HTTP and file transports converge to byte-
  identical canonical records for the same source observation.
- Make the source path **configurable** and, where possible, auto-discovered.
- Keep the transport choice **honest and visible** in `glia status`.
- Guarantee **idempotency across transports**: ingesting the same observation via file then HTTP
  (or vice-versa) MUST NOT create a duplicate canonical record.

## 3. Non-Goals

- **Writing back to claude-mem.** This is still read-only. Write support lives in PRD-7 (HTTP
  `POST /api/memory/save`) and does not gain a file path here.
- **Direct SQLite reads** of `~/.claude-mem/claude-mem.db`. Still rejected (PRD-2 §3) — fragile
  across claude-mem versions. "Static files" means the **export / archive** surface, not the
  internal database.
- **Producing the export.** glia consumes claude-mem's static files; it does not run
  `claude-mem server export` for the user (that may become a convenience wrapper later, but it is
  out of scope here).
- **Live watch / tail** of the files. This is a one-shot read per sync. Continuous file watching
  is a later concern.

## 4. Transport: file

PRD-2 §4 framed claude-mem access as a **transport** decision (HTTP). This PRD adds `file` as a
peer transport behind the same `Adapter` interface. `ListNative` is the only method that changes
behavior; `Health()`, `FromCanonical`, and the (unsupported) `WriteNative` are unchanged.

```yaml
# .glia/config.yaml
providers:
  claude-mem:
    transport: http            # NEW. http | file. Default: http (PRD-2 behavior preserved).
    base_url: http://localhost:37701   # used when transport: http
    file:
      path: ~/.claude-mem/exports      # used when transport: file (see §5, §9)
```

- `transport: http` → exactly today's behavior. Default, so existing configs are unaffected.
- `transport: file` → adapter reads from `file.path`; `base_url` is ignored.
- `Health()` under `file` transport checks that `file.path` exists and is readable (returns
  `ErrUnavailable` otherwise), instead of pinging `/health`.

### 4.1 Optional `auto` transport (open question — §9.3)

A possible third value, `transport: auto`: try the worker first, fall back to file if the worker
is down. Deferred as an open question; v1 ships explicit `http` / `file`.

## 5. Source format

> **STATUS: TO-CONFIRM on a real export sample.** As in PRD-2 §5, the exact on-disk shape of
> claude-mem's export is **not yet verified** and is version-dependent. The adapter MUST dump the
> raw bytes of the first record it reads and the format MUST be reconfirmed before the
> implementation merges.

Expected candidates (design phase picks and pins one, supports more if cheap):

1. **JSONL export** — one observation per line, the same logical record shape the HTTP
   `/api/observations` items use. Preferred: it round-trips cleanly through the PRD-2 §5
   `ClaudeMemRecord` decoder.
2. **Directory of markdown files** — claude-mem's archived session summaries as `.md` files, with
   front-matter or a sidecar index for metadata (id, project, timestamps).

The adapter normalizes whichever format into the existing internal `ClaudeMemRecord`, then runs
the **unchanged** PRD-2 §6 mapping to canonical.

## 6. Field mapping

**Unchanged from PRD-2 §6.** The file transport produces the same internal `ClaudeMemRecord`, so
the claude-mem → canonical mapping (origin.provider = `claude-mem`, `kind: session_summary`, etc.)
is reused verbatim. This is the whole point: transport must not affect canonical identity or
content.

## 7. Listing & filtering strategy

`ListNative(ctx, project, since)` under `file` transport:

1. Resolve `file.path`. If it is a file → read it; if a directory → walk it (sorted, deterministic).
2. Decode each record into `ClaudeMemRecord` (skip malformed lines/files, logging a count).
3. Filter in memory, identical to PRD-2 §8:
   - `project_id` matches the configured glia project (skip mismatches — protects against leaking
     other projects' observations, PRD-2 §9.4).
   - `updated_at >= since`.

## 8. Idempotency across transports (critical)

Both transports MUST resolve to the **same `canonical_id`** for the same claude-mem observation.
This is already guaranteed if the `IDMap` is keyed on `origin.provider_id` (the claude-mem
observation id), independent of transport — which is the PRD-2 §6 design. This PRD adds the
explicit requirement and a test:

- Ingest observation `X` via `file`, then run an `http` sync that also sees `X` → exactly one
  canonical record, no second revision unless `updated_at` advanced.
- `revision` bumps only on `updated_at` advance (PRD-2 §6), never merely because the transport
  changed.

## 9. Open Questions

1. **Export format & command.** Which `claude-mem` command produces the static files this PRD
   reads (`server export`? an archive dir written by the worker?), and what is its exact shape?
   Blocking before implementation — dump a real sample (mirrors PRD-2 §9.2).
2. **Path auto-discovery.** Can the default `file.path` be discovered the way PRD-2 §9.5 discovers
   the worker port (`~/.claude-mem/supervisor.json`)? If claude-mem records an export/archive
   location, read it instead of hardcoding `~/.claude-mem/exports`.
3. **`transport: auto` fallback.** Worth shipping worker-then-file fallback, or keep transports
   explicit in v1? (§4.1.)
4. **Stale export detection.** A file export is a point-in-time snapshot that goes stale as new
   sessions are captured. Should `glia status` warn when the export's newest record is older than
   the worker would have (when both are reachable)? Or is staleness purely the user's
   responsibility? Leaning: surface the export's newest `updated_at` in status, no hard warning.
5. **Mixed-project exports.** A single export holds ALL of a developer's Claude Code projects
   (same risk as PRD-2 §9.4). The `project_id` filter MUST be strict; the design must verify the
   export carries `project_id` per record.

## 10. Config summary

```yaml
providers:
  claude-mem:
    transport: file            # http (default) | file
    file:
      path: ~/.claude-mem/exports
```

Absent `transport` → `http` (PRD-2 behavior). Absent `file.path` with `transport: file` →
adapter is unavailable with a clear error ("claude-mem file transport selected but no
`file.path` configured").

## 11. Status / observability

`glia status` shows the active claude-mem transport and source health:

```
claude-mem:  ✓ file  (~/.claude-mem/exports, 1,204 records, newest 2026-05-30T22:10)
```

or, when the configured path is missing:

```
claude-mem:  ✗ file  (~/.claude-mem/exports not found)
```

This keeps the asymmetry honest, the same spirit as PRD-2 §2.

## 12. Acceptance Criteria

- With `transport: file` and a valid export at `file.path`, `glia sync` ingests claude-mem
  observations into the canonical store **with no worker running**.
- The canonical records produced via `file` are **identical** (canonical_id, content, kind,
  revision) to those the HTTP transport produces for the same observations.
- Ingesting the same observation via both transports yields **exactly one** canonical record (no
  duplicates), and no spurious revision bump from changing transport.
- `project_id` filtering is strict: observations from other projects in the same export are not
  imported into the current project's store.
- `transport: http` (or absent) preserves PRD-2 behavior exactly — existing configs are
  unaffected.
- `Health()` under `file` transport fails clearly when `file.path` is missing/unreadable, and
  `glia status` reflects the active transport and source freshness.
- Malformed lines/files in the export are skipped with a logged count, never aborting the sync.
