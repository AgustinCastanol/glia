# PRD-1 — Adapter Contract + Engram Adapter

**Status:** Draft
**Owner:** @agustincastanol
**Last updated:** 2026-05-12
**Depends on:** PRD-0 (canonical schema)

---

## 1. Context

PRD-0 defined the neutral canonical memory format that lives in `.glia/memory.jsonl`. PRD-1 defines:

1. The **Adapter contract** — the Go interface every provider plugin must implement.
2. The **Engram adapter** — the reference implementation, since engram is the most structured of the v1 providers and serves as the validation target for the contract.

The architecture decision from PRD-0 is preserved: engram and `glia` coexist. `engram sync` keeps working standalone; `glia` mirrors engram-origin content into the canonical JSONL so that claude-mem users can also see it.

## 2. Goals

- Define a small, stable Go interface (`Adapter`) that any provider plugin can implement.
- Implement that interface for engram via the `engram` CLI (subprocess).
- Specify the field-by-field mapping between engram observations and canonical records.
- Specify round-trip guarantees: what is preserved, what is degraded, what is dropped (with reasons).
- Specify mirror semantics: when glia also drives `engram sync` and when it doesn't.

## 3. Non-Goals (this PRD)

- The orchestration layer (when/how `Adapter` methods are invoked) → PRD-3 (sync engine).
- The claude-mem adapter → PRD-2.
- TUI → PRD-4.
- Authentication / multi-user identity beyond what engram already provides.

## 4. The `Adapter` Interface

```go
package adapter

import (
    "context"
    "time"
)

// NativeID is opaque, provider-specific.
type NativeID string

// CanonicalID is a ULID, defined in PRD-0.
type CanonicalID string

// NativeRecord is whatever the provider speaks. Adapters use a concrete type internally.
type NativeRecord any

// CanonicalRecord matches the JSONL schema in PRD-0 §5.
type CanonicalRecord struct {
    CanonicalID    CanonicalID       `json:"canonical_id"`
    SchemaVersion  int               `json:"schema_version"`
    Kind           string            `json:"kind"`             // observation | session_summary | relation
    Revision       int               `json:"revision"`
    Supersedes     *CanonicalID      `json:"supersedes,omitempty"`
    Deleted        bool              `json:"deleted"`

    Title          string            `json:"title"`
    Type           string            `json:"type,omitempty"`
    TopicKey       string            `json:"topic_key,omitempty"`
    Tags           []string          `json:"tags,omitempty"`

    Content        string            `json:"content"`
    ContentFormat  string            `json:"content_format"`   // markdown | plain | structured

    Origin         Origin            `json:"origin"`

    CreatedAt      time.Time         `json:"created_at"`
    UpdatedAt      time.Time         `json:"updated_at"`
}

type Origin struct {
    Provider   string `json:"provider"`
    ProviderID string `json:"provider_id"`
    Author     string `json:"author,omitempty"`
    SessionID  string `json:"session_id,omitempty"`
}

// Adapter is the contract every provider plugin must satisfy.
type Adapter interface {
    // Name returns the stable identifier of the provider, e.g. "engram", "claude-mem".
    // Must match `origin.provider` in canonical records authored by this adapter.
    Name() string

    // Health performs a connectivity / version check. Returns nil if usable.
    Health(ctx context.Context) error

    // ListNative returns IDs of native records updated at or after `since`.
    // `project` is the canonical project name (per PRD-0 §9).
    // Records with provider-side personal/private scope MUST be filtered out here.
    ListNative(ctx context.Context, project string, since time.Time) ([]NativeID, error)

    // ReadNative fetches a single native record by ID.
    ReadNative(ctx context.Context, id NativeID) (NativeRecord, error)

    // ToCanonical converts a native record into a canonical record.
    // It is the adapter's responsibility to invent a canonical_id if this is the
    // first time the record enters the canonical store; otherwise it must reuse
    // the existing canonical_id (looked up via the IDMap, see §6).
    ToCanonical(native NativeRecord, idmap IDMap) (CanonicalRecord, error)

    // FromCanonical converts a canonical record into the provider's native form.
    // The adapter MAY mutate state (write the record to the provider) here or
    // it MAY return a record and let the caller decide — the orchestrator (PRD-3)
    // dictates which. v1 contract: this method MUST be pure (no side effects);
    // writing is done via WriteNative.
    FromCanonical(canonical CanonicalRecord) (NativeRecord, error)

    // WriteNative persists a native record in the provider. Returns the assigned
    // NativeID (the provider may rewrite the ID on insert).
    WriteNative(ctx context.Context, record NativeRecord) (NativeID, error)
}

// IDMap is a read-only view of the canonical ↔ native ID mapping for THIS adapter.
type IDMap interface {
    // CanonicalFromNative returns the canonical ID for a provider-native ID, if known.
    CanonicalFromNative(NativeID) (CanonicalID, bool)
    // NativeFromCanonical returns the provider-native ID for a canonical ID, if known.
    NativeFromCanonical(CanonicalID) (NativeID, bool)
}
```

