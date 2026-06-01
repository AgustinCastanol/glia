# PRD-11 — openspec static artifact source

**Status:** Draft
**Owner:** @agustincastanol
**Last updated:** 2026-05-31
**Depends on:** PRD-0 (canonical schema), PRD-1 (adapter contract), PRD-3 (sync engine)
**Related:** PRD-10 (claude-mem static file source) — shares the static-source mechanism
**Target release:** next minor (v1.x)

---

## 1. Context

glia is a memory broker: it mirrors canonical records across providers (engram, claude-mem). But
a large amount of high-value project memory never reaches any provider because it lives as
**static files on disk** — specifically, the **OpenSpec** artifact trail produced by spec-driven
development:

```
openspec/
  changes/
    <change-name>/
      proposal.md
      specs/...            # delta specs
      design.md
      tasks.md
      state.yaml
  specs/
    <domain>/spec.md       # merged, current specs
```

These markdown artifacts are the *reasoning* behind a codebase — why a change was made, what was
specified, how it was designed, what tasks it broke into. Today glia can't see them, so they are
not searchable in the TUI and not synced to engram/claude-mem alongside the observations they
relate to.

This PRD adds a **read-only static source** that ingests OpenSpec artifacts into glia's canonical
store. It is the sibling of PRD-10: both read static on-disk files, never require a daemon, and
never write back to their source. The two PRDs should share one **static file source** mechanism,
pinned in the design phase.

## 2. Goals

- Add a **read-only source** that reads OpenSpec markdown/yaml artifacts from an `openspec/`
  directory into glia's canonical store, making them searchable and syncable like any record.
- Give each artifact a **stable canonical identity** so re-ingesting an unchanged file is a no-op,
  and editing a file produces a new revision (not a duplicate).
- Group a change's artifacts under a shared **`topic_key`** so they cluster naturally — reusing
  the `sdd/<change>/<artifact>` convention already used for SDD work in engram.
- Be **read-only**: never create, modify, or delete anything under `openspec/`.
- Make the openspec root **configurable** and default to the repo-local `openspec/`.

## 3. Non-Goals

- **Driving the SDD workflow.** glia does not run proposal/spec/design/tasks phases or generate
  OpenSpec artifacts. It only *reads* what the SDD tooling already wrote.
- **Writing canonical → openspec.** No reverse mapping; glia never authors spec files.
- **Deep markdown semantics.** glia does not parse requirement clauses, task checkboxes, or YAML
  internals into structured fields. The artifact's full markdown is stored as `content`; only
  minimal metadata (title, change, artifact kind) is extracted.
- **Bidirectional sync of artifacts back into the repo.** If an artifact record is edited in
  engram, glia does NOT write it back to the `.md` file. The file is the source of truth.

## 4. Source layout & what gets ingested

One canonical record **per artifact file**, not per change. Rationale: artifacts evolve
independently (a `tasks.md` changes far more often than its `proposal.md`), so per-file identity
gives precise revisioning and avoids re-emitting an entire change when one file changes.

Ingested by default:

| File | Ingested | Notes |
|------|----------|-------|
| `changes/<c>/proposal.md` | yes | |
| `changes/<c>/specs/**/*.md` | yes | delta specs; one record per file |
| `changes/<c>/design.md` | yes | |
| `changes/<c>/tasks.md` | yes | |
| `specs/<domain>/spec.md` | yes | current merged specs |
| `changes/<c>/state.yaml` | open question (§9.4) | metadata, not prose |

Archived changes (if OpenSpec moves completed changes to an `archive/` path) — see §9.5.

## 5. Field mapping (openspec artifact → canonical)

| Source | Canonical field | Notes |
|--------|-----------------|-------|
| relative path, e.g. `changes/auth/design.md` | `origin.provider_id` | Stable per-file key; drives the IDMap. |
| file content (full markdown) | `content` | `content_format = "markdown"`. |
| first H1, else `"<change> — <artifact>"` | `title` | e.g. `auth — design`. |
| `"openspec"` | `origin.provider` | New provider/source id. |
| `sdd/<change>/<artifact>` | `topic_key` | e.g. `sdd/auth/design`. Mirrors the engram SDD topic-key convention so artifacts cluster with their engram counterparts. For root `specs/<domain>` use `spec/<domain>`. |
| content hash (sha256) | drives `revision` | New hash on an existing `canonical_id` → revision + 1. |
| file mtime (or git — §9.3) | `created_at` / `updated_at` | |
| `kind` / `type` | **open question (§9.1)** | Needs a PRD-0 decision; see below. |
| — | `canonical_id` | From IDMap on `origin.provider_id`, or generated (ULID) first time. |
| — | `origin.author` | `os.Hostname() + ":" + USER` at ingest, per PRD-2 §6. |

