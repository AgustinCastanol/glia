# PRD-8 — claude-mem "full support" (server-beta transport)

**Status:** SHELVED (not pursued) — decision record
**Owner:** @agustincastanol
**Last updated:** 2026-05-29
**Depends on:** PRD-2 (claude-mem adapter), PRD-7 (claude-mem write support)
**Target release:** none (revisit only if conditions in §5 are met)

---

## 1. Why this PRD exists

This document records a decision, not a plan to build. It was opened to evaluate "full
claude-mem support" — specifically the ability to **read written metadata back** and to
**update** existing observations. Investigation showed both capabilities require a different
transport than the one glia uses today. The decision is to **not pursue it now**. This file
exists so the rationale is not re-litigated.

## 2. The two claude-mem write surfaces (verified 2026-05-29)

claude-mem exposes two distinct HTTP surfaces:

| | Worker (default, used by glia) | server-beta (the "API version") |
|---|---|---|
| Routes | `POST /api/memory/save` only | `POST /v1/memories`, `GET /v1/memories/:id`, `PATCH /v1/memories/:id`, `POST /v1/search`, `POST /v1/context`, `GET /v1/audit` |
| Read a single memory by id | ❌ none | ✅ `GET /v1/memories/:id` |
| Update / PATCH | ❌ append-only | ✅ any field except `projectId` |
| `metadata` round-trip | ❌ write-only (stored as JSON string, never returned) | ✅ (proper REST resource) |
| Auth | none (local-only) | required — scopes `memories:read` / `memories:write` |
| Process | already running on `:37701` | separate opt-in process |

### 2.1 Live evidence

- `GET http://localhost:37701/api/observations` on the running worker returns a rich schema
  (`narrative`, `facts`, `concepts`, `subtitle`, `files_read`, `files_modified`, …) but **no
  `metadata` field**. The schema evolved since PRD-7 but metadata is still not surfaced.
- Source `src/services/worker/http/routes/MemoryRoutes.ts` (main): the worker defines **only**
  `POST /api/memory/save`. metadata is persisted as `JSON.stringify(metadata)` but the response
  returns only `{ success, id, title, project, message }`.
- Source `src/server/routes/v1/ServerV1Routes.ts` (main): server-beta has `GET /v1/memories/:id`
  and `PATCH /v1/memories/:id`, all gated by `requireServerAuth`.

## 3. Key finding

"Read metadata back" and "use server-beta" are **the same feature**. There is no HTTP path on
the worker that returns metadata, so honest metadata round-trip (and real updates) is achievable
**only** via server-beta.

## 4. Decision (2026-05-29)

**Not pursued.** Rationale:

- glia already achieves idempotency with a **glia-local IDMap** (PRD-7 §6). Reading metadata
  back is a correctness *luxury* (honest round-trips), not a bug fix.
- server-beta costs the user a separate opt-in process plus API-key auth, just to gain that
  luxury.
- The cost/benefit does not justify the work at this time.

## 5. Conditions to revisit

Reopen this PRD if **any** of these become true:

- server-beta becomes the **default** claude-mem install (no extra process to run).
- The user explicitly needs **updates/deletes** of existing claude-mem observations (the worker
  cannot do this — PRD-7 §2.4).
- claude-mem exposes `metadata` in a worker `GET` response (would make metadata round-trip
  possible without server-beta).

## 6. Verified sources

- `https://github.com/thedotmack/claude-mem/blob/main/src/services/worker/http/routes/MemoryRoutes.ts`
- `https://github.com/thedotmack/claude-mem/blob/main/src/server/routes/v1/ServerV1Routes.ts`
- Live probe: `GET http://localhost:37701/api/observations?limit=1` (2026-05-29)
