# Security Policy

## Supported versions

glia follows semantic versioning. Security fixes are released for the **latest
minor version** only. Please upgrade to the most recent release before reporting
an issue.

| Version | Supported          |
| ------- | ------------------ |
| 1.3.x   | :white_check_mark: |
| < 1.3   | :x:                |

## Reporting a vulnerability

**Please do not open a public issue for security vulnerabilities.**

Report privately through GitHub's
[private vulnerability reporting](https://github.com/AgustinCastanol/glia/security/advisories/new)
— this opens a confidential advisory visible only to the maintainers.

If you cannot use GitHub advisories, email **castanolagustin@gmail.com** with:

- A description of the vulnerability and its impact.
- Steps to reproduce (a proof of concept if possible).
- The glia version (`glia version`) and your OS.

### What to expect

- **Acknowledgement** within 72 hours.
- An initial assessment and severity classification within 7 days.
- Coordinated disclosure: we will agree on a timeline before any public
  disclosure, and credit you in the advisory unless you prefer to stay anonymous.

## Scope

glia is a local-first CLI that brokers memory between AI providers. Security-
relevant areas include, but are not limited to:

- Handling of the local canonical store under `.glia/` (path traversal, unsafe
  file writes, lock handling).
- Provider transports (HTTP calls to local engram / claude-mem daemons) and how
  responses are parsed.
- Subprocess execution (`glia` shelling out to provider CLIs).
- Any code path that reads or writes credentials, tokens, or user data.

### Out of scope

- Vulnerabilities in the upstream providers themselves (engram, claude-mem) —
  report those to their respective projects.
- Issues that require a pre-compromised local machine or a malicious local user
  with filesystem access (glia trusts the local environment it runs in).
