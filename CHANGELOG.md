# Changelog

All notable changes to Damson are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Before 1.0, minor versions may change
behavior; read the notes before upgrading.

## [Unreleased]

### Added

- A project tree in the web UI sidebar: the projects a signed-in user holds a role on, nested by path, with their jobs, loaded one level at a time, a role badge per project and a "Shared with me" section for jobs reached only through a job's own rules. Nothing the user may not see is listed or hinted at. New `GET /api/v1/tree` and `GET /api/v1/tree/shared` (ADR-0075).

## [0.1.0] - 2026-11-10

First public release. Everything below was built across milestones M0 to M9
(see `ARCHITECTURE.md` section 21). This is young software: expect changes before 1.0.

### Core

- One static Go binary with two roles, `damson controller` and `damson agent`,
  plus `damson dev` (both in one process, SQLite) for development.
- PostgreSQL is the only required infrastructure; SQLite supports development and
  single-controller installs. Controllers keep no durable state: every transition is a
  compare-and-swap, agents are fenced by attempt, leases expire and the reaper retries.
- Pure engine with real control flow: `when`, groups, try/catch/finally, retries with
  backoff, timeouts, cancellation, `for_each` fan-out and `max_parallel`.
- Jobs as immutable JSON specs; CEL for conditions, selectors, interpolation and access rules.
- Live logs over SSE with ANSI rendering and resume; logs in the database or in S3.

### Plugins

- Built-in `ssh`, `winrm`, `transfer` (SFTP, SCP, FTP/FTPS, S3), `ansible`, `buildah` and `helm`
  plugins on the public plugin SDK; `damson plugin new|validate|run` and
  `damson build --with` for custom plugins.

### Inventory, secrets and access

- Node inventory with sync, frozen run targets, host-key policies (strict, TOFU).
- Secrets by reference: encrypted in the database (envelope encryption, KEK rotation) or in OpenBao;
  masked in logs.
- OIDC login, a project tree with CEL access rules and node scopes, service accounts and API tokens,
  webhooks, schedules, an audit trail.
- Jobs as code: `damson apply` and `damson export`.

### Operations

- Helm chart for controllers and agent pools; hardened systemd units; reproducible offline bundle
  with checksums; container images for the controller and two agent flavors.
- Native TLS on the controller (certificate reload without restart) or a TLS-terminating proxy.
- `damson migrate up|status` (migrations are forward-only), `/metrics` (Prometheus, separate token),
  `/healthz` and `/readyz`, agent drain on SIGTERM, opt-in run retention, backup and restore scripts.
- Operations guide: install, upgrade, backup, high availability, monitoring, hardening, troubleshooting.

### Known gaps

- OpenTelemetry traces and several metrics are not implemented.
- The Helm chart has not been run on a real cluster; the bundle, release assets and
  backup scripts are not signed (a signing mechanism exists, no release key is published yet).
- Run retention is global; there is no per-project policy.
- See the operations guide for the full list.

[0.1.0]: https://github.com/denyszorinets/damson/releases/tag/v0.1.0
