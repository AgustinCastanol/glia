# PRD-0 — Canonical Memory Schema

**Status:** Draft
**Owner:** @agustincastanol
**Last updated:** 2026-05-12

---

## 1. Context

`glia` is a memory broker that lets a team share a single project memory across heterogeneous AI memory providers (engram, claude-mem, …). The architecture decision already taken is:

- **Source of truth lives in the project's git repo** (no remote backend in v1).
- **The repo stores a neutral canonical format**, not any provider's native format.
- **Each provider is a client** that imports from / exports to the canonical store via an adapter.

PRD-0 defines that canonical format. Everything else (adapters, sync engine, TUI, conflict resolution) depends on the contract specified here.

## 2. Goals

- Define a **provider-agnostic** serialization for memory that is:
  - **Lossless enough** for the v1 providers (engram, claude-mem) to round-trip without losing user-visible information.
  - **Diff-friendly** in git (line-oriented, append-mostly).
  - **Compact** (no file-per-observation explosion).
  - **Versioned** so we can evolve without breaking existing repos.
- Define the **ID strategy** that lets adapters map provider-native IDs ↔ canonical IDs.
- Define the **mutation semantics** (append, update, delete) compatible with git merges.

## 3. Non-Goals (v1)

- Remote backend, cloud sync, server component.
- Personal/global scope (only project scope).
- Real-time multi-writer concurrency (git is the merge boundary).
- Auto-translation of narrative content via LLM (we store what the provider gives us; we don't paraphrase).

## 4. Storage Layout

A single directory at the root of the project repo:

```
.glia/
  memory.jsonl       # canonical observations, one per line, append-only
  index.json         # canonical_id ↔ provider-native-id mappings (LOCAL, gitignored)
  schema.json        # schema version + provider registry for this repo
```

**Coexistence with `.engram/`** (decided 2026-05-12, see also §10.6): engram already ships a `engram sync` mechanism that maintains a `.engram/` directory of compressed chunks in the repo. `glia` does NOT replace it. Both directories coexist:

- `.engram/` is engram's native sync format, managed by `engram sync`. engram-only users may keep using it standalone.
- `.glia/memory.jsonl` is the neutral canonical store, managed by glia.
- The engram adapter mirrors content between the two so that a power user running `glia sync` produces/consumes both.

This preserves the replaceability property (the JSONL outlives any provider) without forcing the existing engram sub-team off their current workflow.

**Why JSONL, not many .md files:** one file, append-only, clean git diffs, no file explosion. Markdown-per-observation was rejected explicitly because it produces hundreds of files that swamp the repo.

**Why a dot-dir:** keeps it out of the way and signals "tooling state" to humans. We pick `.glia/` (not `.memory/`) to avoid colliding with anything else.

## 5. Canonical Observation Schema

Each line of `memory.jsonl` is one JSON object conforming to this schema:

```jsonc
{
  "canonical_id": "01HXY...",          // ULID, assigned by wrapper on first ingest
  "schema_version": 1,                 // matches schema.json
  "kind": "observation",               // see §5.1
  "revision": 1,                       // monotonic per canonical_id; 1 = first version
  "supersedes": null,                  // canonical_id of prior revision, or null
  "deleted": false,                    // soft-delete tombstone

  "title": "Fixed N+1 query in UserList",
  "type": "bugfix",                    // free-form vocabulary; providers map as they can
  "topic_key": "performance/user-list",// optional, stable string for evolving topics
  "tags": ["performance", "db"],       // optional

  "content": "What: ...\nWhy: ...\nWhere: ...\nLearned: ...",
  "content_format": "markdown",        // markdown | plain | structured

  "origin": {
    "provider": "engram",              // who first wrote this observation
    "provider_id": "obs_8421",         // provider-native ID at origin
    "author": "agus@laptop",           // git-like author hint (best effort)
    "session_id": "sess_..."           // optional, provider-specific
  },

  "created_at": "2026-05-12T14:03:00Z",
  "updated_at": "2026-05-12T14:03:00Z"
}
```

### 5.1 `kind` values

| kind                | Meaning                                                  | Primary provider source |
|---------------------|----------------------------------------------------------|-------------------------|
| `observation`       | Discrete typed fact (decision, bugfix, pattern, …)       | engram                  |
| `session_summary`   | Narrative compressed summary of a coding session         | claude-mem              |
| `relation`          | Edge between two canonical observations (supersedes, conflicts_with, related) | engram (relations model) |

This is the seam between engram's atomic model and claude-mem's session-narrative model. Adapters decide which kinds they consume; nothing forces a provider to surface kinds it can't represent natively.

### 5.2 `type` vocabulary

Free-form string, but the wrapper documents a **recommended vocabulary** mirroring engram's: `decision | architecture | bugfix | discovery | pattern | config | preference | feature`. Adapters that don't have a type system (claude-mem) leave it as `null`.

## 6. `index.json`

```jsonc
{
  "schema_version": 1,
  "canonical": {
    "01HXY...": {
      "latest_revision": 2,
      "latest_line": 1487,             // byte offset or line number for fast lookup
      "deleted": false
    }
  },
  "by_provider": {
    "engram": {
      "obs_8421": "01HXY..."
    },
    "claude-mem": {
      "overview_2026-05-12_a3f": "01HXY..."
    }
  }
}
```

**Purpose:** O(1) lookup from a provider-native ID to its canonical record, and from a canonical_id to its latest live revision. Rebuildable from `memory.jsonl` at any time — it's a cache, not a source of truth.

## 7. ID Strategy

- **canonical_id = ULID** (Crockford base32, timestamp-prefixed, sortable).
  - Generated by the wrapper at the moment an observation is **first ingested into the canonical store** (not by the provider).
  - Stable forever. Survives provider migrations.
- **provider_id** is whatever the originating provider uses (engram's UUID, claude-mem's overview filename, etc.). Stored only in `origin.provider_id` and in `index.json.by_provider`.
- An observation can have **multiple provider_ids over its lifetime** (e.g., re-imported into a fresh engram install with a new internal ID). The mapping in `index.json.by_provider` records the *current* binding per provider.

## 8. Mutation Semantics (append-only)

`memory.jsonl` is **append-only**. No line is ever modified or removed in place. This is what makes git merges sane.

- **Create**: append a new line with `revision: 1`, `supersedes: null`.
- **Update**: append a new line with the same `canonical_id`, `revision: N+1`, and `supersedes: <canonical_id of prior>`. Update `index.json.canonical[id].latest_revision`.
- **Delete**: append a tombstone line — same `canonical_id`, `revision: N+1`, `deleted: true`, empty content. Update `index.json` accordingly.

**Compaction** (not v1): a future tool can rewrite `memory.jsonl` to drop superseded revisions. It must require a clean working tree and produce a single squash commit.

## 9. Schema Versioning

`schema.json`:

```jsonc
{
  "schema_version": 1,
  "wrapper_mems_min_version": "0.1.0",
  "providers_enabled": ["engram", "claude-mem"],
  "source_of_truth_role": "shared",
  "id_strategy": "ulid"
}
```

Bumps to `schema_version` are breaking. The wrapper refuses to operate on a repo whose `schema_version` is newer than it knows.

## 10. Open Questions

These are NOT decided yet. Each will become its own PRD or be resolved before PRD-1.

1. ~~**(BLOCKER for PRD-1)** How do we ingest **claude-mem's session-narrative content**?~~ **RESOLVED 2026-05-12 → option (a)**: claude-mem narratives enter as `kind: session_summary` and live as a parallel stream. engram-side users can browse them but engram does not treat them as native observations. Option (c) — emitting derived `observation` records that link back — is deferred to v2 once the broker is in production. Rationale: extraction pipelines (LLM-based or heuristic) are a project in themselves and would dilute v1 scope. Honesty over false convergence.
2. How do adapters handle a `topic_key` evolving across providers (engram has it natively; claude-mem does not)?
3. Conflict resolution policy when two contributors append updates to the same `canonical_id` in parallel branches → both lines land after merge, but `index.json.latest_revision` will conflict in git. Spec a deterministic resolution (e.g. by `updated_at` then ULID tiebreak).
4. ~~Should `index.json` be committed?~~ **Resolved 2026-05-12**: NOT committed. Each user's engram has its own local IDs, so `by_provider.engram` mappings are per-user. `index.json` is regenerated locally from `memory.jsonl` on demand. Goes in `.gitignore`.
5. Privacy: should there be a way to mark an observation as `local_only` so it never enters the canonical store? (engram has `scope: personal` — glia MUST filter those out at the adapter boundary regardless.)
6. ~~Relationship with `engram sync`?~~ **Resolved 2026-05-12**: coexistence (option 3). See §4.

## 11. Decision Required Before PRD-1

~~**Open Question #1** is a hard blocker.~~ **Resolved 2026-05-12**: option (a). The claude-mem adapter will be a thin serializer in v1 (no LLM extraction). PRD-1 is unblocked.
