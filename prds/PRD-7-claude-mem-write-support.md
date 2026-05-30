# PRD-7 — claude-mem Write Support (bidirectional sync, v1.x)

**Status:** Draft (post-discovery)
**Owner:** @agustincastanol
**Last updated:** 2026-05-27
**Depends on:** PRD-0 (canonical schema), PRD-1 (adapter contract), PRD-2 (claude-mem adapter read-only), PRD-3 (sync engine)
**Target release:** next minor (v1.x)

---

## 1. Context

In v1.0/v1.1 the claude-mem adapter is **read-only** (PRD-2 §1, ADR-10). `glia sync` flows strictly claude-mem → engram. PRD-2 §9.1 deferred bidirectional support to v1.x pending HTTP write surface investigation. PRD-7 closes that investigation and specifies the write path.

The user-facing motivation: when an engram observation is saved (manually via `mem_save`, SDD artifacts, etc.), the developer expects it to also be visible from claude-mem. Today it isn't. The pipe is one-way.

## 2. Discovery Results (verified 2026-05-27)

Repository: `thedotmack/claude-mem@main`. Source-of-truth file: `src/services/worker/http/routes/MemoryRoutes.ts`.

### 2.1 Write endpoint exists — and on the worker we already use

`POST /api/memory/save` is exposed by the SAME worker on `localhost:37701` that glia already consumes for reads (`GET /api/observations`). **No new transport, no new process, no migration to `server-beta`.**

### 2.2 Verified request schema

Strict zod schema:

```ts
{
  text: string;                         // required, min length 1
  title?: string;                        // optional — auto-derived from first 60 chars of text if absent
  project?: string;                      // optional — falls back to metadata.project, then worker's defaultProject
  metadata?: Record<string, unknown>;    // optional, free-form
}
```

Strict mode rejects unknown top-level fields.

### 2.3 Verified write behavior

- Stored as observation with `type: 'discovery'` (hardcoded).
- Attached to a synthetic `manual` session per project (`sessionStore.getOrCreateManualSession(targetProject)`).
- Auto-syncs to ChromaDB (best-effort, non-blocking).
- Response: `{ success, id, title, project, message }` where `id` is claude-mem's numeric auto-increment.
- Written via `sessionStore.storeObservation()` — the same DB that `GET /api/observations` reads. **Round-trip confirmed at the storage layer.**

### 2.4 What is NOT exposed

- **No PATCH/PUT** — observations are append-only via this endpoint. Updates are not supported.
- **No DELETE** — deletions are not supported.
- **No control over `created_at`, `updated_at`, `session_id`, `tags`** — the worker assigns these. `tags` field doesn't exist on the schema at all.
- **No auth** — worker is local-only by design.

### 2.6 Live probe results (verified 2026-05-27 against running worker v13.x)

Executed end-to-end probe against `localhost:37701`:

- `POST /api/memory/save` with `{}` → `400 ValidationError` (text required). Endpoint detection works.
- `POST /api/memory/save` with valid payload → `200 { success:true, id:685, title, project, message }`.
- `GET /api/observations` after the save → record id 685 visible.

**Two findings that change the design**:

1. **`metadata` is write-only.** `GET /api/observations` does NOT return the `metadata` field we sent. Stashing `glia:canonical_id` in metadata for round-trip lookup is impossible — we write it but cannot read it back. IDMap MUST live in glia's local store; we cannot offload it to claude-mem.

2. **The worker triggers asynchronous AI summarization on manual saves.** After our single `POST /api/memory/save`, the worker emitted additional `type: "discovery"` observations (ids 686, 687) containing AI-compressed summaries of the activity around the save. Implication: writing N observations may produce M ≥ N entries in claude-mem. Glia's next `ListNative` will see the originals AND the summaries. Loop prevention (§5.3) is even more critical — without it, the summaries would re-import into canonical and re-push.

