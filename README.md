# Damson

Damson is an open-source job orchestrator in the spirit of Rundeck and a modern, minimal Jenkins. You define jobs as JSON specs made of steps, and each step calls a plugin: SSH, WinRM, file transfer, Ansible, Buildah or Helm. Steps run on stateless agents that connect out to a highly available controller tier. The only required infrastructure is PostgreSQL.

> **Status: design and skeleton (milestone M0).** There is no release yet and nothing to run. [ARCHITECTURE.md](ARCHITECTURE.md) holds the v1 design.

## Why Damson

- **HA from day one.** You can restart any controller or agent at any time without losing runs. Controllers keep no durable state, and there is no Raft cluster.
- **Simple to run.** Damson is one static Go binary, either `damson controller` or `damson agent`, plus PostgreSQL. It has no message broker, no cache and no consensus cluster. SQLite works for development and single-controller installs.
- **Air-gap friendly.** Damson needs no internet access at runtime, and the web UI is embedded in the binary.
- **Agents connect out.** Nothing connects inbound to an agent, so agents work behind NAT, firewalls and in Kubernetes. You place a pool of agents in each network zone, and the agents reach targets over SSH, WinRM, SFTP, FTP or S3. Targets need no Damson software.
- **Real control flow.** Steps support `when` conditions, try/catch/finally groups, retries with exponential backoff, timeouts, cancellation and per-node fan-out.
- **Job specs are data.** Each job version is an immutable JSON spec. You can apply specs from a folder, and every run pins one version.
- **One expression language.** CEL handles conditions, node selectors, interpolation (`${{ … }}`), authorization rules and webhook mapping.
- **Secrets by reference.** Specs contain only `secret://` refs. Values are encrypted in the database (or held in OpenBao), delivered to the agent just in time and masked in logs.
- **OIDC and claims-based access.** CEL rules over IdP claims grant access on a tree of projects.
- **Public plugin SDK.** Built-in plugins use the same SDK as third-party plugins. Plugin forms in the UI are generated from JSON Schema.
- **Graph view.** Job and run graphs show live step status. The server builds them from the engine, so the UI never re-implements control flow.

## A job, briefly

```json
{
  "apiVersion": "damson.dev/v1",
  "kind": "Job",
  "metadata": { "project": "infra/billing", "name": "uptime" },
  "spec": {
    "targets": { "selector": "'report' in node.tags" },
    "steps": [
      {
        "id": "per-node", "type": "group", "for_each": "targets", "max_parallel": 5,
        "steps": [
          { "id": "check", "plugin": "ssh",
            "retry": { "max_attempts": 3, "backoff": { "initial": "5s", "multiplier": 2 } },
            "with": { "node": "${{ node.name }}", "command": "uptime" } }
        ]
      }
    ]
  }
}
```

For a full example with S3, try/catch/finally and rollback, see [ARCHITECTURE.md §5](ARCHITECTURE.md#5-job-spec-json).

## Architecture at a glance

```
Users / CI / webhooks ──HTTPS──► load balancer ──► controllers (stateless, N ≥ 2)
                                                        │
                                                   PostgreSQL (source of truth)
                                                        ▲
                     agents (per network zone) ──wss:// (agent-initiated)
                        │
                        └─ SSH / WinRM / SFTP / FTP / S3 ─► target nodes
```

| Area | Choice |
|---|---|
| Backend and agent | Go, one static binary, no cgo |
| Database | PostgreSQL (production, HA) and SQLite (dev, single controller); sqlc and goose |
| Agent protocol | WebSocket carrying JSON-RPC 2.0 encoded with MessagePack |
| Background work | River |
| Expressions | CEL |
| API | `net/http` and Huma (OpenAPI 3.1), with a generated TypeScript client |
| UI | SvelteKit SPA, shadcn-svelte, `@sjsf/form`, Svelte Flow |
| Secrets | Encrypted in the database (AES-256-GCM envelope), or OpenBao |
| Observability | OpenTelemetry and `log/slog` |

The reasoning behind each choice is recorded as an architecture decision record in [docs/architecture/decisions/](docs/architecture/decisions/index.rst).

## Documentation

The documentation is a Sphinx site in [docs/](docs/index.rst), built with [uv](https://docs.astral.sh/uv/):

```console
make docs          # build into docs/_build/html
make docs-serve    # live preview on http://127.0.0.1:8000
make docs-check    # strict build + linkcheck (run before committing docs)
```

- [ARCHITECTURE.md](ARCHITECTURE.md) is the design and the source of truth. The site renders it at `architecture/design`.
- [Concepts](docs/concepts/index.rst) covers projects, jobs, runs, agents, plugins, secrets and access.
- [Architecture](docs/architecture/index.rst) has the overview, components, data flows and security.
- [Decisions](docs/architecture/decisions/index.rst) holds the ADRs.
- [Development](docs/development/index.rst) describes the contributor workflow and how to write docs.

## Roadmap

Work proceeds in vertical slices:

| # | Slice |
|---|---|
| M0 | Skeleton: binary, config, both databases, CI, empty embedded SPA, plugin SDK v0 |
| M1 | Agent link: WebSocket protocol, pool tokens, heartbeat, reconnect |
| M2 | First run end to end: linear jobs, `ssh` plugin, dispatch with leases and fencing, live logs |
| M3 | Full engine and HA: control flow, multi-controller, chaos test, graph view |
| M4 | Secrets and inventory |
| M5 | `transfer`, `winrm`, plugin tooling |
| M6 | Ansible, Buildah, Helm |
| M7 | Schedules, webhooks, service accounts |
| M8 | OIDC, project tree, CEL access rules, audit |
| M9 | Production readiness: S3 logs, OpenBao, apply/export, Helm chart, offline bundle |

Proposed ADRs track the open questions, including GPL vs AGPL, YAML input, parallel groups and approval gates.

## Contributing

Read [ARCHITECTURE.md](ARCHITECTURE.md) and the [contributor workflow](docs/development/workflow.rst) first. A few rules matter most:

- The engine stays pure.
- Every state transition is a compare-and-swap.
- Store code is tested on both SQLite and PostgreSQL.
- Secret values are never logged or persisted.
- Generated code is never hand-edited.
- New dependencies must have a license compatible with GPL-3.0 ([ADR-0021](docs/architecture/decisions/0021-dependency-license-policy.rst)).

## License

Damson is licensed under [GPL-3.0-or-later](https://www.gnu.org/licenses/gpl-3.0.html).
