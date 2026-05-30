# PRD-5 — Config, Identity, Install, Privacy

**Status:** Draft
**Owner:** @agustincastanol
**Last updated:** 2026-05-12
**Depends on:** PRD-0..PRD-4

---

## 1. Context

PRDs 0-4 define the data, adapters, engine, and UI. PRD-5 wires the operational surface: where config lives, how `glia` is installed, who an "author" is, what privacy controls exist, and how the tool reports its own health.

This is the last v1 PRD. After this, the v1 scope is frozen and implementation starts.

## 2. Goals

- One obvious place for each kind of setting (project vs user) with deterministic precedence.
- Zero-friction install for a team that already has Go installed; one-liner for those who don't.
- Honest privacy controls: anything that the canonical store cannot keep private is documented as such.
- A `doctor` command that gives a single ground-truth answer to "is my setup OK?".
- No telemetry. Ever. Stated explicitly.

## 3. Non-Goals (v1)

- Encryption at rest. `memory.jsonl` is plaintext in git — same trust boundary as code.
- Per-observation ACLs.
- Multi-project per repo (one repo = one project; subprojects use separate `.glia/`).
- Cloud sync, auth tokens, user accounts.

## 4. Configuration

### 4.1 Files and precedence

Two layers, deepest wins:

1. **Project config** (committed to repo): `.glia/config.yaml`
2. **User config** (per-machine, never committed): `~/.config/glia/config.yaml`

User config **overrides** project config field-by-field. Use case: the rest of the team has `mirror_engram: true` but you personally don't want it.

Environment variables override both (see §4.4).

### 4.2 Project config shape

```yaml
# .glia/config.yaml
schema_version: 1
project: my-app                # the canonical project name for this repo

providers:
  engram:
    enabled: true
    transport: cli             # cli | http (cli default, see PRD-1 §5.1)
    cli_path: engram           # full path or name on PATH
    http_base_url: http://localhost:7437   # used only if transport=http

  claude-mem:
    enabled: true
    transport: http            # http only in v1 (see PRD-2 §4)
    http_base_url: http://localhost:37701  # default; auto-discovered from supervisor.json if absent
    worker_pid_path: ~/.claude-mem/worker.pid
    project_path_mapping:
      # claude-mem identifies projects by filesystem path; map them to glia project name
      "/Users/agustin/proyects/my-app": my-app
      "/Users/juan/work/my-app":         my-app

sync:
  mirror_engram: false         # opt-in (PRD-1 §5.6, PRD-3 §8)
  default_action: full         # full | pull | push (used by `glia sync` with no args)
  auto_commit: false           # if true, sync runs `git add .glia/ && git commit`

privacy:
  excluded_session_ids:        # PRD-2 gap mitigation, see §6.2
    - sess_abc123
```

### 4.3 User config shape

Same schema, but only the fields the user wants to override:

```yaml
# ~/.config/glia/config.yaml
sync:
  mirror_engram: true          # this user wants mirror even if project says false
identity:
  author: agus@personal-laptop
```

### 4.4 Environment variables

| Variable                    | Effect                                                  |
|-----------------------------|---------------------------------------------------------|
| `WRAPPER_MEMS_CONFIG`       | Override path to user config file                       |
| `WRAPPER_MEMS_PROJECT`      | Override `project` field (rare; useful for CI)          |
| `WRAPPER_MEMS_AUTHOR`       | Override `origin.author` for records this run produces  |
| `WRAPPER_MEMS_ENGRAM_BIN`   | Override `providers.engram.cli_path`                    |
| `WRAPPER_MEMS_CM_BASE_URL`  | Override `providers.claude-mem.http_base_url`           |
| `NO_COLOR`                  | Disable color output in CLI and TUI (respected)         |

## 5. `glia init`

```
glia init [--force] [--providers engram,claude-mem] [--project NAME]
```

Behavior:

1. Refuse if `.glia/` already exists, unless `--force`.
2. Detect project name: `--project` flag → git remote basename → directory basename → prompt.
3. Detect providers: probe `engram version` and `claude-mem status`. Default to whichever responds.
4. Create:
   - `.glia/schema.json` (PRD-0 §9 shape)
   - `.glia/config.yaml` (PRD-5 §4.2 shape, with detected values)
   - `.glia/memory.jsonl` (empty file with a single comment header line is NOT used — JSONL must be parsable, so file is truly empty)
5. Update `.gitignore`:
   - Add `.glia/index.json`
   - Add `.glia/.lock`
   - If `.gitignore` does not exist, create it.
6. Print a "next steps" block: how to run first sync, where docs live, how to invite teammates.

Non-interactive mode (`--providers ... --project ...` with all required flags) emits no prompts; required for CI.

## 6. Privacy Controls

### 6.1 What we can keep private

| Mechanism                                  | Effect                                                     |
|--------------------------------------------|------------------------------------------------------------|
| Engram `scope: personal`                   | Observation never read by the engram adapter (PRD-1 §7).   |
| `.gitignore` for `memory.jsonl` (DIY)      | Repo-wide opt-out (the whole canonical store stays local). |
| `privacy.excluded_session_ids` config      | Claude-mem sessions in this list are skipped by the adapter (see §6.2). |

