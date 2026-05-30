# PRD-2 — claude-mem Adapter (read-only v1)

**Status:** Draft
**Owner:** @agustincastanol
**Last updated:** 2026-05-12
**Depends on:** PRD-0 (canonical schema), PRD-1 (adapter contract)

---

## 1. Context

PRD-1 defined the Go `Adapter` interface and implemented it for engram. PRD-2 implements that same interface for `claude-mem` (thedotmack/claude-mem, v13.2.0 at the time of writing).

claude-mem's design constrains us:

- **No public write surface.** The CLI exposes `search`, `transcript watch`, control commands (`start`/`stop`/`status`), and `server export`/`import` marked **"not yet implemented"**. There is no `save` / `add` / `observe` command. Capture is passive, via the worker watching Claude Code transcripts.
- **No CLI enumeration.** `claude-mem search ""` rejects empty queries; the CLI cannot list all observations.
- **HTTP API exists** on `http://localhost:<worker-port>` (default `37701`). Confirmed endpoints during PRD-2 research:
  - `GET /api/observations?limit=N&offset=N` → `{ items, hasMore, offset, limit }` paginated list.
  - `GET /api/projects` → 200 OK.
  - `GET /api/search` → 400 without params (search endpoint exists).
  - `GET /health` → `{ status, timestamp, activeSessions }`.

The architectural decision for v1 (decided 2026-05-12) is **option (a): claude-mem is read-only from glia**. The adapter reads claude-mem observations and pushes them into the canonical JSONL. It does NOT write canonical observations back into claude-mem. Bidirectional support is deferred to v1.1 once the HTTP API write surface is investigated.

## 2. Goals

- Implement the `Adapter` interface from PRD-1 for claude-mem, in v1 **read-only** mode.
- Document the field mapping claude-mem → canonical.
- Specify graceful behavior when claude-mem's worker is not running.
- Make the asymmetry (read-only) **visible and honest** to end users via `glia status`.

## 3. Non-Goals (v1)

- Writing observations into claude-mem (deferred to v1.1 — see §9.1).
- Reverse-engineering the HTTP API beyond `/api/observations`, `/api/projects`, `/api/search`, `/health`.
- Direct SQLite reads of `~/.claude-mem/claude-mem.db` (rejected — fragile across claude-mem versions).
- Triggering claude-mem captures (that's claude-mem's worker job).

## 4. Transport: HTTP

The claude-mem adapter speaks HTTP to the running worker (`localhost:37701` by default). Unlike the engram adapter (CLI subprocess), claude-mem requires HTTP because:

- The CLI cannot enumerate observations (no `list`, no empty-query `search`).
- The HTTP API already returns clean paginated JSON (`/api/observations`).
- The worker is meant to be long-running on the developer's machine (it's the capture daemon for Claude Code sessions). Requiring it to be up is not a hostile assumption.

The adapter's HTTP base URL is configurable:

```yaml
# .glia/config.yaml
providers:
  claude-mem:
    base_url: http://localhost:37701
    timeout: 10s
```

`Health()` performs `GET /health` and returns `ErrUnavailable` on connection failure. The orchestrator (PRD-3) will surface this as "claude-mem worker not running — start it with `claude-mem start`."

## 5. The native record (internal type)

> **STATUS: TO-CONFIRM on first real sample.** The actual JSON shape of an `item` from `GET /api/observations` is not verified yet (the test installation had zero observations). Fields below are the **expected minimal viable shape** based on claude-mem's documented model (observations + sessions + projects). The adapter MUST log the raw JSON of the first observation it sees and the field mapping MUST be reconfirmed before shipping.

```go
type ClaudeMemRecord struct {
    ID         string            // claude-mem-assigned observation ID (UUID or hash)
    ProjectID  string            // claude-mem project identifier (filesystem path or slug)
    SessionID  string            // capturing Claude Code session
    Title      string            // short summary (may be derived)
    Summary    string            // narrative content (the actual observation body)
    Tags       []string          // optional, claude-mem may extract these
    CreatedAt  time.Time
    UpdatedAt  time.Time
    // Raw is the original JSON, kept for forensics during PRD-2 validation.
    Raw        json.RawMessage   `json:"-"`
}
```

**Action item before merging this PRD into code**: run glia against a claude-mem instance with a real session; dump the first observation JSON; update §5 with the verified shape and §6 with the verified mapping.

