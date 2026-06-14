# Proposal: static-file-sources (openspec read-only source; PRD-10 deferred)

## Intent
OpenSpec artifacts (proposal/spec/design/tasks markdown) are the *reasoning trail* behind a codebase but never reach any provider — they live only as on-disk files, invisible to glia's TUI search and to engram/claude-mem sync. Ingest them into the canonical store so the "why" is searchable and syncable alongside the observations they justify. PRD-10 (claude-mem file transport) is DEFERRED: exploration proved claude-mem v13.2.0 ships no static export surface (`server export` unimplemented upstream, no `exports/` dir exists), so there is nothing to read.

## Scope

### In Scope
- **PRD-0 amendment (sequenced FIRST)**: add `kind: spec_artifact` to `validKinds` in `internal/store/record.go`; `type` ∈ {proposal, spec, design, tasks}. Without this, `validateRecord` rejects every ingested artifact. Update PRD-0 §5.1 doc.
- **First-class read-only Source** for openspec (exploration Option A): a distinct config + wiring identity that internally satisfies the existing `adapter.Adapter` port. `WriteNative`/`FromCanonical` return `ErrUnsupported`; `SupportedKinds()` = `["spec_artifact"]`. Plugs into the same `map[string]adapter.Adapter` via `buildAdapters` (`out["openspec"]`).
- **openspec source impl**: walk `openspec/` (changes/<c>/{proposal,design,tasks}.md, changes/<c>/specs/**/*.md, specs/<domain>/spec.md), decode, field-map per PRD-11 §5, content-hash (sha256) revisioning per §6.
- **Config**: new top-level `sources.openspec` section (`enabled`, `path` default `openspec`) in `internal/config/config.go` + 4-layer merge + defaults.
- **Status output**: sources line in CLI `glia status` and TUI (changes count, artifact count, newest mtime; "disabled" when off).
- **Synthetic openspec fixture** for tests (no real openspec/ dependency).
- **PRD-10 doc update**: record blocked/deferred status with the exploration evidence.

### Out of Scope
- Writing canonical → openspec (no reverse mapping; the `.md` files are the source of truth).
- The claude-mem file transport (PRD-10) — deferred, upstream blocked.

## Resolved open questions
- §9.3 timestamps: mtime only (the no-op comparator ignores timestamps, so git buys no correctness).
- §9.4 state.yaml: skipped, not ingested.
- §9.5 archived changes: ingested, tagged `archived`.
- §9.6 removal: leave-stale (no tombstone on file disappearance).
