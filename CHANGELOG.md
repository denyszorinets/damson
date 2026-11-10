# Changelog

All notable changes to Damson are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Before 1.0, minor versions may change
behavior; read the notes before upgrading.

## [Unreleased]

### Added

- A project has its own sidebar with Overview (success rate over its newest runs and the latest runs), Jobs and Activity (runs filtered by status and job), all scoped to the project and the projects below it. `GET /api/v1/runs` takes a `project` filter; a project the caller holds no role on answers 404, as a missing one does.

- The web UI is project-first: Home is a table of the projects you hold a role on (role, child projects, jobs, runs failed in 24 hours) with "Shared with me" for jobs reached only through a job's own rules; a project switcher and a path breadcrumb sit in the top bar; the sidebar tree and the cross-project Jobs list are gone (`/jobs` redirects to Home), and a project's page is at `/projects/<path>`, with tabs to come at `/projects/<path>/-/<tab>`.

- The Audit page filters by action, actor, project and time range, shows the actor's name or email with the id as a tooltip, and hides `system:*` events (such as the dispatch `secret.read` rows) until you turn them on; `GET /api/v1/audit` takes `action`, `actor`, `from`, `to` and `hide_system`.
- A project tree in the web UI sidebar: the projects a signed-in user holds a role on, nested by path, with their jobs, loaded one level at a time, a role badge per project and a "Shared with me" section for jobs reached only through a job's own rules. Nothing the user may not see is listed or hinted at. New `GET /api/v1/tree` and `GET /api/v1/tree/shared` (ADR-0075).
- The Jobs page lists the jobs of every project the user can see; choosing a project in the sidebar shows that project and the ones below it at `/projects/<path>`, with a breadcrumb and an All / project selector. New `GET /api/v1/tree/jobs`. A project the user has no role on reads as not found.
- The job page matches the user's role: Run is disabled with "needs runner" for a viewer, schedules and webhooks show only for instance admins, and a job or run the user cannot see shows a not-found page with links back, the same as one that does not exist. `GET /api/v1/jobs/{id}` returns the caller's role as `job.my_role`.

- The run page has a log viewer: choose a step's Log to follow its execution's output live, with ANSI colours. It renders only the rows in view, keeps at most the newest 5000 lines in memory (saying how many earlier ones it dropped), applies each line once by sequence number, and reopens a dropped connection from the last sequence it saw.

- The run dialog shows what the server finds wrong with an option beside that field (a 422 with a located error such as `options.env`) instead of a banner, and no run is created. Errors about the options as a whole stay in a banner.

- Choosing a step's Log on the run page opens a step panel (status, error, and every attempt with its state, agent and times) and the log of its latest attempt; any attempt's log can be opened from the panel, and the log can be filtered to stdout or stderr.

- Sidebar tree nodes show the jobs below them, the runs that failed in the last 24 hours and the runs running now, over the projects the user can see only, each counted once.

- The home page lists the newest runs of the projects the user can see (failures highlighted first), with entry points to their projects, to all jobs and to jobs shared only through a job's rules, and an empty state for a user with no role. It refreshes every 10 seconds.

- Jobs can be created and versioned in the web UI by users with the editor role: **New job** on a project page and **Edit** on a job open a JSON editor; **Validate** asks the server, which reports errors by spec path and returns the job graph it builds, drawn beside the editor. Saving a new version is based on the version being edited and fails if another landed meanwhile. A job managed by a file source, or archived, opens read-only with the reason; a user without the editor role sees no New job or Edit control.

- A job page lists the job's versions (current one marked, with checksum and time) and compares any two as a line diff of their specs; a run page names the job version it ran and links to the job.

- Nodes page: a selector builder turns a field and a value into a quoted clause (os, transport, pool, name, tag, or an attribute with an identifier key) and adds it to the selector; selectors can be saved under a name, reused and deleted (kept in this browser). Agents page: a Health column tells a healthy agent from one that is connected but has not been heard from for 30 seconds, and from one that is offline. Secrets page: **Copy reference** copies the `secret://` reference (never a value).

- An **Access** page: every signed-in user sees their own roles and the kind of rule that gave each (direct, inherited from a named ancestor, or the job's own); an instance admin can enter groups, an optional email and a project or job to see the role they would hold and the rule that grants it, evaluated by the same code that enforces rules. New `GET /api/v1/access/me` and `POST /api/v1/access/check`; the check is audited as `access.check` without the groups or the email.

### Fixed

- The run graph no longer lets a step box overlap the next one or the end node when badges (when, retry, status, attempt, lanes) wrap: a step is as tall as its badges need.

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