### 5.1 `kind` / `type` — needs PRD-0 (open question §9.1)

PRD-0 §10.1 keeps streams **disjoint and honest**: claude-mem session narratives are
`kind: session_summary`, never `observation`, so users don't mistake them for peer facts. OpenSpec
artifacts are a **third** kind of content — design/spec documents, not observations and not session
summaries. Forcing them into an existing `kind` would repeat exactly the dishonesty PRD-0 warns
against.

**Proposed:** introduce `kind: spec_artifact` with `type` ∈ {`proposal`, `spec`, `design`,
`tasks`}. This requires a PRD-0 amendment and corresponding handling in the engram adapter
(PRD-1 §5.4) so engram users see them tagged distinctly. **Blocking decision before
implementation.**

## 6. Identity, revisioning & idempotency

- `canonical_id` is resolved via IDMap keyed on `origin.provider_id` (the relative artifact path),
  so re-ingesting is stable.
- A **content hash** decides revisioning: unchanged file → same hash → **no-op** (no new revision).
  Changed file → new hash → `revision + 1`.
- Deleted/moved artifacts: handled like any source removal — see §9.6 (tombstone vs leave-stale).

## 7. Project identity

The `openspec/` directory is repo-local, so its artifacts belong to the **current glia project**
(PRD-0 §9). Unlike claude-mem (one daemon, many projects), there is no cross-project leakage risk:
the source root is scoped to the repo. The openspec root is configurable for the rare case where
artifacts live outside the repo.

## 8. Config

```yaml
# .glia/config.yaml
sources:
  openspec:
    enabled: true
    path: openspec        # repo-relative; default "openspec"
```

Absent/`enabled: false` → openspec is not ingested (no behavior change for users who don't use
SDD). Note: openspec is modeled as a read-only **`source`**, not a bidirectional `provider`, to
make the asymmetry structural — see §9.2.

## 9. Open Questions

1. **`kind`/`type` vocabulary (blocking).** Amend PRD-0 to add `kind: spec_artifact` + the
   `proposal|spec|design|tasks` types, and update the engram adapter mapping. (§5.1.)
2. **`source` vs `provider` modeling.** claude-mem and engram are `providers` (sync targets);
   openspec and the claude-mem file transport are read-only **inputs**. Should glia formalize a
   `sources` concept distinct from `providers` (cleaner), or shoehorn openspec in as a read-only
   provider (less code, muddier semantics)? Leaning toward a first-class `sources` abstraction
   shared with PRD-10.
3. **Timestamps: mtime vs git.** File mtime is cheap but unreliable across clones/checkouts. Git
   (`git log -1 --format=%cI <file>`) gives authored time but couples ingest to a git repo and is
   slower. Leaning: git when the repo is available, mtime fallback.
4. **Ingest `state.yaml`?** It carries phase/status metadata, not prose. Option: skip it as a
   record but use it to tag the change's other artifacts (e.g. `status: archived`). (§4.)
5. **Archived changes.** If OpenSpec relocates completed changes (e.g. `changes/archive/<c>/`),
   ingest them too (with a tag) or skip? Leaning: ingest, tagged archived, so history stays
   searchable.
6. **Removal semantics.** When an artifact file disappears, should glia tombstone the canonical
   record (like a delete) or leave it as last-known? Leaning: leave stale by default; a future
   `--prune` reconciles. Tombstoning on file absence risks data loss if the path simply moved.

## 10. Status / observability

`glia status` gains a sources line:

```
sources:  openspec  (openspec/, 6 changes, 23 artifacts, newest 2026-05-31T01:40)
```

When disabled or empty:

```
sources:  openspec disabled
```

## 11. Acceptance Criteria

- With `sources.openspec.enabled: true`, `glia sync` ingests every artifact under `openspec/` into
  the canonical store as searchable records, with no daemon and no network.
- Each artifact maps to a record whose `topic_key` is `sdd/<change>/<artifact>` (or `spec/<domain>`
  for merged specs) and whose `content` is the full markdown.
- Re-running ingest on an **unchanged** tree produces **zero** new revisions (hash-stable no-op).
- Editing one artifact bumps **only that record's** revision; sibling artifacts of the same change
  are untouched.
- openspec is read-only: after any glia operation, the `openspec/` directory is byte-for-byte
  unchanged.
- `sources.openspec.enabled: false`/absent → openspec is never read; users not using SDD see no
  change.
- `glia status` reports the openspec source, change/artifact counts, and newest artifact time.
- The `kind`/`type` of ingested records follows the PRD-0 amendment (§5.1); records are visibly
  distinct from observations and session summaries in both the TUI and engram.