### 4.1 Contract guarantees

- **Purity**: `ToCanonical` and `FromCanonical` are pure functions. They MUST NOT call the provider. This makes the orchestrator deterministic and adapters trivially testable.
- **Side-effect surface**: only `ListNative`, `ReadNative`, `WriteNative` may touch the provider. `Health` is allowed to do a single read.
- **Filtering at the boundary**: any provider concept of "personal / private" scope MUST be filtered inside `ListNative`. The canonical store never sees personal data.
- **Idempotence**: `WriteNative` of a record whose `origin.provider_id` already exists in the provider MUST update in place, not duplicate.

### 4.2 Errors

Standard Go errors. The package defines a small set of sentinels:

```go
var (
    ErrUnsupported = errors.New("adapter: operation unsupported by this provider")
    ErrNotFound    = errors.New("adapter: native record not found")
    ErrUnavailable = errors.New("adapter: provider not reachable")
)
```

## 5. Engram Adapter

### 5.1 Transport: CLI subprocess

The engram adapter shells out to the `engram` CLI. Decided over HTTP API for v1 because:

- No daemon to keep alive (`engram serve` would need port management, lifecycle).
- engram CLI is the supported user-facing surface; less likely to break than scraping the HTTP server.
- Sync operations are infrequent and not latency-sensitive.

Upgrade to HTTP (`engram serve` on :7437) is tracked as a v2 optimization and exposed via a config flag (`--engram-transport=http`).

The adapter executes:

| Operation     | Command                                                                                  |
|---------------|------------------------------------------------------------------------------------------|
| Health        | `engram version`                                                                         |
| ListNative    | `engram search "" --project <P> --limit 1000 --scope project` (then filter by `updated_at >= since`) |
| ReadNative    | Engram does not expose `read` by ID directly via CLI in v1.15. The adapter uses `engram search` with the title or sync_id as query, then matches. **See §9 open question 1.** |
| WriteNative   | `engram save <title> <content> --type <T> --project <P> --scope project` (and `engram update` for revisions when available) |
| Mirror to git | `engram sync --project <P>` — invoked at the end of a glia push cycle, optional via flag. |

### 5.2 Engram native record (internal type)

```go
type EngramRecord struct {
    ID         string            // engram's sync_id or numeric id
    Title      string
    Type       string            // decision | architecture | bugfix | discovery | pattern | config | preference | feature
    Content    string            // markdown body
    TopicKey   string            // optional
    Project    string
    Scope      string            // "project" | "personal"
    SessionID  string
    CreatedAt  time.Time
    UpdatedAt  time.Time
}
```

### 5.3 Field mapping (engram → canonical)

| Engram field | Canonical field         | Notes                                                                          |
|--------------|-------------------------|--------------------------------------------------------------------------------|
| `id`         | `origin.provider_id`    | Engram's `sync_id` preferred (stable across DBs) over numeric id.              |
| `title`      | `title`                 | 1:1.                                                                            |
| `type`       | `type`                  | 1:1, free string per PRD-0 §5.2.                                                |
| `content`    | `content`               | 1:1. `content_format = "markdown"`.                                            |
| `topic_key`  | `topic_key`             | 1:1.                                                                            |
| `project`    | (matched against repo)  | If engram project ≠ repo's glia project, the record is skipped.        |
| `scope`      | (filter)                | If `personal` → record excluded from canonical entirely.                       |
| `session_id` | `origin.session_id`     | 1:1.                                                                            |
| `created_at` | `created_at`            | 1:1.                                                                            |
| `updated_at` | `updated_at`            | 1:1.                                                                            |
| —            | `kind`                  | Always `"observation"` for engram-origin records.                              |
| —            | `canonical_id`          | Looked up via `IDMap` or generated (ULID) if first time.                       |
| —            | `revision`              | Incremented on each engram update (best effort via `updated_at` monotonicity).|
| —            | `origin.provider`       | Always `"engram"`.                                                              |
| —            | `origin.author`         | `os.Hostname() + ":" + os.Getenv("USER")` at write time. Best-effort.          |

### 5.4 Field mapping (canonical → engram)

