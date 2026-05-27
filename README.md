# glia

A **memory broker** that lets a team share a single project memory across heterogeneous AI memory providers (engram, claude-mem, …). The source of truth lives in the project's git repo as a provider-agnostic **JSONL** canonical store — not in any provider's native format.

Each provider is a client that imports/exports through an **adapter**, keeping the canonical store portable and independent of any single tool.

## Architecture Overview

```
┌──────────────┐     ┌──────────────┐
│  engram CLI  │     │  claude-mem  │   ← AI memory providers
└──────┬───────┘     └──────┬───────┘
       │                    │
┌──────▼───────┐     ┌──────▼───────┐
│ Engram       │     │ Claude-Mem   │   ← Adapters (internal/adapter/*)
│ Adapter      │     │ Adapter      │
└──────┬───────┘     └──────┬───────┘
       │                    │
       └────────┬───────────┘
                │
       ┌────────▼────────┐
       │ Canonical Store │   ← internal/store
       │  memory.jsonl   │
       │  index.json     │
       │  schema.json    │
       └─────────────────┘
```

**Import direction (enforced):** `internal/adapter/engram` → `internal/adapter` → `internal/store`.
The store MUST NOT import the adapter layer.

---

## Project Structure

```
glia/
├── cmd/
│   └── debug-store/          # Internal smoke-testing CLI (not user-facing)
│       └── main.go
├── internal/
│   ├── adapter/              # Provider-agnostic adapter contract
│   │   ├── adapter.go        # Adapter interface + sentinel errors
│   │   └── engram/           # Engram provider implementation
│   │       ├── engram.go     # Adapter logic (ToCanonical, FromCanonical, etc.)
│   │       ├── exec.go       # Commander: CLI subprocess abstraction
│   │       └── transport.go  # Transport: HTTP client for engram daemon
│   └── store/                # Append-only JSONL canonical store
│       ├── store.go          # Store struct, Open, Close, Append, AppendBatch, ReadLive, ReadAll
│       ├── record.go         # CanonicalRecord + Origin structs, validation, two-pass decode
│       ├── schema.go         # Schema versioning, loadOrBootstrapSchema, atomicWriteJSON
│       ├── index.go          # Index struct, fingerprinting (xxhash), load/persist
│       ├── rebuild.go        # Full-scan index rebuild + loadOrRebuild cache validation
│       ├── recover.go        # Crash recovery: truncate partial trailing lines
│       ├── tombstone.go      # Tombstone record builder + validation
│       ├── lock.go           # Advisory file lock (flock)
│       ├── paths.go          # File name constants
│       ├── ulid.go           # ULID generation interface + default impl
│       ├── errors.go         # Sentinel errors (ErrNotFound, ErrDeleted, etc.)
│       └── testdata/         # Golden test fixtures
├── prds/                     # Product Requirements Documents
│   ├── PRD-0-canonical-schema.md
│   ├── PRD-1-adapter-contract-engram.md
│   ├── PRD-2-claude-mem-adapter.md
│   ├── PRD-3-sync-engine.md
│   ├── PRD-4-tui.md
│   └── PRD-5-config-identity-install.md
├── go.mod
├── go.sum
└── .gitignore
```

---

## File-by-File Reference

### `internal/store/` — Canonical Store

The store manages the on-disk JSONL log. It is the single source of truth for all canonical records.