## 6. Field mapping (claude-mem → canonical)

| claude-mem field | Canonical field           | Notes                                                                          |
|------------------|---------------------------|--------------------------------------------------------------------------------|
| `id`             | `origin.provider_id`      | 1:1.                                                                            |
| `project_id`     | (matched against repo)    | Adapter compares against the configured project. Mismatch → skip.              |
| `session_id`     | `origin.session_id`       | 1:1.                                                                            |
| `title`          | `title`                   | If absent, derive from first N chars of `summary`.                              |
| `summary`        | `content`                 | `content_format = "markdown"` (claude-mem produces markdown summaries).        |
| `tags`           | `tags`                    | 1:1 if present.                                                                 |
| `created_at`     | `created_at`              | 1:1.                                                                            |
| `updated_at`     | `updated_at`              | 1:1.                                                                            |
| —                | `kind`                    | **Always `"session_summary"`** per PRD-0 §10.1 decision (option a, disjoint streams). |
| —                | `type`                    | Empty / null. claude-mem has no engram-style type vocabulary.                  |
| —                | `topic_key`               | Empty. claude-mem has no topic_key concept.                                    |
| —                | `canonical_id`            | Looked up via `IDMap`, or generated (ULID) if first time.                      |
| —                | `revision`                | Incremented if claude-mem `updated_at` advances.                               |
| —                | `origin.provider`         | Always `"claude-mem"`.                                                          |
| —                | `origin.author`           | `os.Hostname() + ":" + os.Getenv("USER")` at write time.                       |

### 6.1 Why `kind: session_summary` (and never `observation`)

claude-mem observations are compressed session narratives, not atomic typed facts. Per PRD-0 §10.1 (resolved), we keep the two streams disjoint in v1. Mixing them under `kind: observation` would let engram users believe these are peer facts to their own observations, when they are summaries of an entire session's work. Honesty over false convergence.

The engram adapter, when importing canonical → engram, **does** include `kind: session_summary` records (per PRD-1 §5.4 last row) so engram users can browse them — but tagged as `type: session_summary` so they're visibly distinct.

## 7. Field mapping (canonical → claude-mem)

**Not applicable in v1.** `WriteNative` returns `adapter.ErrUnsupported`. `FromCanonical` is still implementable as a pure function (per the PRD-1 contract) but its return value is never persisted.

The pure `FromCanonical` is kept implemented anyway because:

- It validates that the canonical schema can be expressed in claude-mem's model (lossless round-trip checking).
- It will be needed verbatim in v1.1 when bidirectional support lands.

## 8. Listing strategy

`ListNative(ctx, project, since)` walks `/api/observations` with pagination:

```
GET /api/observations?limit=100&offset=0
GET /api/observations?limit=100&offset=100
...
```

until `hasMore: false`. Then filters in memory:

1. `project_id` matches the glia project (see §6).
2. `updated_at >= since`.

For projects with thousands of observations this is inefficient, but acceptable for v1. If `/api/observations` later supports server-side filters (`project=`, `since=`), the adapter switches to them transparently.

## 9. Open Questions

1. **v1.1 — write support via HTTP API**: investigate whether the claude-mem HTTP server accepts `POST /api/observations` or equivalent. If yes, design bidirectional flow as PRD-2.1. If no, escalate to claude-mem upstream as a feature request before shipping bidirectional.
2. **TO-CONFIRM** — actual JSON shape of an observation item. Required before merging the adapter implementation.
3. **Project identity mapping**: claude-mem identifies projects by filesystem path; glia uses its own project name (PRD-0 §9). Need a config field to map `claude-mem project path → glia project name`.
4. **Multiple glia projects on one machine**: a single claude-mem worker holds observations for ALL of a developer's Claude Code projects. The adapter MUST filter strictly to avoid leaking observations from `project-A` into `project-B`'s canonical store.
5. **Worker port discovery**: the worker writes its port to `~/.claude-mem/supervisor.json` and `worker.pid`. Adapter should read those instead of hardcoding `37701`, so it works after a port change.

## 10. Decision Required Before PRD-3

None blocking, but PRD-3 (sync engine) needs to know:

- claude-mem adapter is **read-only** in v1 → `glia sync` from canonical does NOT call `claude-mem.WriteNative`.
- claude-mem worker MUST be running for any sync involving claude-mem → `glia sync` must check `Health()` and degrade gracefully (warn + skip, not fail) if the worker is down.