3. **No DELETE** means the test probe (id 685) and its AI-derived siblings persist. Mild side-effect cost of running this PRD's discovery; acceptable.

### 2.5 server-beta is NOT the target

We separately verified `server-beta` (`src/server/routes/v1/ServerV1Routes.ts`) which exposes `POST /v1/memories` + `PATCH /v1/memories/:id`. **Rejected for v1.x because**:

- It is a separate opt-in process (`claude-mem server worker start`), not the default install.
- It requires API keys (`CLAUDE_MEM_AUTH_MODE=api-key`) via `writeAuth` middleware.
- It would force the user to run an extra service just for glia.

Defer server-beta integration to v1.y when (a) it becomes the default, OR (b) the user explicitly opts in via config.

## 3. Goals

- Make `glia sync` bidirectional: canonical records originating outside claude-mem (engram, manual, SDD) are pushed via `POST /api/memory/save`.
- Honest provenance: stash `glia:canonical_id`, `glia:from`, `glia:revision` inside `metadata` so round-trips identify themselves.
- Idempotent: re-running `glia sync` does NOT create duplicate claude-mem observations.
- Graceful degradation: if the worker is on a version without `/api/memory/save`, fall back to read-only with a clear status message.
- Loop prevention: records whose `origin.provider == "claude-mem"` are NEVER pushed back into claude-mem.

## 4. Non-Goals

- Updates / deletes of existing claude-mem observations (not exposed by the worker — see §2.4).
- server-beta integration (deferred — see §2.5).
- SQLite direct writes (stays rejected per PRD-2 §3).
- Tag preservation as a first-class field in claude-mem (the schema has no `tags`). We stash them in `metadata["glia:tags"]` for forward compatibility, but they remain unreadable on round-trip until claude-mem exposes `metadata` in `GET /api/observations`.

## 5. Adapter changes

### 5.1 Transport (`internal/adapter/claudemem/transport.go`)

Add:

```go
type SaveMemoryRequest struct {
    Text     string                 `json:"text"`
    Title    string                 `json:"title,omitempty"`
    Project  string                 `json:"project,omitempty"`
    Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type SaveMemoryResponse struct {
    Success bool   `json:"success"`
    ID      int64  `json:"id"`
    Title   string `json:"title"`
    Project string `json:"project"`
    Message string `json:"message"`
}

func (t *Transport) SaveMemory(ctx context.Context, body SaveMemoryRequest) (*SaveMemoryResponse, error)
```

Endpoint detection: on first call, probe `POST /api/memory/save` with a no-op invalid payload (e.g., empty `text`). If the server responds 404 → cache `writeSupported = false`. If it responds 400 (validation error) → cache `writeSupported = true`. Cache the result for the process lifetime.

### 5.2 Adapter (`internal/adapter/claudemem/claudemem.go`)

`WriteNative` becomes:

```go
func (a *ClaudeMemAdapter) WriteNative(ctx context.Context, record adapter.NativeRecord) (adapter.NativeID, error) {
    if !a.transport.WriteSupported(ctx) {
        return "", adapter.ErrUnsupported
    }
    cmRecord := record.(*ClaudeMemRecord)
    resp, err := a.transport.SaveMemory(ctx, SaveMemoryRequest{
        Text:    cmRecord.Summary,
        Title:   cmRecord.Title,
        Project: a.config.ProjectAlias, // PRD-6 alias
        Metadata: map[string]interface{}{
            "glia:canonical_id": cmRecord.GliaCanonicalID,
            "glia:from":         cmRecord.GliaOriginProvider,
            "glia:revision":     cmRecord.GliaRevision,
            "glia:tags":         cmRecord.GliaTags,            // []string; omit if empty
            "glia:source_session_id": cmRecord.GliaSourceSessionID, // from canonical.Origin.SessionID; omit if empty
        },
    })
    if err != nil { return "", err }
    return adapter.NativeID(strconv.FormatInt(resp.ID, 10)), nil
}
```

