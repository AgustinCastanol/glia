# PRD-4 — Terminal UI (dashboard mode)

**Status:** Draft
**Owner:** @agustincastanol
**Last updated:** 2026-05-12
**Depends on:** PRD-0..PRD-3

---

## 1. Context

PRDs 0-3 deliver a working CLI. PRD-4 puts a face on it: a terminal UI for browsing `memory.jsonl`, inspecting status, resolving conflicts, and triggering syncs.

**Soul: A (dashboard, read-mostly)** — confirmed 2026-05-12. The TUI is invoked, used, closed. It is NOT a daemon. No background polling, no live activity stream. Soul B (always-on cockpit) is roadmap for v2.

## 2. Goals

- Make `memory.jsonl` browsable by humans (no one wants to read JSONL by hand).
- Surface provider health and last-sync timestamps at a glance.
- Surface conflicts in a way that makes resolution obvious.
- Trigger sync operations with one keystroke, with their output visible.
- Keep state ephemeral: TUI closes, nothing leaks (no state files written by the TUI itself).

## 3. Non-Goals (v1)

- Live updates / polling / activity feed (alma B, deferred).
- Editing observations from inside the TUI (read + targeted ops only; saves go through providers).
- Mouse support beyond what bubbletea gives for free.
- Multi-pane resizable layouts. Fixed layouts per tab.

## 4. Tech Stack

- **Framework**: `github.com/charmbracelet/bubbletea` (Elm architecture for Go).
- **Components**: `github.com/charmbracelet/bubbles` (list, table, viewport, textinput, spinner).
- **Styling**: `github.com/charmbracelet/lipgloss`.
- **Subprocess output**: pipe `glia sync` stdout into a viewport so the user sees progress in-place.

Entry point: `glia tui` (alias: `glia ui`).

## 5. Layout

```
┌─ glia ─ project: my-app ────────────────────────── q quit ─┐
│ [O]bservations  [C]onflicts (2)  [S]tatus  [?]Help                  │  ← tab bar
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  (tab content fills here, see §6)                                   │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│ engram ✓  claude-mem ✓     last sync 12m ago       press s to sync  │  ← status bar
└─────────────────────────────────────────────────────────────────────┘
```

- **Header**: project name, global quit hint.
- **Tab bar**: 1-key switch (O / C / S / ?). Conflict count in parens if > 0, in red.
- **Status bar**: provider health glyphs (✓ / ⚠ / ✗), last sync, sync shortcut hint.

## 6. Tabs

### 6.1 [O]bservations

Two-pane layout: list on the left, detail on the right.

```
┌─ Observations (1247) ──────────────────┬─ detail ───────────────────┐
│ filter: _                              │ 01HXY2K3...                │
│                                        │                            │
│ ▶ Fixed N+1 in UserList      bugfix    │ Title: Fixed N+1 ...       │
│   Adopted hexagonal layout   decision  │ Type:  bugfix              │
│   session 2026-05-12         session   │ Topic: performance/...     │
│   ...                                  │ Origin: engram (agus@...)  │
│                                        │ Created: 2026-05-12 10:03  │
│                                        │ Rev: 2 (supersedes 01HXX..)│
│                                        │                            │
│                                        │ ── content ──              │
│                                        │ What: ...                  │
│                                        │ Why: ...                   │
│                                        │ Where: ...                 │
│                                        │ Learned: ...               │
│                                        │                            │
│                                        │ [enter] full  [c] copy id  │
└────────────────────────────────────────┴────────────────────────────┘
```

**Keys**:

| key       | action                                                   |
|-----------|----------------------------------------------------------|
| `/`       | focus filter input                                       |
| `↑/k`     | move selection up                                        |
| `↓/j`     | down                                                     |
| `enter`   | open full-screen detail view (markdown-rendered)         |
| `c`       | copy `canonical_id` to clipboard                         |
| `f`       | cycle filter mode: all / observation / session_summary / relation |
| `t`       | cycle type filter (bugfix, decision, ...) — engram types |
| `g/G`     | go to top / bottom                                       |
| `esc`     | clear filter                                             |

**Filter syntax** (in the filter input): plain substring on title + content; prefix `type:bugfix` and `kind:session_summary` are recognized as structured filters; `provider:engram` filters by origin.

**Source**: reads `.glia/memory.jsonl` once on tab entry. No live refresh. Pressing `r` reloads.

### 6.2 [C]onflicts

List of unresolved revision collisions from `index.json.conflicts` (PRD-3 §7).