### 6.2 The claude-mem session opt-out

claude-mem has no native equivalent of engram's `scope: personal`. The user cannot mark a Claude Code session as "do not share". To plug this gap WITHOUT requiring upstream changes, the config exposes:

```yaml
privacy:
  excluded_session_ids:
    - sess_abc123
    - sess_def456
```

The claude-mem adapter filters these out inside `ListNative()`. The list is in **project config** so it can be committed if the team agrees on shared exclusions, or in **user config** if the exclusion is personal.

**Honest limitation**: this is reactive (you have to know the session ID to exclude it). A future v1.1 may add `glia exclude <last>` to mark the most recent session.

### 6.3 What we cannot keep private (documented limits)

- Once an observation is pushed to canonical and committed, it is in git history forever (until manual rewrite).
- `origin.author` is a hostname/user hint, not authenticated. Anyone with repo write access can author.
- claude-mem session content is summarized BEFORE glia sees it. If the summary leaks information, that's a claude-mem concern, not ours.

## 7. Identity

Default `origin.author` = `os.Hostname() + ":" + os.Getenv("USER")` (e.g. `laptop-agus:agustin`).

Override precedence: `WRAPPER_MEMS_AUTHOR` env > user config `identity.author` > default.

There is NO authentication. The trust boundary is git: if you can push to the repo, you can author observations. Documented.

## 8. Distribution

### 8.1 v1.0 release channel

- **GitHub Releases**: prebuilt binaries for `darwin-arm64`, `darwin-amd64`, `linux-amd64`, `linux-arm64`. Checksums + signed if we set up signing.
- **`go install github.com/<org>/glia@latest`**: for devs with Go.

### 8.2 v1.1+ channels

- **Homebrew tap**: `brew install <org>/tap/glia`. Deferred to v1.1 to avoid maintaining a tap before the binary stabilizes.
- **Linux packages** (apt/rpm): deferred. Snap/Flatpak not planned.

### 8.3 Versioning

- Semantic versioning.
- The binary refuses to operate on a `.glia/` whose `wrapper_mems_min_version` exceeds itself.
- `glia version` prints binary version + canonical schema version compatibility range.

## 9. `glia doctor`

Single command, single ground-truth answer.

```
glia doctor
```

Output:

```
glia v0.1.0
project: my-app

✓ canonical store        .glia/memory.jsonl (1247 lines, valid)
✓ schema                 v1 compatible
✓ index.json             present, consistent with jsonl
✓ engram                 v1.15.10  ✓ reachable via cli
✓ claude-mem             v13.2.0   ✓ worker reachable :37701
⚠ git                    .glia/index.json not in .gitignore
✓ lock                   no stale lock

1 warning. Run `glia doctor --fix` to apply safe fixes.
```

`--fix` applies:

- Add missing `.gitignore` entries.
- Rebuild `index.json` if corrupt or out of sync.
- Remove stale `.lock` files (PID not alive).
- Truncate `memory.jsonl` to last complete line if a partial write was detected (PRD-3 §9 last-line-incomplete recovery).

`--fix` NEVER:

- Modifies provider data.
- Removes any line from `memory.jsonl`.
- Commits or pushes to git.

Exit codes: `0` healthy, `1` warnings, `2` errors that blocked checks.

## 10. Telemetry

**None. Not now, not later.** Stated explicitly in `--help` and README. No phone home, no anonymous usage stats, no crash reporting that ships content. If we ever add opt-in error reporting, it would require an explicit `telemetry.enabled: true` in user config AND a `glia telemetry enable` flow.

## 11. Open Questions

1. **Single binary or plugin model?** v1 ships all adapters compiled in. If we want third-party adapters someday (notion, jira-style memory tools), we'd need a plugin loader. Out of scope for v1; revisit when the third adapter is requested.
2. **Config schema migration**: when `schema_version` of config bumps, do we auto-migrate or refuse? Lean: refuse + emit a `glia migrate-config` command. Decide before v0.2 ships.
3. **`glia exclude` UX for claude-mem session opt-out**: command shape and where the latest session ID comes from (parse `.claude-mem/logs/`? query worker `/api/sessions?limit=1`?). Defer to v1.1 design.

## 12. v1 Scope Freeze

With PRD-5 complete, the v1 surface is frozen. Implementation order:

1. **Skeleton + canonical I/O** (PRD-0 §4, §5): read/write `memory.jsonl`, `schema.json`, `index.json`.
2. **Adapter interface + engram adapter** (PRD-1).
3. **claude-mem adapter** (PRD-2) — FIRST IMPLEMENTATION ACTION: dump a real `/api/observations` JSON, reconfirm §5-§6 mapping.
4. **Sync engine** (PRD-3) — collision tests are mandatory.
5. **CLI commands**: `init`, `sync`, `status`, `show`, `doctor` (PRD-3, PRD-5).
6. **TUI** (PRD-4).
7. **Distribution**: GitHub releases, version refusal logic (PRD-5 §8).

Each step is independently shippable and testable.
