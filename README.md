<div align="center">

# glia

**One project memory, shared across every AI memory provider.**

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/agustincastanol/glia?include_prereleases&sort=semver)](https://github.com/agustincastanol/glia/releases)
[![Telemetry: none](https://img.shields.io/badge/telemetry-none-success)](#privacy)

</div>

---

`glia` is a **memory broker**. It keeps a single, portable project memory in your git
repository — as a provider-agnostic **JSONL** store — and syncs it bidirectionally with the
AI memory tools your team already uses (engram, claude-mem, …).

No tool owns your memory. The canonical store does. Each provider is just a client that reads
from and writes to it through an adapter.

## Why glia

AI memory tools each invent their own storage: engram has a daemon, claude-mem has a worker
and a SQLite DB, the next one will have something else. Your knowledge ends up **fragmented and
locked in** — different formats, different machines, no shared history.

glia fixes that by making the source of truth a plain JSONL file that lives in your repo:

- **Portable** — independent of any single tool. Switch providers without losing memory.
- **Versioned** — it's in git. Full history, diffs, blame, branches, code review.
- **Bidirectional** — pull from providers into the store, push the store back out to them.
- **Auditable** — every record carries its origin, revision, and timestamps.

## How it works

```
┌──────────────┐     ┌──────────────┐
│  engram      │     │  claude-mem  │   ← AI memory providers
└──────┬───────┘     └──────┬───────┘
       │                    │
┌──────▼───────┐     ┌──────▼───────┐
│ Engram       │     │ Claude-Mem   │   ← Adapters (internal/adapter/*)
│ Adapter      │     │ Adapter      │
└──────┬───────┘     └──────┬───────┘
       └────────┬───────────┘
                │
       ┌────────▼────────┐
       │ Canonical Store │   ← .glia/  (committed to your repo)
       │  memory.jsonl   │      append-only JSONL log
       │  index.json     │      fast index (xxhash staleness check)
       │  schema.json    │      schema version guard
       └─────────────────┘
```

The store is **append-only**: updates and deletes are new revisions and tombstones, never
in-place mutations. That's what makes the git history meaningful and crash recovery trivial.
Deep dive: [`docs/internals.md`](docs/internals.md).

## Installation

Requires **Go 1.26+**.

```bash
go install github.com/agustincastanol/glia/cmd/glia@latest
```

Or build from source with an embedded version string:

```bash
git clone https://github.com/agustincastanol/glia
cd glia
go build -ldflags "-X github.com/agustincastanol/glia/cmd/glia/cmd.Version=v1.2.0" -o glia ./cmd/glia
```

**Provider prerequisites** (only the ones you enable):

- **engram** — the `engram` CLI on your `PATH`, daemon on `127.0.0.1:7437`.
- **claude-mem** — the worker running on `localhost:37701`.

## Quick start

```bash
# 1. Initialise the store + config in your repo
glia init --providers engram,claude-mem --project my-team

# 2. Check that providers are reachable and healthy
glia status

# 3. Sync — pull provider records in, push the canonical store out
glia sync

# 4. Browse what's in the store
glia show
```

Commit `.glia/` to share the memory with your team. The next person runs `glia sync` and their
providers light up with the shared history.

## Commands

Run `glia <command> --help` for full flag details.

| Command | What it does |
|---------|--------------|
| `glia init` | Initialise the `.glia/` store and config. Flags: `--providers`, `--project`, `--force` |
| `glia sync` | Bidirectional sync with all configured providers. Flags: `--dry-run`, `--provider`, `--mirror-engram` / `--no-mirror`, `--commit`, `--max` |
| `glia sync pull` | Pull provider records into the canonical store |
| `glia sync push` | Push canonical store records out to providers |
| `glia sync resolve <id>` | Resolve a sync conflict by choosing which duplicate to keep. Flag: `--keep` |
| `glia status` | Provider health, effective project per provider, last-sync timestamps. Flags: `--conflicts`, `--json` |
| `glia show` | List canonical records in the store. Flags: `--kind`, `--type`, `--json` |
| `glia doctor` | Health checks on the store and providers. Flag: `--fix` |
| `glia tui` (alias `glia ui`) | Open the interactive terminal dashboard |
| `glia version` | Print the binary version and supported schema range |

**Global flags:** `--dir` (project root, defaults to cwd), `--project` (project override),
`--verbose`.

## Configuration

`glia init` writes `.glia/config.yaml`. A full example:

```yaml
schema_version: 1
project: my-team                  # global project name (fallback for all providers)

providers:
  engram:
    enabled: true
    transport: cli
    cli_path: engram
    http_base_url: http://localhost:7437
    project: my-team              # optional: override the project for engram only

  claude-mem:
    enabled: true
    transport: http
    http_base_url: http://localhost:37701
    worker_pid_path: ~/.claude-mem/worker.pid

sync:
  mirror_engram: false            # run `engram sync` before/after to refresh the daemon
  default_action: full            # full | delta
  auto_commit: false              # git-commit .glia/ after sync (or use `glia sync --commit`)
  mirror_timeout_seconds: 5

privacy:
  excluded_session_ids: []        # session IDs never synced

identity:
  author: ""                      # stamped on records you push
```

### Per-provider project resolution

Each provider can use a different project namespace. Resolution order, highest priority first:

1. `--project <name>` CLI flag
2. `providers.<x>.project` (per-provider override)
3. `project:` (global fallback)

> **claude-mem write limitation:** `providers.claude-mem.project` affects **read filtering**
> (which records are pulled in), not the write side. The claude-mem worker assigns the project
> on saved records itself. See [`prds/PRD-7-claude-mem-write-support.md`](prds/PRD-7-claude-mem-write-support.md).

## Providers

| Provider | Transport | Capability |
|----------|-----------|------------|
| **engram** | CLI + HTTP daemon (`:7437`) | read + write |
| **claude-mem** | HTTP worker (`:37701`) | read + write (append-only; no update/delete) |

The adapter contract is provider-agnostic — adding a new provider means implementing one
interface. See [`docs/internals.md`](docs/internals.md) for the adapter contract.

## Privacy

**No telemetry. Ever.** glia never transmits usage data, crash reports, or analytics to any
external endpoint. All communication is with the local provider daemons you configure. Your
memory stays in your repo.

## Roadmap

| PRD | Status |
|-----|--------|
| PRD-0 – PRD-5 | Shipped — schema, adapters, sync engine, TUI, config |
| PRD-6 — per-provider project naming | Shipped (v1.2.0) |
| PRD-7 — claude-mem write support | Shipped |
| PRD-8 — claude-mem server-beta (updates) | Shelved — [decision record](prds/PRD-8-claude-mem-server-beta.md) |
| PRD-9 — local store backup & rollback | Draft — [proposal](prds/PRD-9-local-store-backup-rollback.md) |

## Documentation

- [`docs/internals.md`](docs/internals.md) — file-by-file architecture reference
- [`docs/architecture.md`](docs/architecture.md) — system design and data flows
- [`docs/concepts.md`](docs/concepts.md) — domain concepts (canonical store, revisions, tombstones)
- [`docs/step-by-step-guide.md`](docs/step-by-step-guide.md) — hands-on tutorial
- [`prds/`](prds/) — product requirements documents

## Contributing

```bash
go test ./...        # run the full suite
go vet ./...         # static checks
```

The store layer must never import the adapter layer — the dependency direction
(`adapter → store`) is enforced and load-bearing. Read [`docs/internals.md`](docs/internals.md)
before touching `internal/store`.

## License

[MIT](LICENSE) © 2026 Agustín Castañol
