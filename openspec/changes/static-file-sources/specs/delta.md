# Spec: static-file-sources (PRD-11 openspec read-only source; PRD-10 deferred)

## Domain Index

| Domain | Type | Added | Modified | Removed |
|--------|------|-------|----------|---------|
| canonical-schema | Delta | 1 | 1 | 0 |
| config-loading | Delta | 1 | 0 | 0 |
| sources/openspec | New full spec | 6 | 0 | 0 |
| sync-engine | Delta | 0 | 1 | 0 |
| status-cli | Delta | 1 | 0 | 0 |
| status-tui | Delta | 1 | 0 | 0 |
| prd-10-doc | Delta | 1 | 0 | 0 |

---

## Domain: canonical-schema

### MODIFIED Requirement: Kind Vocabulary

The `validKinds` map in `internal/store/record.go` MUST include `"spec_artifact"` alongside the existing three kinds (`observation`, `session_summary`, `relation`). The error message in `validateRecord` MUST reflect the updated set. The `type` field for `kind: spec_artifact` records MUST be one of `proposal | spec | design | tasks`; documented but not enforced by `validateRecord` (free-form `type` per PRD-0 §5.2).

#### Scenario: spec_artifact passes validation
- GIVEN a CanonicalRecord with `kind: "spec_artifact"` and `content_format: "markdown"`
- WHEN `validateRecord` is called
- THEN it returns nil (no error)

#### Scenario: unknown kind still rejected
- GIVEN a CanonicalRecord with `kind: "foo"`
- WHEN `validateRecord` is called
- THEN it returns `ErrInvalidRecord`

#### Scenario: error message lists spec_artifact
- GIVEN a CanonicalRecord with an invalid kind
- WHEN `validateRecord` returns an error
- THEN the error string mentions `spec_artifact` in the kind set

## Domain: sync-engine

### MODIFIED Requirement: Source ingestion in Push

Push MUST ingest read-only sources registered in the adapters map even when they are not enumerated in `cfg.Providers`. Pull MUST NOT iterate sources (read-only sources receive no export).

#### Scenario: openspec ingested by sync
- GIVEN `sources.openspec.enabled: true` and a populated `openspec/` tree
- WHEN `glia sync` runs with no live providers
- THEN the canonical store gains one `spec_artifact` record per artifact file

#### Scenario: openspec untouched by pull
- GIVEN a store containing `spec_artifact` records
- WHEN pull runs
- THEN the `openspec/` directory is byte-identical and no WriteNative is attempted