| Canonical field      | Engram field       | Notes                                                                          |
|----------------------|--------------------|--------------------------------------------------------------------------------|
| `title`              | `title`            | 1:1.                                                                            |
| `type` (when present)| `type`             | 1:1.                                                                            |
| `content`            | `content`          | 1:1.                                                                            |
| `topic_key`          | `topic_key`        | 1:1.                                                                            |
| `tags`               | (lost)             | Engram CLI v1.15 does not expose a tags field on save. Tracked as PRD-1 §9.2. |
| `origin.session_id`  | `session_id`       | Best effort; engram may not accept it from external save. **§9.3**.            |
| `created_at`         | (engram-set)       | Engram assigns its own timestamps on save. Canonical timestamp preserved in JSONL. |
| `kind == "session_summary"` | type=`session_summary` content=raw narrative | Imported into engram as a typed observation so engram users can browse. |
| `kind == "relation"` | (skipped)          | Engram has its own relations model; v1 does not import canonical relations into engram. **§9.4** |

### 5.5 Round-trip guarantees

For engram-origin observations entering canonical and coming back:

| Field                | Lossless | Notes                                                            |
|----------------------|----------|------------------------------------------------------------------|
| title, type, content | ✅       |                                                                  |
| topic_key            | ✅       |                                                                  |
| session_id           | ⚠️       | Preserved in canonical; may not be re-injectable into engram.    |
| created_at           | ⚠️       | Engram assigns its own on re-import.                             |
| relations            | ❌       | Engram's relations (supersedes/conflicts_with/related/...) are NOT round-tripped in v1. See §9.4. |
| personal scope       | n/a      | Filtered out, never enters canonical.                            |

### 5.6 Mirror semantics with `engram sync`

By default, `glia sync` does NOT invoke `engram sync`. The two stay independent; the user can run either or both.

When the user passes `--mirror-engram` (or the project config sets `mirror_engram: true`), `glia sync push` additionally invokes `engram sync --project <P>` after writing the canonical JSONL, so that `.engram/` chunks are also up to date in the same commit. Symmetric for pull: `glia sync pull --mirror-engram` runs `engram sync --import` after pulling the canonical JSONL.

This is opt-in to avoid surprising engram-only users who manage `.engram/` themselves.

## 6. ID Mapping (engram-side)

`index.json` (gitignored, per PRD-0 §10.4) holds the per-user mapping:

```jsonc
{
  "by_provider": {
    "engram": {
      "obs-64324338ed6fd7cf": "01HXY..."   // engram sync_id -> canonical_id
    }
  }
}
```

When a new canonical record is imported into a fresh engram DB on another teammate's machine, engram assigns a NEW `sync_id`. That teammate's `index.json` records THEIR mapping. The canonical JSONL is the same across the team; only the local index differs.

## 7. Personal Scope Handling

Engram's `scope: personal` observations are filtered inside `ListNative` and never reach the canonical store. This is non-negotiable in v1 and aligns with PRD-0 §10.5.

If a user wants to share an observation they previously saved as personal, the path is: re-save it as `scope: project` in engram first.

## 8. Author Attribution

Canonical records carry `origin.author` for traceability (e.g. `agus@laptop`). Default is `os.Hostname() + ":" + os.Getenv("USER")`. Overridable via `WRAPPER_MEMS_AUTHOR` env var.

This is best-effort, not auth. Trust model: anyone with repo write access can author observations. Same trust boundary as git commits.

## 9. Open Questions

1. **Reading a single engram observation by sync_id**: engram CLI v1.15 doesn't expose `read <id>` directly. The adapter has to use `engram search` and match. Two viable solutions: (a) request a CLI flag upstream (`engram get <sync_id>`), (b) fall back to HTTP API for this single operation. **Leaning (b)** — HTTP API as a focused fallback even when the rest stays on CLI.
2. **Tags preservation**: engram CLI save command in v1.15 doesn't accept a tags array. Adapter loses tags on canonical → engram. Options: (a) accept the loss and document, (b) encode tags inline in content header, (c) wait for upstream support. **Leaning (a) with (c)** — document and request upstream.
3. **session_id on import**: when glia imports a foreign canonical record into a local engram, can `session_id` be preserved? Requires CLI flag or HTTP API write. Needs testing.
4. **Engram relations** (`supersedes`, `conflicts_with`, `related`, `compatible`, `scoped`, `not_conflict`): not round-tripped in v1. They could be modeled in canonical as `kind: relation` records (already reserved in PRD-0). This is a significant chunk of work — proposed for v1.1, NOT v1.0. Confirm the deferral.
5. **`updated_at` monotonicity**: does engram guarantee `updated_at` is monotonically increasing across updates? If not, `revision` tracking in canonical may misorder updates. Needs verification.

## 10. Decision Required Before PRD-2

None. PRD-2 (claude-mem adapter) can be drafted in parallel; it implements the same `Adapter` interface defined here.
