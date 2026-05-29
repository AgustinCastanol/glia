# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

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
