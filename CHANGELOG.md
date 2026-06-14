# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [1.4.0] - 2026-06-13

### Added

- **Read-only `openspec` source** (PRD-11): ingests SDD/OpenSpec artifacts
  (`proposal.md`, `design.md`, `tasks.md`, `specs/**/*.md`) from an `openspec/`
  directory into the canonical store, making them searchable in the TUI and
  pushable to providers without any daemon or network dependency.
  Enable with `sources.openspec.enabled: true` in `.glia/config.yaml`.
  Foundation in PR #11; `glia status` Sources block, TUI filters, and
  sync-engine ingestion fix in PR #12.

- **`glia status` Sources block**: when at least one read-only source is
  configured, status prints a separate `SOURCE / STATUS / WRITE_CAPABILITY /
  ARTIFACTS / NEWEST` table. The `--json` output gains a `sources` array
  (`name`, `write_capability`, `healthy`, `health_error`, `artifact_count`,
  `newest_artifact`).

### Fixed

- **`glia status` newest-artifact timestamp**: `NewestArtifact` in the Sources
  block now correctly reports the most-recently-modified artifact file per source
  (PRD-11 §10) (PR #14).

### Notes

- **claude-mem `supervisor.json` publishes no `port` field** (PR #13): the
  supervisor manifest does not include a port entry; the worker address is fixed
  at `localhost:37701`.

---

## [1.3.1] - 2026-06-02

### Added

- **`SECURITY.md` and `CODEOWNERS`** added for the OSS launch (PR #9).

### Fixed

- **TUI Observations/Conflicts views showed empty**: store files were read from
  the project root instead of the `.glia/` subdirectory; now read from `.glia/`
  (PR #10).

---

## [1.3.0] - 2026-06-01

### Fixed

- **TUI async loads route to the owning sub-model**: async data loads now
  dispatch messages to the correct sub-model; tab navigation redesigned to be
  intuitive (PR #7).

### Docs

- README rewritten as a public OSS guide; `docs/` promoted out of drafts;
  PRD-8, PRD-9, PRD-10, PRD-11 added (PR #8).

---

## [1.2.0] - 2026-05-29

### Added

- **Per-provider project override** (`providers.engram.project`, `providers.claudemem.project`):
  each provider block in `.glia/config.yaml` now accepts an optional `project` string field.
  When set, it overrides the global `Config.Project` for that provider only. Resolution
  order: `--project` CLI flag > `providers.<x>.project` > global `project`.

- **`glia status` surfaces effective project per provider**: the table output now includes
  an `EFFECTIVE_PROJECT` column and the `--json` output includes an `effective_project` map,
  showing the resolved project name each adapter will use for list and write operations.

### Fixed

- **engram write path now correctly stamps `project` on `FromCanonical`**: previously,
  `engram.FromCanonical` did not populate `rec.Project` from the adapter config, causing
  records pushed back to engram to lose their project association. The adapter now reads
  `a.cfg.Project` and sets it on the native record during conversion.

### Notes

- **claudemem write-path limitation**: `providers.claude-mem.project` affects READ
  filtering only (which records are pulled from the claude-mem worker into the canonical
  store). The claudemem write path posts a plain text payload and does not carry a project
  field to the server; project assignment on the write side is determined by the worker.
