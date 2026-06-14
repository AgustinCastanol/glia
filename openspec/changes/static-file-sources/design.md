# Design: static-file-sources (openspec read-only source, PRD-11)

## Technical Approach
The openspec source is a read-only ingest adapter that satisfies the existing `adapter.Adapter` port. It only does I/O in `ListNative`/`ReadNative`; `FromCanonical`/`WriteNative` return `adapter.ErrUnsupported`. It registers in the same `map[string]adapter.Adapter` produced by `buildAdapters` under key `"openspec"`. Push ingests files; pull is structurally inert (proven below).

## Architecture Decisions

### D1: Reuse Adapter port instead of a new Source interface
**Choice**: openspec implements `adapter.Adapter`; asymmetry is enforced via ErrUnsupported on write methods + `SupportedKinds()=["spec_artifact"]`. **Rejected**: a parallel `Source` interface + engine branch. **Rationale**: the engine already tolerates read-only adapters (claude-mem path: pull.go skips ErrUnsupported silently). A new interface forces engine.go/push.go/pull.go edits and a second loop — large blast radius for zero behavioral gain. Config/status keep `sources` as a distinct user-facing identity (PRD-11 §9.2) WITHOUT a new internal port.

### D2: Pull-leakage is closed by ErrUnsupported, verified by test
**Mechanism**: pull.go iterates canonical records; for openspec it calls `FromCanonical` (returns ErrUnsupported → `continue`) — `WriteNative` is never reached. `SupportedKinds()=["spec_artifact"]` is a second guard but is NOT sufficient alone — the ErrUnsupported on FromCanonical is the real gate. Test asserts a full pull over a store containing spec_artifact records leaves `openspec/` byte-identical AND writes nothing.

### D3: No content_hash field on CanonicalRecord
**Choice**: hash is computed internally and used only to build `content` identity; revisioning rides the EXISTING `recordsEqualIgnoringMetadata` (equal.go), which already compares Title/Content/Type/TopicKey/Kind/Tags and ignores Origin and timestamps.

## Field mapping (PRD-11 §5)
- `origin.provider_id` = relative path; drives the IDMap.
- `title` = first H1, else `<change> — <artifact>`.
- `topic_key` = `sdd/<change>/<artifact>`, or `spec/<domain>` for merged specs.
- `type` = derived from filename (proposal | design | tasks | spec).
- `tags` = `["archived"]` when under `changes/archive/<change>/`, else empty.

## Deviation discovered during implementation
The "zero engine changes" claim above proved FALSE. Both Push and Status iterate `activeProviders()`, which filters by `cfg.Providers` (default `[engram, claude-mem]`) and excluded the openspec source. Fix: `Engine.activeSources()` so Push ingests read-only sources not in `cfg.Providers`; Status got the same loop via a `sourceStatus()` helper. Pull stays unchanged. Caught by the PR-B e2e.