| File | Responsibility |
|------|---------------|
| **store.go** | Core `Store` struct. `Open()` runs the 7-step bootstrap: mkdir → lock → schema → recover → open-append → load-or-rebuild index → persist if dirty. `Close()` flushes, fsyncs, persists index, releases lock. `Append()` / `AppendBatch()` write records. `ReadLive()` reads the latest non-deleted revision by byte offset. `ReadAll()` returns the full revision chain via sequential scan. `ProviderIDMap()` exposes a read-only snapshot for adapter ID resolution. |
| **record.go** | `CanonicalRecord` and `Origin` struct definitions. `validateRecord()` enforces schema invariants (valid `kind`, required `content_format`, tombstone consistency). `decodeLine()` does a two-pass decode: pass 1 gates on `schema_version`, pass 2 does the full unmarshal. |
| **schema.go** | `loadOrBootstrapSchema()` — creates `schema.json` on first run, rejects it if the version is newer than the binary supports (`ErrSchemaTooNew`). `atomicWriteJSON()` writes any JSON file via temp + rename for crash safety. |
| **index.go** | `index` struct (entries map, by-provider map, fingerprint, line count). `loadIndex()` / `persist()` for JSON read/write. `computeFingerprint()` hashes `size + head(4KB) + tail(4KB)` with xxhash for O(1) staleness detection. |
| **rebuild.go** | `rebuildFromFile()` — full streaming scan of `memory.jsonl`, builds a fresh index with tiebreak selection (`revision DESC → updatedAt DESC → lineULID DESC`). `loadOrRebuild()` — tries cached index first, validates fingerprint + line count, rebuilds on mismatch. Also populates `ByProvider` from origin fields. |
| **recover.go** | `recoverPartialLine()` — crash recovery. Scans backwards for the last `\n`, truncates any partial trailing line left by an interrupted write. |
| **tombstone.go** | `buildTombstone()` constructs a tombstone record (deleted=true, self-referential supersedes). `validateTombstone()` enforces tombstone invariants. |
| **lock.go** | Advisory file lock via `gofrs/flock`. Non-blocking `tryAcquire()` returns `ErrLocked` if another process holds it. |
| **paths.go** | Constants: `memory.jsonl`, `index.json`, `schema.json`, `.lock`. |
| **ulid.go** | `ulidSource` interface for dependency injection. Default implementation wraps `oklog/ulid`. Tests inject deterministic sources. |
| **errors.go** | Sentinel errors: `ErrNotFound`, `ErrDeleted`, `ErrLocked`, `ErrSchemaTooNew`, `ErrInvalidRecord`. |

---

### `internal/adapter/` — Adapter Layer

The adapter layer defines the provider-agnostic contract and the engram-specific implementation.

| File | Responsibility |
|------|---------------|
| **adapter.go** | `Adapter` interface (6 methods: `Name`, `Health`, `ListNative`, `ReadNative`, `ToCanonical`, `FromCanonical`, `WriteNative`). `IDMap` interface for bidirectional native↔canonical ID resolution. Purity contract: `ToCanonical`/`FromCanonical` are pure (no I/O). Sentinel errors: `ErrUnsupported`, `ErrNotFound`, `ErrUnavailable`. |

#### `internal/adapter/engram/` — Engram Adapter

| File | Responsibility |
|------|---------------|
| **engram.go** | `EngramAdapter` struct implementing `adapter.Adapter`. `Health()` → `engram version`. `ListNative()` → `GET /export` via Transport, client-side filter by project + scope + since. `ReadNative()` → CLI search first, HTTP fallback. `ToCanonical()` → pure mapping from `EngramRecord` to `CanonicalRecord` with timestamp normalization (RFC3339Nano, fixed 9 digits). `FromCanonical()` → reverse mapping. `WriteNative()` → `engram save` via CLI. Also contains `WrapIDMap()` — boundary adapter that bridges the named-type gap between store (plain strings) and adapter (typed `NativeID`/`CanonicalID`). |
| **exec.go** | `Commander` interface + `execCommander` — shells out to the `engram` binary on PATH. Captures stdout/stderr. Injected as a dependency so tests use fakes. |
| **transport.go** | `Transport` interface + `httpTransport` — HTTP client for the local engram daemon (`127.0.0.1:7437`). `Export()` → `GET /export` (full dump). `GetByID()` → `GET /observations/:id`. Maps HTTP errors to sentinel errors. |

---

### `cmd/debug-store/` — Debug CLI