`FromCanonical(canonical)` becomes load-bearing — promotes PRD-2 §7 from "Not applicable" to:

| Canonical field      | Maps to                         | Notes                                                                |
|----------------------|---------------------------------|----------------------------------------------------------------------|
| `content`            | `text`                          | Required by the worker (min 1).                                      |
| `title`              | `title`                         | Optional; worker auto-derives if empty.                              |
| (project alias)      | `project`                       | From PRD-6 per-provider project alias config.                        |
| `canonical_id`       | `metadata["glia:canonical_id"]` | For idempotency lookup on next sync.                                 |
| `origin.provider`    | `metadata["glia:from"]`         | Honest provenance.                                                   |
| `revision`           | `metadata["glia:revision"]`     | Detect content drift across syncs (see §6).                          |
| `tags`               | `metadata["glia:tags"]`         | Stashed as JSON array. Write-only today (metadata not in GET); forward-compatible.|
| `origin.session_id`  | `metadata["glia:source_session_id"]` | Preserves engram session context. Lands in claude-mem's synthetic `manual` session regardless; this is forensic only.|
| `kind`               | (filter)                        | Only `observation` and `session_summary` pushed. Others skipped.     |
| `created_at`, `updated_at` | (DROPPED)                 | Worker assigns its own timestamps. Document the loss.                |

### 5.3 Loop prevention

The sync engine MUST filter `origin.provider == "claude-mem"` BEFORE invoking `claudemem.WriteNative`. Enforced in `internal/sync/push.go`. Integration test verifies that a record read from claude-mem in sync N is NOT pushed back to claude-mem in sync N+1.

## 6. Idempotency strategy (no PATCH, metadata not readable)

Since `/api/memory/save` only creates AND `GET /api/observations` does NOT return `metadata` (verified §2.6), idempotency cannot lean on claude-mem at all. Strategy:

1. **Glia-local IDMap is authoritative**: glia stores `canonical_id → claude_mem_native_id` in its own store after every successful push. We never re-derive this from claude-mem.
2. **Skip-if-already-pushed**: on every sync, before push, check the local IDMap. If present → skip. No round-trip metadata lookup.
3. **Content drift detection**: `IDMap.last_pushed_revision` is tracked locally. If `canonical.revision > last_pushed`, log a warning ("claude-mem cannot receive updates — content has drifted") and skip. Do NOT push a duplicate.
4. **Worker-generated siblings**: when the worker emits AI-summarized observations triggered by our save (§2.6), they appear in the next `ListNative` with `origin.provider == "claude-mem"`. The loop-prevention filter (§5.3) keeps them out of the push direction. They flow into engram as session_summaries, which is the right behavior.
5. **Metadata still useful for humans**: even though glia cannot read it back, the `metadata` field IS persisted (the worker stores it in SQLite). Future versions of claude-mem may expose it. Cost is zero, so we keep stamping `glia:canonical_id`, `glia:from`, `glia:revision` for forward compatibility and human forensics.

This is acceptable lossiness because claude-mem's design is append-only summarization, not authoritative source-of-truth for edits.

## 7. Config

`.glia/config.yaml`:

```yaml
providers:
  claude-mem:
    base_url: http://localhost:37701
    timeout: 10s
    write_enabled: true   # NEW. Default true; user can disable for exact v1.0 behavior.
```

If `write_enabled: false`, behave as v1.0 (read-only). Rollback switch.

## 8. Sync engine changes (PRD-3 update)

`internal/sync/push.go`:

- New phase "push to claude-mem", after the existing engram push.
- Order: pull from both → diff → push to engram (existing) → push to claude-mem (new).
- Filter: `kind ∈ {observation, session_summary}` AND `origin.provider != "claude-mem"`.
- Skip if `IDMap.has(canonical_id)`.
- Backfill: first sync after enabling `write_enabled` pushes ALL eligible historical records. Subsequent syncs push only new ones. No `--backfill` flag needed because IDMap naturally gates duplicates.
- If the worker rejects writes (endpoint missing), log once per sync and continue. Report counter: `WrittenToClaudeMem: skipped (write-unsupported)`.
- If individual writes fail (non-404 error, e.g. schema drift on a 4xx/5xx), count them under `ClaudeMemWriteErrors` and continue. Push phase never aborts the whole sync over claude-mem write failures.

## 9. Status / observability

`glia status`:

```
claude-mem:  read+write   (worker on :37701)
```

If write probe failed:

```
claude-mem:  read-only    (worker version lacks POST /api/memory/save)
```

`glia sync` report gains `WrittenToClaudeMem` counter AND `ClaudeMemWriteErrors` counter (per-sync failure count for honest degraded-state reporting per §10 Q3).

If `ClaudeMemWriteErrors > 0` in the most recent sync, `glia status` shows:

```
claude-mem:  degraded     (N write errors in last sync — worker schema may have drifted)
```

## 10. Open Questions

1. ~~**Tag loss**~~ **RESOLVED (2026-05-27)**: stash tags in `metadata["glia:tags"]` as a JSON array. Cost is zero; forward-compatible when claude-mem exposes metadata in `GET`. Tags remain practically unreadable on round-trip today, but no information is destroyed on the write path. Mapping moved to §5.2.
2. ~~**session_id mapping**~~ **RESOLVED (2026-05-27)**: stash `canonical.Origin.SessionID` in `metadata["glia:source_session_id"]`. Canonical schema already carries `Origin.SessionID` (`internal/store/record.go:34`); engram adapter already populates it (`internal/adapter/engram/engram.go:466`). No PRD-0 change required. Same caveat as Q1: write-only until claude-mem exposes metadata in GET. Pushed observations still land in claude-mem's synthetic `manual` session — that is by design and not in our control.
3. ~~**Schema drift**~~ **RESOLVED (2026-05-27)**: no active version-check. Rationale: the §5.1 endpoint probe already gates "route exists / route missing"; claude-mem does not publish a stable API changelog to pin against; a version-check would create false negatives. Instead, fail honestly at write time: count adapter errors per sync, and surface `claude-mem: degraded (N write errors last sync)` in `glia status` when failures occur. Add this counter to §8 (sync report) and §9 (status).
4. **server-beta opt-in**: when do we add the `server-beta` transport as an alternative? Probably when the user runs both worker AND server-beta and explicitly sets `providers.claude-mem.transport: server-beta` in config. Out of scope for v1.x.

## 11. Acceptance Criteria

- `glia sync` writes engram-origin observations into claude-mem via `POST /api/memory/save` on a clean install where the worker is running and on a version exposing the route.
- Re-running `glia sync` is idempotent (verified via IDMap-gated test).
- A claude-mem-origin observation does NOT get re-pushed to claude-mem (§5.3, integration test).
- `glia status` shows `read+write` when supported, `read-only` with reason otherwise.
- `write_enabled: false` restores exact v1.0 behavior.
- Round-trip test: save observation via glia → push to claude-mem → read back via `GET /api/observations` → canonical hash matches (modulo dropped fields documented in §5.2).
- Loop test: claude-mem-origin observation read in sync N is NOT pushed in sync N+1.
- Drift test: canonical revision bump triggers warning, not duplicate push.

## 12. Verified sources

- `https://github.com/thedotmack/claude-mem/blob/main/src/services/worker/http/routes/MemoryRoutes.ts` — write endpoint + schema.
- `https://github.com/thedotmack/claude-mem/blob/main/src/server/routes/v1/ServerV1Routes.ts` — server-beta routes (not used in v1.x).
- `https://github.com/thedotmack/claude-mem/blob/main/docs/migration-worker-to-server.md` — coexistence model.
- `https://github.com/thedotmack/claude-mem/blob/main/docs/server.md` — server-beta auth requirements.
