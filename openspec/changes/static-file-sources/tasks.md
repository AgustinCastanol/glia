# Tasks: static-file-sources (PRD-11 openspec read-only source)

## Delivery: two chained PRs (ask-on-risk threshold exceeded)

### PR-A boundary: Foundation (Tasks 1–5)
Core infrastructure: validKinds amendment, openspec adapter package, config schema + merge + Default(), wiring, and the pull-leakage safety test. All engine behaviour is correct after PR-A merges. No UI changes.

### PR-B boundary: Observability + fixtures + docs (Tasks 6–9)
Status CLI sources line, TUI kind/type filter extension, synthetic openspec fixture tree for integration/e2e, PRD-10 doc update. Depends on PR-A.

---

## PR-A — Foundation

### Task 1 · validKinds + PRD-0 amendment [SEQUENTIAL — must be FIRST]
- `internal/store/record.go` — add `"spec_artifact": true` to `validKinds`; update error message.
- `prds/PRD-0-canonical-schema.md` — add `spec_artifact` row to §5.1 kind table.
- Tests: spec_artifact accepted, unknown kind still rejected, error string mentions spec_artifact.

### Task 2 · openspec adapter package
- `internal/source/openspec/openspec.go` — new package implementing `adapter.Adapter` (Name, Health, ListNative, ReadNative, ToCanonical, FromCanonical→ErrUnsupported, WriteNative→ErrUnsupported, SupportedKinds, WriteCapability).

### Task 3 · config schema + merge + Default()
- `internal/config/config.go`, `load.go` — `sources.openspec` {enabled, path}, four-layer merge, tilde expansion.

### Task 4 · wiring
- `cmd/glia/cmd/wiring.go` — register `openspec` under buildAdapters when enabled.

### Task 5 · pull-leakage safety test
- `internal/sync/` — engine-level test: openspec dir byte-identical after Pull().

---

## PR-B — Observability + fixtures + docs

### Task 6 · status CLI sources block
- `internal/sync/report.go`, `cmd/glia/cmd/status.go` — SourceStatus + render distinct from providers.

### Task 7 · TUI kind/type filters
- `internal/tui/observations.go` — spec_artifact in kind cycle; proposal|spec|design|tasks in type cycle.

### Task 8 · synthetic fixture + integration/e2e
- `internal/source/openspec/testdata/`, integration + e2e tests for end-to-end ingestion.

### Task 9 · PRD-10 deferral doc
- `prds/PRD-10-claude-mem-static-file-source.md` — Status: Deferred, with evidence.