| File | Responsibility |
|------|---------------|
| **main.go** | Internal smoke-testing tool. NOT the user-facing CLI (that's PRD-5). Subcommands: `append` (write a test record), `read <id>`, `delete <id>` (tombstone), `rebuild` (force index rebuild). Opens a store at the given root dir and outputs JSON. |

---

### `prds/` — Product Requirements Documents

| Document | Scope |
|----------|-------|
| **PRD-0** | Canonical schema: JSONL format, ID strategy (ULID), mutation semantics (append-only), schema versioning. |
| **PRD-1** | Adapter contract + engram adapter requirements. |
| **PRD-2** | Claude-mem adapter (session narrative ingest). |
| **PRD-3** | Sync engine (orchestrates full sync cycle across providers). |
| **PRD-4** | TUI (terminal interface for browsing/searching memories). |
| **PRD-5** | Config, identity, and CLI install. |

---

## Data Flow

### 1. Store Bootstrap (`store.Open`)

```
MkdirAll → TryLock → loadOrBootstrapSchema → recoverPartialLine → open O_APPEND → loadOrRebuild → persist index
```

1. Create root directory if missing.
2. Acquire advisory file lock (single-process guarantee).
3. Read or create `schema.json`; reject if version too new.
4. Truncate any partial trailing line in `memory.jsonl` (crash recovery).
5. Open `memory.jsonl` in append mode with a 64 KB buffered writer.
6. Load `index.json` if it exists and its fingerprint/line-count matches; otherwise rebuild from a full scan.
7. Persist index to disk if it was rebuilt or didn't exist.

### 2. Write Path (`Append` / `AppendBatch`)

```
CanonicalRecord → computeAppendFields → validate → JSON marshal → buffer → (batch: flush + fsync + persist index)
```

1. Clone the index into a projected snapshot.
2. For each record: assign `canonical_id` (ULID if empty), compute `revision` and `supersedes`, assign `line_ulid`, set `schema_version`.
3. Validate invariants (valid kind, content_format, tombstone rules).
4. Marshal to JSON + `\n`, write to buffer, record byte offset.
5. On `AppendBatch`: single flush → fsync → update index → compute fingerprint → persist `index.json`.

### 3. Read Path (`ReadLive` / `ReadAll`)

- **ReadLive**: index lookup → seek to byte offset → decode single line.
- **ReadAll**: sequential scan of `memory.jsonl`, collect all lines matching the `canonical_id`.

### 4. Adapter Ingest (engram → canonical)

```
engram daemon/CLI → ListNative (GET /export) → ReadNative (CLI + HTTP) → ToCanonical → store.Append
```

1. `ListNative`: fetch full export via HTTP, filter by project + scope + `updated_at >= since`.
2. `ReadNative`: search by sync_id via CLI; if not found, fall back to HTTP `GET /observations/:id`.
3. `ToCanonical`: pure mapping — normalize timestamps, resolve IDs via `IDMap`, set origin.
4. Store receives the `CanonicalRecord` and handles ID assignment, revision tracking, and persistence.

### 5. Adapter Export (canonical → engram)

```
store.ReadLive → FromCanonical → WriteNative (engram save via CLI)
```

1. `FromCanonical`: map canonical fields back to `EngramRecord`.
2. `WriteNative`: execute `engram save` via CLI. If CLI doesn't echo the ID, do a follow-up search.

---

## Key Dependencies

| Dependency | Purpose |
|-----------|---------|
| `github.com/oklog/ulid/v2` | Monotonic, sortable ULID generation for `canonical_id` and `line_ulid`. |
| `github.com/cespare/xxhash/v2` | Fast non-cryptographic hash for index fingerprinting (staleness detection). |
| `github.com/gofrs/flock` | Advisory file locking (single-process access to the store). |
| `github.com/stretchr/testify` | Test assertions. |

---

## Quick Start (Development)

```bash
# Run all tests
go test ./...

# Use the debug tool
go run ./cmd/debug-store /tmp/test-store append
go run ./cmd/debug-store /tmp/test-store read <canonical_id>
go run ./cmd/debug-store /tmp/test-store delete <canonical_id>
go run ./cmd/debug-store /tmp/test-store rebuild
```