```
┌─ Conflicts (2) ────────────────────────────────────────────────────┐
│ ▶ 01HXY2K3... revision 3   detected 12m ago                        │
│   01HXZ8L4... revision 5   detected  1h ago                        │
└────────────────────────────────────────────────────────────────────┘

│ ── selected ──                                                     │
│ canonical_id: 01HXY2K3...                                          │
│ revision: 3                                                        │
│                                                                    │
│ winner   (line 1487)   updated 14:00:22  author agus@laptop        │
│   What: refactor X to use Y                                        │
│                                                                    │
│ loser    (line 1492)   updated 14:00:19  author juan@desktop       │
│   What: refactor X to use Z                                        │
│                                                                    │
│ [w] keep winner   [l] keep loser   [d] diff   [s] skip             │
```

**Keys**:

| key   | action                                                                |
|-------|-----------------------------------------------------------------------|
| `w`   | accept the deterministic winner (clear conflict entry, no write)      |
| `l`   | promote the loser: appends a new revision N+1 mirroring the loser's content. Clears entry. |
| `d`   | open a side-by-side diff of the two content bodies                    |
| `s`   | skip (leave conflict for later)                                       |

This tab is hidden (collapsed) when there are no conflicts. Tab bar shows `[C]onflicts (0)` greyed out.

### 6.3 [S]tatus

Provider health, last sync, watermarks, config flags.

```
┌─ Status ───────────────────────────────────────────────────────────┐
│ Project:   my-app                                                  │
│ Repo:      /Users/agustin/proyects/my-app                          │
│ Canonical: .glia/memory.jsonl  (1247 lines, 314 KB)        │
│ Schema:    v1                                                      │
│                                                                    │
│ Providers                                                          │
│ ──────────────────────────────────────────────────────────────────│
│ engram      ✓ healthy   v1.15.10                                   │
│             last pushed: 2026-05-12 09:00:00 (12m ago)             │
│             last pulled: 2026-05-12 09:00:00                       │
│             mirror engram sync: OFF                                │
│                                                                    │
│ claude-mem  ✓ healthy   v13.2.0   worker :37701                    │
│             last pushed: 2026-05-12 09:00:00                       │
│             last pulled: (read-only in v1)                         │
│                                                                    │
│ [s] sync now   [p] pull only   [P] push only   [m] toggle mirror   │
```

Pressing `s` / `p` / `P` shells out to `glia sync [...]` with output piped into a viewport overlay. Esc returns to the status view. After the run, watermarks and timestamps refresh.

### 6.4 [?] Help

Static cheatsheet listing the keybindings of the currently focused tab.

## 7. Sync invocation overlay

When the user triggers a sync from any tab:

```
┌─ glia sync ────────────────────────────────────────────────┐
│ engram      ✓ pushed 3 records, pulled 1                           │
│ claude-mem  ✓ pushed 12 records (read-only, skipping pull)         │
│                                                                    │
│ Done in 2.3s.                                                      │
│                                                                    │
│ [enter] close                                                      │
└────────────────────────────────────────────────────────────────────┘
```

A spinner during the run, then the result lines. The TUI does NOT reimplement sync — it spawns the same `glia sync` command the CLI exposes, so the two paths can never drift.

## 8. State and Lifecycle

- The TUI reads `.glia/memory.jsonl`, `.glia/index.json`, and `.glia/schema.json` on startup.
- It writes NOTHING on its own. Every mutating action (sync, conflict resolution) goes through the CLI subprocess, which owns all writes.
- Closing the TUI is `q` or `ctrl+c`. No confirmation prompts (nothing in-flight to lose; subprocess sync is foreground and blocks).

## 9. Performance

Target: open in under 100ms with a 100k-line `memory.jsonl`.

- Lazy parse: stream the file once into in-memory records, but only fully parse the content body when the row is selected.
- Filter is in-memory linear scan; 100k entries is fine.
- No persistent index beyond what `index.json` already provides.

## 10. Open Questions

1. **Markdown rendering** in the detail pane: use `github.com/charmbracelet/glamour`? Adds binary size but renders beautifully. Lean yes.
2. **Color theme**: detect terminal background or ship a single neutral theme? v1: single neutral theme (low contrast on both light and dark), `--theme dark|light` flag for v1.1.
3. **Conflict diff** (key `d` in §6.2): inline two-pane or shell out to `git diff --no-index`? Lean inline (consistent UX), use `github.com/sergi/go-diff` for the line-level diff.
4. **Should `glia` open the TUI when invoked with no args?** Two camps: (a) yes, friendliest, what new users expect; (b) no, CLI tools default to `--help`. Lean (a) — if the project is initialized, opening the TUI is more useful than printing help. If not initialized, print init instructions.

## 11. Decision Required Before PRD-5

None blocking. PRD-5 cleans up config, identity, env vars, install instructions.
