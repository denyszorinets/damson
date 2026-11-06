# Damson — Architecture

> **Name:** Damson (binary `damson`). Final namespace checks (GitHub org, Docker Hub/GHCR, domain, trademark) are pending.
> **License:** GPL-3.0-or-later.
> **Status:** v1 design, the starting point for implementation.

Damson is an open-source job orchestrator in the spirit of Rundeck and a modern, minimal Jenkins. Jobs are JSON specs made of steps. Each step calls a plugin with parameters. Steps run on stateless agent-workers that connect to a highly available controller tier over WebSockets.

---

## 1. Goals and non-goals

### Goals (v1)

- **HA from day one.** Any controller or agent can be restarted, bounced, or rescheduled at any time without losing runs.
- **Simple deployment** on VMs, Kubernetes, or a mix. One static binary, with no internal consensus cluster (no Raft).
- **Stateless agents.** Each step execution carries everything it needs: resolved spec and secrets.
- **Air-gap friendly.** No outbound internet dependencies at runtime. All UI assets are embedded.
- **Job specs as data:** JSON stored in the database, versioned, and importable from a folder. Git sync comes later.
- **Real control flow:** conditions, try/catch/finally, retries with exponential backoff, timeouts, node fan-out.
- **Built-in plugins:** Ansible, SSH, WinRM, file transfer (SFTP/SCP/FTP/S3), Buildah (build and push images from git), and Helm (deploy).
- **Plugin SDK from day one.** Built-in plugins use the same public SDK as third-party ones. In v1 all plugins are compiled in; the design keeps runtime-loaded `.so` plugins on workers possible later (§6).
- **Graph view of jobs and runs.** An activity-diagram-style view of the job structure, with live step status during a run.
- **Secrets** in the database (encrypted) by default, or OpenBao/Vault.
- **OIDC authentication** with CEL-based authorization over claims, on hierarchical projects.
- **Scale target:** 1,000 connected agents and roughly 500 concurrent step executions.
- **Less code.** Prefer generated code, schema-driven UI, and one expression language.

### Non-goals (v1)

- CI features beyond build-and-deploy steps: Damson-managed build caches, artifacts passed between pipelines, PR status checks.
- Dynamic inventory providers inside the app. Inventory is pushed in through the API (see §11).
- Runtime-loaded plugins. v1 plugins are compiled in only; §6 describes how the design keeps `.so` plugins possible.
- A drag-and-drop visual job editor. v1 has a JSON editor with a live graph preview.
- Git sync of job definitions. The architecture prepares for it (see §13).
- Parallel branches inside a job, other than node fan-out.

---

## 2. Tech stack

| Concern | Choice | Notes |
|---|---|---|
| Language (backend + agent) | **Go** (latest stable) | Single static binary, `damson controller` / `damson agent` / `damson cli` |
| HTTP API | stdlib `net/http` + **Huma** | OpenAPI 3.1 generated from Go types |
| Agent transport | **WebSocket** (`github.com/coder/websocket`) over TLS | Binary frames |
| RPC framing | **JSON-RPC 2.0 envelope encoded with MessagePack** (`github.com/vmihailenco/msgpack/v5`) | Thin in-house layer (~300 LOC), bidirectional |
| Database | **PostgreSQL** (prod, HA) / **SQLite** (dev, single controller) | `pgx` / `modernc.org/sqlite` (pure Go, no cgo) |
| SQL access | **sqlc** | Shared queries where portable, plus per-dialect overrides |
| Migrations | **goose** | Separate migration dirs per dialect |
| Background work | **River** (Postgres driver, SQLite driver) | Run orchestration, maintenance jobs |
| Expressions | **CEL** (`github.com/google/cel-go`) | Conditions, selectors, interpolation, authz, webhook mapping |
| JSON Schema | `github.com/invopop/jsonschema` (generate) + `github.com/santhosh-tekuri/jsonschema/v6` (validate) | Plugin inputs and outputs, job options |
| Secrets | DB backend (AES-256-GCM envelope) / **OpenBao** (`github.com/openbao/openbao/api`) | |
| Log storage | `fs` / `db` / `s3` backends (`minio-go` S3 client, shared with the `transfer` plugin; S3-compatible endpoints) | |
| SSH / SFTP | `golang.org/x/crypto/ssh`, `github.com/pkg/sftp` | SCP implemented over an SSH session |
| WinRM | `github.com/masterzen/winrm` | HTTPS, Basic/NTLM in v1 |
| FTP / FTPS | `github.com/jlaffaye/ftp` | |
| Ansible | `ansible-playbook` binary in the agent image, plus an embedded Python callback plugin | |
| Git (Ansible, Buildah, Helm) | `git` binary in the agent images | Shared checkout helper in `plugins/internal/gitcheckout` |
| Image builds | `buildah` binary (build-agent image) | Rootless where possible |
| Kubernetes deploys | `helm` binary (build-agent image) | |
| Plugins | Public Go SDK (`pkg/pluginsdk`), compiled in. Custom binaries via `damson build --with`. | Runtime `.so` loading deferred (§6); no gRPC |
| Cron | `github.com/robfig/cron/v3` parser | Scheduling loop is ours |
| Config | `github.com/knadh/koanf` | File + env + flags |
| Auth | `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2` | Server-side sessions |
| Observability | OpenTelemetry (traces + metrics), `log/slog` | Prometheus endpoint via the OTel exporter |
| Frontend | **SvelteKit** (SPA, `adapter-static`), Svelte 5, TypeScript, Tailwind v4 | Embedded in the binary via `embed` |
| UI kit | **shadcn-svelte** (MIT) | Components vendored into the repo |
| Schema forms | **`@sjsf/form`** + shadcn theme + ajv8 validator (MIT) | Plugin step forms and run-option dialogs |
| Code editor | CodeMirror 6 (MIT) | JSON job editor with schema validation |
| Job and run graph | `@xyflow/svelte` (Svelte Flow, MIT) plus an in-house layout function | No graph layout library needed (§15.1) |
| API client | `openapi-typescript` + `openapi-fetch` | Generated from the controller's OpenAPI |
| Tests | Go `testing`, `testcontainers-go` (Postgres, sshd, FTP, S3-compatible), Playwright (UI smoke) | |

Health and readiness endpoints (`/healthz`, `/readyz`) use plain stdlib `net/http`. The typed `/api/v1` endpoints use Huma; its errors are built from `internal/api`'s `Problem` type (`problemFor`), so there is one error-to-problem+json mapping. The OpenAPI document is committed as `web/openapi.json` and the TS types are generated by `make gen`.

**License compatibility:** all dependencies are MIT, BSD, Apache-2.0, or MPL-2.0. These are compatible with GPL-3.0; note that Apache-2.0 is compatible with GPLv3 but not GPLv2. Any new dependency must be checked against this list.

---

## 3. System overview

```
              Users / CI / ServiceNow sync / webhooks
                              │ HTTPS
                     ┌────────▼────────┐
                     │  Load balancer  │  (no stickiness required)
                     └───┬────────┬────┘
            ┌────────────▼┐      ┌▼────────────┐
            │ controller A │ ... │ controller N │   stateless, N ≥ 2 in prod
            │ API+UI+WS    │◄───►│ API+UI+WS    │   internal HTTP: log tail proxy, cancel hints
            └──┬───────┬───┘     └───┬───────┬──┘
               │       │             │       │
     ┌─────────▼──┐  ┌─▼─────────────▼─┐  ┌──▼──────────┐
     │ PostgreSQL │  │ S3 (optional)   │  │ OpenBao     │ (optional)
     │ (source of │  └─────────────────┘  └─────────────┘
     │  truth)    │
     └────────────┘
               ▲ wss:// (agent-initiated, JSON-RPC over MessagePack)
   ┌───────────┴───────────┬────────────────────────┐
┌──┴──────┐          ┌─────┴───┐              ┌─────┴───┐
│ agent   │          │ agent   │   ...        │ agent   │  stateless workers; VMs or k8s pods
│ pool=dc1│          │ pool=dc2│              │ pool=k8s│  all plugins built in
└──┬──────┘          └────┬────┘              └────┬────┘
   │ SSH/WinRM/SFTP/FTP/S3│                        │
   ▼                      ▼                        ▼
 target nodes (no Damson software installed)
```

**Required infrastructure: PostgreSQL only.** Damson itself is two roles of one Go binary (controller and agent). Object storage for logs and OpenBao for secrets are **optional**. Logs default to the `db` log store, and secrets to the `db` backend. There is no message broker: Postgres plus the agent WebSockets cover queueing, coordination, and log delivery (see §23 for why NATS was rejected).

**Key properties**

- **The controllers hold no durable state.** All state is in the database and the log store. Controllers can be killed at any time.
- **Agents connect out to the controllers.** Nothing connects inbound to agents, which works through NAT, firewalls, and k8s.
- **Agents are workers, not node-resident.** They reach target nodes over SSH, WinRM, SFTP, FTP, or S3. Agent pools are placed in each network zone. This is how air-gapped and segmented networks are handled.
- **File data streams directly between endpoints through the agent's memory.** It never passes through the controller.
- **Runs survive a controller bounce.** Agents keep executing and reconnect to another controller. Logs and completions are buffered and re-delivered.

---

## 4. Domain model

```
Project (tree) ──< Job ──< JobVersion (immutable spec)
                    │
                    ├──< Schedule
                    ├──< Webhook
                    └──< Run ──< StepInstance ──< Execution (one attempt) ──< LogChunk
Node (global inventory, filtered per project)
Agent (live connection, pool, capabilities)
Secret (scoped to a project path)
AccessRule (project or job, role, CEL over claims)
ServiceAccount / ApiToken
AuditEvent
```

| Entity | Purpose |
|---|---|
| **Project** | A tree (`infra`, `infra/billing`, `infra/billing/reports`). It carries access rules, a node scope, and secrets. Children inherit from their ancestors. |
| **Job** | A named job in a project, pointing to its current `JobVersion`. `managed_by` is one of `ui`, `files:<source>`, or `git:<source>` (future). |
| **JobVersion** | An immutable JSON spec with a checksum. Every run pins exactly one version. |
| **Run** | One invocation of a job: trigger, resolved options, status, and a `concurrency_key`. |
| **StepInstance** | One logical step in a run. Fan-out and `for_each` create one instance per node. It has a stable `path` (ADR-0037): the ids from the top of the job down joined by `/`, such as `deploy/inner/build`, `deploy/catch/rollback` or `per-node/for_each[web-01]/install/catch/rollback`; a top-level step's path is its id. |
| **Execution** | One attempt of a step instance on one agent. It carries the lease, the fencing `attempt_id`, and the agent and controller ownership. |
| **LogChunk** | A manifest row pointing to a stored log chunk object (sequence range, byte count, timestamps). |
| **Node** | An inventory record: name, address, transport, tags, attributes, pool, credential ref, host key, source, external_id. |
| **Agent** | A connected worker: pool, labels, capabilities (plugins, versions, ansible), capacity, telemetry snapshot, protocol version, owning controller and session. |

---

## 5. Job spec (JSON)

The spec is JSON; it is the canonical format stored in the database. YAML may be accepted as input by the CLI and converted to JSON.

### 5.1 Example: S3 → VM → SSH, with try/catch/finally

```json
{
  "apiVersion": "damson.dev/v1",
  "kind": "Job",
  "metadata": {
    "project": "infra/billing",
    "name": "deploy-report-tool",
    "description": "Fetch a release from S3 and install it on report servers"
  },
  "spec": {
    "options": {
      "type": "object",
      "required": ["version", "env"],
      "properties": {
        "version": { "type": "string", "pattern": "^\\d+\\.\\d+\\.\\d+$" },
        "env": { "type": "string", "enum": ["staging", "prod"] }
      }
    },
    "targets": {
      "selector": "'report' in node.tags && node.attributes.env == inputs.env"
    },
    "concurrency": { "key": "inputs.env", "max": 1, "policy": "queue" },
    "timeout": "30m",
    "steps": [
      {
        "id": "per-node",
        "type": "group",
        "for_each": "targets",
        "max_parallel": 5,
        "fail_threshold": 1,
        "steps": [
          {
            "id": "fetch",
            "plugin": "transfer",
            "timeout": "10m",
            "retry": {
              "max_attempts": 4,
              "backoff": { "initial": "5s", "multiplier": 2, "max": "2m", "jitter": 0.2 },
              "retry_on": "error.kind in ['connection', 'timeout']"
            },
            "with": {
              "mode": "copy",
              "source": {
                "s3": {
                  "connection": "secret://infra/s3-releases",
                  "bucket": "releases",
                  "key": "report-tool/${{ inputs.version }}/report-tool.tar.gz"
                }
              },
              "destination": {
                "node": { "name": "${{ node.name }}", "protocol": "sftp", "path": "/opt/staging/" }
              },
              "verify": "sha256"
            }
          },
          {
            "id": "install",
            "type": "group",
            "steps": [
              {
                "id": "unpack",
                "plugin": "ssh",
                "with": {
                  "node": "${{ node.name }}",
                  "sudo": true,
                  "command": "tar -xzf ${{ steps.fetch.outputs.destination_path }} -C /opt/report-tool && /opt/report-tool/install.sh"
                }
              }
            ],
            "catch": [
              {
                "id": "rollback",
                "plugin": "ssh",
                "with": { "node": "${{ node.name }}", "sudo": true, "command": "/opt/report-tool/rollback.sh" }
              }
            ],
            "finally": [
              {
                "id": "cleanup",
                "plugin": "ssh",
                "with": { "node": "${{ node.name }}", "command": "rm -f ${{ steps.fetch.outputs.destination_path }}" }
              }
            ],
            "rethrow": true
          }
        ]
      },
      {
        "id": "report",
        "plugin": "ssh",
        "when": "always()",
        "with": {
          "node": "ops-bastion",
          "command": "echo 'run ${{ run.id }} finished: ${{ run.status }}'"
        }
      }
    ]
  }
}
```

### 5.2 Step types

| Field | Meaning |
|---|---|
| `id` | Unique within its enclosing scope (`[a-z0-9-]+`) |
| `plugin` + `with` | A plugin step. `plugin` is `name` or `name@<major>`. `with` is validated against the plugin's input schema *after* interpolation. |
| `type: "group"` | A container with `steps` (at least one), optional `catch`, `finally`, `rethrow`, `for_each`, `max_parallel`, `fail_threshold`. It takes `when` and `continue_on_error`, not `plugin`, `with`, `retry`, `timeout` or `runs_on`. Inside a group the ids `catch` and `finally` are reserved (a top-level step may still use them); a group's three lists share one id namespace; groups nest at most 8 deep (ADR-0037). |
| `when` | A CEL condition. The default is `success()` (all previous steps in scope succeeded or were handled). If the expression calls any status function (`success()`, `failure()`, `always()`, `cancelled()`), the implicit `success()` is off; otherwise it is evaluated as `success() && (when)`. |
| `continue_on_error` | A failure is recorded but does not fail the enclosing scope. |
| `timeout` | Per attempt, a Go duration (ADR-0035). The engine stores it on the execution; the **agent enforces** it (a context deadline from `timeout_ms` in `step.execute`, started when the agent accepts the execute and kept across reconnects), so it works while the agent is cut off from its controller. A timed-out attempt reports `timed_out` with error kind `timeout`; the engine sees a failed step whose `error.kind` is `timeout` (retry, catch and `continue_on_error` react to that). Whichever of the deadline and a `step.cancel` cancels the context first decides `timed_out` or `canceled`. The job-level `timeout` covers the whole run from its start (§5.3 rule 9). |
| `retry` | Plugin steps only (ADR-0036). `max_attempts` (required, 1 to 100, counts the first attempt), `backoff{initial, multiplier, max, jitter}` (defaults 1s, 2, 5m, 0.2; `max` at most 24h), `retry_on` (CEL over `error`; default `connection`, `timeout`, `lost`), `on_lost` (optional bool: unset follows the plugin's `idempotent`, `false` never retries a lost attempt). |
| `runs_on` | Optional agent selection: `{ "pool": "dc1", "labels": {...} }`. The default is the pool of the target node, or any pool. |

### 5.3 Control-flow semantics

The engine evaluates these rules deterministically:

1. Steps in a list run **sequentially**.
2. Before a step runs, its `when` is evaluated. If false, the step is `skipped`.
3. If a step fails and has no `continue_on_error`, the rest of that list is skipped, except for steps whose `when` explicitly allows it by calling a status function (`failure()`, `always()` or `cancelled()`). Any status function in `when` turns off the implicit `success() &&` that is otherwise prepended.
4. **Group with `catch`** (ADR-0037). If a step in `steps` fails (and does not `continue_on_error`), the rest of `steps` is skipped and `catch` runs with `error` bound to that failure: `step` (the failing step's id), `path` (its instance path, the leaf for a failure in a nested group), `kind`, `message` and, when known, `exit_code`; steps nested in the catch list inherit `error`. A cancelled body does not enter `catch` (cancelled is not failed).
   - If `catch` does not fail, the group is `succeeded (handled)`: it keeps the caught error (`steps.<group>.handled` is true, `steps.<group>.error` is the caught error). A catch whose steps were all skipped counts as handled.
   - If `rethrow` is true, the group fails with the original body error after `catch` and `finally`.
   - If `catch` fails, the group fails with the catch error, which replaces the body error; if `catch` was cancelled the body error stands.
5. **`finally`** runs after the body and the catch list, whether the group succeeded, failed, or was cancelled. A failure in `finally` fails the group and replaces any other error. It does not run for a group that never started (its `when` was false). After a cancel or the job timeout the finally list is protected for a grace period (`engine.finally_grace`, default 2m): its steps are not cancelled until the cancel instant plus the grace, a finally step that starts during the grace gets at most the time left as its `timeout`, and a wake at the end of the grace cancels what is still running; after it, unstarted finally steps are cancelled ("finally grace expired"). The grace applies to `finally` only, not to top-level `always()` steps, and not to the finally list of a group created at or after the cancel (it is itself left to run to completion). A finally step cut by its capped timeout fails with kind `timeout` and can make the run `failed` rather than `cancelled`; the grace is measured against `cancel_requested_at`, so controller clocks must be kept in sync. The status functions in a finally list are local to it, and a finally list is run as if created after the cancel, so the default `when` runs.
   - A group is finished as: finally failed, then catch failed, then rethrown, then handled, then uncaught body failure (all `failed`), else `cancelled` if any step was cancelled, else `succeeded`. At group level a failure beats a cancel.
6. **`for_each: "targets"`** (or any CEL list expression) runs the group once per item, in independent *lanes*, with `item` bound (and `node`, when the item is an object; ADR-0038).
   - The list is evaluated **once**, when the group starts, and frozen on the group's row. The lane key is the item's `name` (an object with a string `name`), the item (a string), else its index; keys must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$` and be unique. A failing expression, a non-list, more than 1000 items or a bad or duplicate key fails the group before any lane starts. An empty list succeeds with no lanes.
   - Each lane is a full instance of the group at `<group>/for_each[<key>]`: its `steps`, `catch`, `finally` and `rethrow` run per lane. Inside a lane, `steps.<id>` refers to that lane's steps; lanes do not see each other.
   - `max_parallel` (1 to 1000, default all) limits concurrent lanes; lanes open in list order and every open lane progresses in the same `Decide`.
   - `fail_threshold` (1 to 1000, default never) is the number of failed lanes after which lanes that have not started are cancelled ("fail_threshold reached"); running lanes finish. A lane handled by its catch does not count; a rethrown one does.
   - The group fails with the first failed lane's error (in list order, its `path` the leaf step), else is cancelled if a lane was, else succeeds. A cancel or the job timeout cancels the lanes that have not started, unless the group was admitted after the cancel.
   - `for_each` groups do not nest in v1.
7. **Retries** (ADR-0036). A failed, timed-out or lost attempt does not finish its step: the engine either schedules attempt n+1 or emits `SettleStep`. A step without `retry` has one attempt. The retry is scheduled when the attempt's `retry_on` holds (default `connection`, `timeout`, `lost`), attempts are left, a lost attempt is allowed (`on_lost`, else the plugin's `idempotent`), the run is not being cancelled (a user cancel or the job timeout) and the retry would not be due after the job deadline. It is due at the failed attempt's `finished_at` plus `min(max, initial * multiplier^(n-1)) * (1 - jitter * u)`, with `u` a deterministic hash of run, step and attempt (the engine has no randomness). An exhausted or refused retry settles the step with the last attempt's error; a `retry_on` that fails to evaluate counts as false. `continue_on_error` and `catch` see the final error only.
8. **Status functions** available in `when`:
   - `success()`: everything previous in scope succeeded or was handled.
   - `failure()`: something previous in scope failed.
   - `always()`: always true.
   - `cancelled()`: the run has been cancelled.
9. **Job timeout** (`spec.timeout`, ADR-0035). The deadline is `runs.started_at + timeout`. When it has passed and no cancel was requested, `Decide` emits `TimeoutRun` and treats the run as cancelling from that instant; the store records `cancel_requested_at` with `cancel_reason = 'timeout'` inside the advance's version CAS (a user cancel that commits first wins). The ordinary cancel rules then apply: running attempts are cancelled, pending steps are cancelled as `job timed out`, and only `always()` and `cancelled()` steps still run (the timeout itself does not trigger `failure()` handlers; a step that really failed still does). The run is `failed` if any step failed or was cancelled, `succeeded` if every step still succeeded. When the run starts the engine also emits `ScheduleWake{deadline}`.

### 5.4 Expressions and data passing

- Interpolation uses `${{ <CEL> }}` inside any string in `with`.
  - If a string is *exactly* one expression, the result keeps its CEL type (number, bool, list, map).
  - Otherwise the results are stringified and concatenated.
- **Resolution happens on the controller** when a step instance becomes runnable. The resolved input, without secret values, is persisted on the StepInstance for audit and replay.
- CEL context variables:

  | Variable | Contents |
  |---|---|
  | `inputs` | Run options, validated against `spec.options` |
  | `run` | `id`, `status`, `trigger`, `triggered_by`, `started_at` |
  | `job` | `name`, `project`, `version` |
  | `node` | Inside a `for_each` lane whose item is an object (a node of `targets`): `name`, `address`, `tags`, `attributes`; empty otherwise |
  | `targets` | The resolved node list |
  | `steps.<id>` | `status`, `outputs`, `error`, `started_at`, `finished_at`. Lexical: a step sees the earlier siblings of its own list and of every enclosing list, an inner id shadowing an outer one; lane-local inside `for_each`. A group also has `handled` and `steps` (the values of its body steps) |
  | `steps.<id>.lanes[<key>]` | Access to a specific lane's results from outside the group: `key`, `item`, `status`, `error`, `handled`, `started_at`, `finished_at` and `steps` (the lane's step values) |
| `item` | Inside a `for_each` lane: the lane's item (any JSON value) |
  | `error` | Inside `catch`: `step`, `path`, `kind`, `message`, `exit_code` (steps nested in the catch list inherit it; `retry_on` is evaluated over the attempt's own error) |

- **Secrets are never values in the CEL context.** They are referenced only as `secret://...` strings in plugin fields whose schema marks them `x-damson-secret: true`. They are resolved at dispatch time (§9).
- **Run options** marked `x-damson-secret: true` in `spec.options` accept only `secret://` references (anything else is rejected at trigger time, and such an option may not default to a plaintext value). Options that are not marked are stored and returned verbatim, so authors must mark credentials.
- CEL programs run with a cost limit and a size limit on results.
- **Outputs** are JSON objects, validated against the plugin's output schema, with a maximum of 256 KiB per execution. Larger data belongs in files or object storage.

### 5.5 Targets and node selection

- `spec.targets.selector` is a CEL expression over `node` (name, tags, attributes, pool, os, transport).
  - It is evaluated against the inventory intersected with the project's node scope (§12).
  - It is evaluated once, at trigger, and the matching nodes are frozen into the run's `run_targets` rows (ADR-0045). More than 1000 matches rejects the trigger with 422 at `spec.targets` and creates no run. Each `for_each` group also freezes the list it iterates when it starts.
- `targets` is the enabled inventory nodes (ADR-0043) that the project's node scope allows (`inventory.Scope`; `AllNodes` until M8), sorted by name, filtered by the selector (the whole inventory is scanned; more than 1000 matches is a 422 at `spec.targets`). Each is a plain map of `id`, `name`, `address`, `tags`, `attributes`, `pool`, `os` and `transport`, and never a credential ref, host key, host key policy or port. The list is read at trigger only when the job can use it: `engine.UsesTargets` is true if `spec.targets` is set or the word `targets` appears anywhere in the spec (conservative). A failed read fails the trigger with a 5xx and creates no run. The advancer does not read the inventory: when `engine.UsesTargets(job)` it decodes the run's `run_targets` rows (`store.ListRunTargets`) as `targets`, and a run without rows has empty targets. `--dev-nodes` seeds its file into the inventory at startup (ADR-0044), so seeded nodes are served like any other. With no nodes, `targets` is empty. A node for which the selector does not evaluate to true, or fails to, does not match. A `for_each` group freezes the list it iterates when it starts (ADR-0038), so the lanes of a run do not change.
- Frozen run targets (ADR-0045): each `run_targets` row holds `name`, `node_id` and the node as JSON (the map above, with `id`; never credential or host key), so nodes added, disabled or renamed after the trigger never change a running run's lanes. Dispatch resolves a step's node name through the snapshot: a name in the snapshot follows its frozen `node_id` (a rename still reaches the node, `NodeInfo.Name` stays the requested name, a deleted node is `unknown node`); a name outside the snapshot is looked up by name. The scope check, the enabled check and the host key policy apply to the node's current row, so address, credential and host key are those at dispatch time. A literal `node: <name>` in step input also reaches inventory nodes (ADR-0043). `node.pool` is stored but does not route yet: `runs_on` still defaults to the `default` pool.
- Plugins that accept a whole inventory (Ansible) receive `targets` directly. Plugins that work on one node (SSH, WinRM) take a `node` field, usually `${{ node.name }}` inside `for_each`.

---

## 6. Plugin model and SDK

The plugin SDK (`pkg/pluginsdk`) is public and versioned from day one. **Built-in plugins use exactly the same SDK** as third-party plugins. The SDK is therefore exercised constantly, and the built-ins serve as reference implementations for humans and Claude.

### Delivery: compiled in (v1)

In v1 every plugin is **compiled into the binary**.

- A plugin is a Go package (or an out-of-tree Go module) that implements `pluginsdk.Plugin` and exports one constructor:

  ```go
  // Every plugin package exports exactly this. It is also the symbol a future
  // runtime-loaded .so would export, so plugin code won't change if that is added.
  func Plugins() []pluginsdk.Plugin
  ```

- `plugins/all` lists the built-in plugins explicitly. There is no `init()` registration.
- Out-of-tree plugins are added with `damson build --with <module>[@<version>][=<dir>]` (ADR-0050). It generates a small `main` package in a temporary module that calls `damsoncli.Main` (`pkg/damsoncli`, the second public Go API, released with Damson's version) with the extra modules, then builds a custom binary with the Go toolchain only (no shell, no `go mod tidy`, checksum database untouched), the way `xcaddy` works for Caddy. The CLI itself lives in `internal/cli`; `cmd/damson` is a thin `damsoncli.Main` call.
- Extra modules are linked into the process and fully trusted (their initialisers run at start; on an agent they can read every dispatched secret, on a controller the DSN, KEK and secrets). Link them into agent binaries only. They must export `Plugins()`, may not reuse a built-in name at any version, a name belongs to one module, `name@version` is unique and manifests must validate; a violation stops the binary at start. Extras are agent plugins: the controller registry links only `plugins/all`, and a non-dev controller in a binary with extras logs a warning.

**Plugin registry**

- Agents send the full manifests of all plugins compiled into their binary in `agent.hello`.
- Manifests are persisted and each controller keeps an in-memory registry (`internal/pluginreg`). `plugin_manifests(name, version, checksum, manifest)` holds one row per distinct manifest, and `agent_plugins(agent_id, name, version, checksum)` links an agent to the plugins it reported. Both are written in the same transaction as the agent row on `agent.hello`, so they follow the `session_seq` order and a refused hello writes nothing. Nothing is deleted on disconnect: a provider is live while its agent row has a `controller_id`, no `disconnected_at`, and `last_seen` within twice the heartbeat plus flush intervals (40s at defaults), so the agents of a crashed controller age out. Local agents enter the registry directly (added on hello, dropped on disconnect). Every 7s, and once at start, each controller reads the live agents of the *other* controllers and replaces its remote view, so any controller validates, resolves (cluster-wide) and lists, and a controller that starts while agents are live on other controllers knows their plugins at once (agents that were connected to the restarted controller itself reconnect first). The dispatch path never reads the database. The view of other controllers lags by at most one sync interval. The stored checksum is used as is, never recomputed, so the checksum algorithm is part of the storage contract. Manifests no agent references are swept hourly when older than an hour. The controller also links the built-in plugins itself (not stored), so their schemas are known even when no agent is connected. During a mixed-version rolling upgrade two controllers can accept different checksums for a built-in; that is harmless because each dispatches only to its own agents.
- **Version pinning:** steps may pin a major version (`"plugin": "helm@1"`). Unpinned means the highest version available on eligible agents.
- **Dispatch** only goes to agents that report the plugin at a compatible version and satisfy its `Requires` capabilities.
- **Schema conflicts:** if agents report different schemas for the same name and version (checksum mismatch), the controller's own manifest wins, otherwise the most reported one; the controller flags the minority in the API and refuses dispatch to it until fixed.
- **Built-ins cannot be shadowed by agents.** An unpinned reference to a name the controller links itself resolves only to a built-in version; an agent-only version of a built-in name (for example a higher `ssh` version) is reachable only with an explicit pin (`ssh@2`). An agent reporting a manifest for a built-in name that differs from the built-in, or a higher version, is logged as a warning (names, versions, checksums only).
- **Validation.** `Lookup` is for authoring time and may name a version no agent can run; the dispatch path validates the resolved input against the manifest returned by `Resolve`. Manifest schemas (size and nesting) and step inputs (size) are bounded, and validation errors never contain input values.

**Developer tooling**

- `damson plugin new <name>` (implemented) scaffolds a Go module with the plugin package, a test (contract suite, source check, `Execute`), and a README.
- `pkg/pluginsdk/plugintest` runs a plugin's `Execute` in a unit test with a fake emitter, fake secrets, and a temp WorkDir, then asserts outputs and masked logs. It also provides a **contract suite** every plugin must pass: schema generation, cancellation (including an optional blocking case), secret masking (plaintext check), and output-schema conformance (validated with `santhosh-tekuri/jsonschema/v6`, a dependency of `plugintest` only).
- `damson plugin run <ref> --input input.json --secrets secrets.json [--nodes nodes.json]` (implemented) runs one linked plugin locally without a controller, with the agent's masker and output sanitizer.
- `damson plugin validate [ref]` (implemented) checks the manifest, the schemas, and semver of linked plugins.
- `examples/plugins/hello` is a minimal out-of-tree plugin. It is a nested Go module (the root `./...` does not cover it); `make plugin-contract` vets and tests it and builds it into a custom binary with `damson build --with` offline (`GOPROXY=off` after `go mod download`), then runs `plugin validate`, `plugin run` and `version` on it.

### Future: runtime-loaded plugins (`.so`)

This is not in v1, but the design keeps it possible: a plugin `.so` dropped into the worker's plugin directory and loaded when the agent starts.

**What already makes it possible**

- The engine and dispatcher reach plugins only through the registry interface. They never know how a plugin was delivered.
- The controller learns plugin schemas from agent manifests, so it never needs the plugin's code.
- Plugins export `Plugins() []pluginsdk.Plugin`, which is the symbol the agent would look up with `plugin.Lookup`.
- Plugins have no `init()` registration and no package-level mutable state.
- `pkg/pluginsdk` keeps a minimal dependency surface (the standard library plus the JSON Schema package). A `.so` must be built against exactly the same version of every package it shares with the host, so every extra SDK dependency makes that harder.

**Constraints to resolve before adopting it** (recorded in ADR-0032)

- **Build lock-step.** A `.so` loads only if it was built with the same Go toolchain, the same versions of all shared packages, and compatible build flags as the agent binary. In practice, plugins must be rebuilt for every Damson release. A `damson plugin build` command that pins all of these would be needed.
- **cgo and dynamic linking.** These are required, and Go supports `.so` plugins on Linux and macOS only (no Windows agents). It would need a separate agent build flavor; the default static binary stays as it is.
- **No isolation or unloading.** Panics can be recovered per execution, but a crash in cgo code or corrupted memory takes the agent down, and a loaded plugin can't be unloaded without restarting the agent.

### SDK interface

The SDK is semver `0.x` (`pluginsdk.Version`): treated as stable, but breaking changes are still allowed before 1.0.

```go
package pluginsdk

const Version = "0.3.0"

type TargetMode string // wire values: "none" | "single_node" | "inventory" | "nodes"; Valid() bool

type Manifest struct {
    Name         string             // "ssh": [a-z][a-z0-9_-]*
    Version      string             // semver
    Title        string
    Description  string
    InputSchema  *jsonschema.Schema // generated from the Input struct (SchemaFor / MustSchemaFor)
    OutputSchema *jsonschema.Schema // generated from the Output struct
    Idempotent   bool               // safe to retry after a "lost" execution
    Requires     []string           // agent capabilities, e.g. "bin:ansible-playbook", "bin:git"
    TargetMode   TargetMode
    NodePaths    []string           // mode "nodes" only: 1..8 unique RFC 6901 pointers into the input, each naming a node (ADR-0048)
}

// Validate checks the manifest; Checksum is sha256 over canonical JSON (sorted keys),
// independent of schema marshaling order.
func (Manifest) Validate() error
func (Manifest) Checksum() (string, error)

type Plugin interface {
    Manifest() Manifest
    // Validate runs on the agent before Execute (cross-field rules). A failure is a
    // non-retryable validation error. The controller validates only against InputSchema,
    // because it may not have the plugin's code (plugins in custom agent binaries).
    // InputSchema validates the RESOLVED input (after ${{ }} evaluation), not the saved spec.
    Validate(ctx context.Context, input json.RawMessage) error
    // Execute runs on the agent. A step failure goes in Result.Error; a non-nil error
    // return means an unexpected plugin fault.
    Execute(ctx context.Context, req ExecRequest, emit Emitter) (Result, error)
}

type ExecRequest struct {
    Input     json.RawMessage        // fully resolved input (secret refs still as refs)
    Secrets   SecretResolver         // resolves secret:// refs from the bundle sent with the request
    Nodes     map[string]NodeInfo    // nodes referenced by this step, keyed by node name
    WorkDir   string                 // per-execution temp dir, removed after
    Logger    *slog.Logger           // diagnostics only; never receives secret values
    HostKeys  HostKeyReporter        // observed host keys (tofu); may be nil
    Config    map[string]string      // operator settings for this plugin, e.g. "allow_insecure_host_keys"; may be nil
}

// ReportHostKey records a host key (authorized_keys format) observed on a node. The agent
// forwards it to the controller, which stores it as the node's observed host key.
type HostKeyReporter interface {
    ReportHostKey(node, key string)
}

// No ctx: the bundle (§9.3) is already in agent memory.
type SecretResolver interface {
    Resolve(ref string) (string, error)                  // one value
    ResolveFields(ref string) (map[string]string, error) // whole credential (ssh_key, s3, ...)
}

type NodeInfo struct { // names and wire tags follow the node fields of §11
    Name          string         `json:"name"`
    Address       string         `json:"address"`
    Port          int            `json:"port,omitempty"`          // the node's own port; 0 = plugin default (winrm: 5986 https, 5985 http)
    Transport     string         `json:"transport,omitempty"`       // ssh | winrm
    OS            string         `json:"os,omitempty"`
    Attributes    map[string]any `json:"attributes,omitempty"`      // string | number | bool
    Credential    string         `json:"credential,omitempty"`      // secret:// ref, never a value
    HostKey       string         `json:"host_key,omitempty"`
    HostKeyPolicy string         `json:"host_key_policy,omitempty"` // strict | tofu | insecure
}

type Emitter interface {
    Log(stream Stream, line string)  // Stdout, Stderr, System
    Progress(pct float64, msg string)
}

type Result struct {
    Outputs json.RawMessage
    Error   *StepError               // see below
}

type ErrorKind string // connection|auth|timeout|exit_code|validation|cancelled|internal; Valid() bool

type StepError struct {
    Kind     ErrorKind `json:"kind"`
    Message  string    `json:"message"`
    ExitCode *int      `json:"exit_code,omitempty"` // for exit_code
    // unexported cause, never serialized
}

// Helpers: NewError, ValidationError, ExitCodeError, and AsStepError(err), which maps
// any error to a *StepError (context.Canceled -> cancelled, DeadlineExceeded -> timeout,
// anything else -> internal).
```

**Error reporting**

- A step failure is returned as `Result.Error`. A non-nil `error` return means the plugin itself failed unexpectedly; callers normalize it with `AsStepError`.
- If `Execute` returns a non-nil error while its context is cancelled, the kind is `cancelled`.
- The agent recovers a panic in a plugin and reports it as an `internal` step error.

**Schemas**

- `SchemaFor[T]` / `MustSchemaFor[T]` generate JSON Schema (`github.com/invopop/jsonschema`, version pinned in `go.mod`) from the struct's `json` and `jsonschema` tags. A field is required unless its `json` tag has `omitempty`; objects get `additionalProperties: false`. A golden test pins the output and the manifest checksum.

**Rules**

- Inputs and outputs are Go structs with JSON and jsonschema tags. Schemas are generated from them, never hand-written.
- Plugins must honor `ctx` cancellation and kill remote processes where possible.
- Plugins never log secret values. The emitter also masks them (§9.4).
- **Script output protocol (SSH, WinRM).** Lines matching `##damson-output <key>=<value>` or `##damson-output-json <json-object>` are parsed into `outputs.values` and stripped from the log stream.
- **Through the registry only.** The core never assumes a plugin is in-process; it always goes through the plugin registry interface. How a plugin was delivered is invisible to the engine and the dispatcher.
- **SDK compatibility.** `pkg/pluginsdk` follows semver. Breaking changes require a new major version.

### 6.1 `ssh`

- **Input:** `node`, `command` *or* `script` (uploaded to `WorkDir` on the target and executed), `interpreter` (default `/bin/sh`), `env`, `workdir`, `sudo`, `sudo_user`, `pty`, `credential` (overrides the node default).
- **Output:** `exit_code`, `stdout_tail` (last 64 KiB), `stderr_tail`, `values` (from the output protocol).
- **Host keys:** the node's `host_key_policy` is one of `strict` (default; requires `host_key` in the inventory), `tofu` (dispatch sends the pinned `host_key`, else the observed key, else none; with no key the agent accepts the first key it sees and reports it, and the controller stores it as the node's observed key, first key wins, ADR-0046), or `insecure` (refused by the controller at dispatch as `host key policy not available`; the agent's `allow_insecure_host_keys` stays a second gate).
- **Cancel:** send a signal via the SSH session, then close the session.
- **Success:** a non-zero exit code is an `exit_code` error. `success_exit_codes` can override that.

### 6.2 `winrm`

Details: `docs/reference/plugin-winrm.rst`, ADR-0049.

- **Input:** `node`, `command` (cmd.exe) *or* `powershell` (script block), `env`, `credential` (fields `username`, `password`; `DOMAIN\user` or `user@domain` for NTLM; `username` may come from the node attribute), `transport` (`https` default | `http`), `auth` (`ntlm` default | `basic`), `insecure_skip_verify`, `success_exit_codes`.
- **Output:** the same as `ssh`.
- **Connection:** the port is the node's own (inventory default 5986; an `http` node stores 5985). `basic` over `http` is always refused. `http` needs `ntlm` (message encryption) and the agent setting `agent.allow_insecure_winrm`; `insecure_skip_verify` needs the same setting and is invalid over `http`. https verifies against the system roots plus `agent.tls_ca_file`, TLS 1.2 minimum, no environment proxy. `host_key_policy` and `host_key` are ignored (no certificate pinning in v1).
- **Execution:** the command line is a fixed `powershell.exe ... -EncodedCommand` bootstrap. The script, `env` values and resolved secrets go to its stdin as base64 JSON, so they never appear in the process command line, event 4688 or the 8191-character limit. Exit code: explicit `exit N`; a terminating error gives 1 with text on stderr; otherwise the last native `$LASTEXITCODE`; for `command`, the cmd.exe exit code. Payload limit about 2 MB (`ConvertFrom-Json` in Windows PowerShell 5.1).
- **Cancel:** terminate signal, then delete the shell (ends the process tree); returns within about 2 s.
- **Status:** tested only against a fake WinRM server (`plugins/internal/winrmtest`); a run on a Windows host (§19) is still required.
- Kerberos and CredSSP are deferred to after v1.

### 6.3 `transfer`

One plugin covers server-to-server copies *and* S3 in both directions. It uses `TargetMode = nodes` with `NodePaths` `/source/node/name` and `/destination/node/name`; an S3 endpoint has no node and is skipped (ADR-0048).

**Status:** all protocols are implemented: `sftp` (#144), `scp` (#145), `ftp` or `ftps` (#146), and `s3` (#147). Details of what runs today: `docs/reference/plugin-transfer.rst`. Plain `ftp` is refused unless the agent sets `agent.allow_plain_ftp` (#161).

- **Input:**
  - `mode` is `copy` or `move`.
  - `source` and `destination` each take **one** of:
    - `node: {name, protocol: sftp|scp|ftp|ftps, path}`
    - `s3: {connection, bucket, key}`, where `connection` is a `secret://` ref (a literal is rejected) with fields `endpoint`, `region`, `access_key`, `secret_key` (required), and optional `session_token` and `path_style`. `endpoint` must not carry userinfo, a query or a fragment (validation error). `endpoint`, `region` and `path_style` are not masked by the agent when strictly shaped (ADR-0051). S3 and node endpoints can be mixed; `s3` to `s3` needs no node
  - `verify` is `none`, `size`, or `sha256` (default `size`). The check runs on the partial file before the rename; on an S3 destination it runs after the write (see below). `size` requires source size = bytes streamed = destination size; `sha256` also re-reads the written file and compares digests (doubling destination reads). A failure is an `internal` error `verification failed: ...`.
  - `overwrite` (bool, default false). Without it an existing destination is a validation error, and the final rename is a plain SFTP rename that fails if the target exists (closing the race after the check). With it the rename is `posix-rename`. An SCP destination has no atomic no-clobber rename: existence is re-checked (`[ -e ]`) just before the rename, which is best effort, and `overwrite` uses `mv -f`.
  - `mkdirs` (bool).
- **Behavior:**
  - Streaming: `io.Pipe` from the source reader to the destination writer, with the SHA-256 computed in-stream. **No temporary files.**
  - Atomic write: upload to `<path>.damson-partial-<8 random hex>` (a fresh suffix per attempt, because a lost attempt and its retry could otherwise write the same file; deleted best effort on failure or cancel), then rename. The manifest is `Idempotent=false` (ADR-0048). Multipart is used for objects larger than 64 MiB.
  - S3 destination: a PUT is atomic per object, so there is no partial key and no rename (a temp key plus server-side copy would not work above 5 GiB). The object is written to the final key and then verified (`size`: HEAD size equals bytes streamed; `sha256`: re-read and hashed). A failed verification is `internal` `verification failed ... (the destination was written and is left in place)`; a replaced object cannot be restored (use a versioned bucket) and a move keeps its source. With `overwrite=false` a stat check runs first and the PUT carries `If-None-Match: *` (412 is validation `destination exists`) on servers that support conditional writes. `mkdirs` is a no-op. Objects up to 64 MiB are one PUT; larger ones use multipart with 64 MiB parts, each buffered in agent memory (about 64 MiB per S3 destination transfer).
  - If `path` ends with `/`, the source file name is appended.
  - `move` = copy, then verify, then delete the source. The source is deleted only after verification passes and the destination is in place. If the delete fails the step fails with `copied and verified; source not deleted` and the outputs stay set; the destination is never deleted. A move retried after the source is gone fails with `source not found`.
- **Output:** `destination_path` (or `destination_key`), `bytes`, `sha256`; `etag` and `version_id` (when versioned) only with an S3 destination.
- **v1 scope:** a single file per step. Globs and directories come in v1.1.
- **FTP/FTPS:** the FTP port comes from the node attribute `ftp_port` (default 21, 990 for implicit), because `NodeInfo.Port` is the SSH port; `ftps_mode: implicit` selects implicit TLS, otherwise explicit TLS. Credentials need `password` and a `username` (a credential field, else the node attribute `username`); there is no anonymous login. The server certificate is always verified against the system roots plus `agent.tls_ca_file`; there is no skip-verify. Details: `docs/reference/plugin-transfer.rst`.
- **S3 connection:** client `github.com/minio/minio-go/v7` (Apache-2.0). `endpoint` is host[:port] or an http(s) URL without a path, userinfo, query or fragment (default `s3.amazonaws.com`); `path_style` is `true`/`false` (default auto). A plain `http://` endpoint logs a warning per step; TLS uses the system roots only. Errors: `AccessDenied`, `InvalidAccessKeyId`, `SignatureDoesNotMatch`, `ExpiredToken` are `auth`; `NoSuchBucket`, invalid names and a missing source are `validation`; network failures are `connection`. A source key ending in `/` is a validation error; a destination key ending in `/` gets the source file name appended. A move from S3 deletes the object only after verification. An `https` endpoint is verified against the system roots plus `agent.tls_ca_file` (no skip-verify).
- **Windows targets:** use SFTP via Windows OpenSSH. WinRM-based file copy is out of scope.

### 6.4 `ansible`

- **Input:**
  - `repo: {url, ref, credential, submodules}`
  - `playbook` (path in the repo)
  - `requirements` (optional path to `requirements.yml`; installed from the configured internal Galaxy or mirror)
  - `extra_vars` (object)
  - `limit`, `tags`, `skip_tags`, `check`, `diff`, `forks`, `verbosity`
  - `vault_password` (secret ref)
  - `credential` (the default SSH or WinRM credential for hosts)
- **`TargetMode = Inventory`:** the controller sends `targets`. The agent generates a JSON inventory (hosts, groups from tags, hostvars from node attributes, `ansible_host`, `ansible_port`, `ansible_user`, `ansible_connection`).
- **Execution on the agent:**
  1. `git clone --depth 1 --branch <ref>` into `WorkDir`. An optional local cache keyed by URL and commit is allowed.
  2. Write credentials to `WorkDir/creds/` (mode 0600, on tmpfs when available). Remove them after.
  3. Write the embedded Python callback plugin `damson_events.py`. It emits JSON lines per task and host result plus the final recap to a file descriptor the agent reads.
  4. Run `ansible-playbook` with `ANSIBLE_CALLBACKS_ENABLED=damson_events`. Human-readable stdout is streamed as logs.
- **Output:** `stats` (per host: ok, changed, failed, unreachable, skipped), `failed_hosts`, `changed_hosts`, `commit` (the resolved SHA).
- **Error mapping:** unreachable hosts map to `connection`, failed tasks to `exit_code`.
- **Agent image:** pinned `ansible-core`, `git`, and a curated set of collections. Users can build derived images. The agent advertises `bin:ansible-playbook` and `bin:git` capabilities.

### 6.5 `buildah`

Builds a container image from a git repository and pushes it to a registry. It runs **on the agent itself** (`TargetMode = None`), in a dedicated build pool.

- **Input:**
  - `repo: {url, ref, credential, submodules}`
  - `context` (directory in the repo, default `.`)
  - `containerfile` (default `Containerfile`, falling back to `Dockerfile`)
  - `image` (for example `registry.internal/team/app`)
  - `tags` (list; default is the short commit SHA)
  - `build_args` (map), `labels` (map), `target` (stage name)
  - `build_secrets`: a map of id → secret ref, passed as `--secret id=<id>,src=<tmpfile>`. Secrets are never passed as build args.
  - `registry_credentials`: a list of secret refs of type `registry`, used for pulling base images and for pushing
  - `cache: {from, to}`: registry-based layer caching (`--cache-from`, `--cache-to`)
  - `push` (default true), `tls_verify` (default true)
  - `platform` (a single platform in v1)
- **Execution:**
  1. Shared git checkout into `WorkDir`.
  2. Write the registry auth file to `WorkDir/creds/auth.json` (0600) and set `REGISTRY_AUTH_FILE`.
  3. Run `buildah build --layers …` with all tags, streaming the output as logs.
  4. Run `buildah push --digestfile …` for each tag.
  5. Remove the local image (`buildah rmi`) and the WorkDir, so the agent stays stateless. Disk use is bounded by the build pool's ephemeral storage.
- **Output:**
  - `image`
  - `digest` (`sha256:…`)
  - `image_ref` (`<image>@<digest>`; preferred for deploys)
  - `tags`
  - `commit`
- **Error mapping:** registry auth failures map to `auth`, an unreachable network or registry to `connection`, and build failures to `exit_code`.
- **Requires:** `bin:buildah`, `bin:git`.
- **Security:** a Containerfile is arbitrary code, so builds never run on general-purpose agent pools.
- **Kubernetes:** rootless buildah needs user namespaces and `fuse-overlayfs` (or the slower `vfs` storage driver). That usually means a relaxed `securityContext` for the build pool. The Helm chart documents the exact pod spec.
- **Air-gapped:** base images come from internal registries. Nothing assumes Docker Hub.

### 6.6 `helm`

Deploys charts to Kubernetes. It runs on the agent (`TargetMode = None`).

- **Input:**
  - `action`: `upgrade` (install or upgrade; the default), `rollback`, `uninstall`, or `status`
  - `release`, `namespace`, `create_namespace`
  - `chart`, one of:
    - `{oci: "oci://registry.internal/charts/app", version}`
    - `{repo: {url, name, version}}` (a classic Helm repository)
    - `{git: {url, ref, path, credential}}` (a chart in a git repo, using the shared checkout)
  - `values`: an object. This is the natural place for interpolated build outputs such as `${{ steps.build.outputs.digest }}`.
  - `values_files` (paths within a git chart source), `set` (map)
  - `cluster`: `{kubeconfig: <secret ref of type kubeconfig>, context}` **or** `{in_cluster: true}` (the agent's own service account)
  - `atomic` (default true), `wait` (default true), `timeout` (default `10m`)
  - `registry_credentials` (for OCI charts)
  - `rollback_revision` (for `action: rollback`; default is the previous revision)
- **Execution:**
  1. Write `values` to `WorkDir/values.json`.
  2. Write the kubeconfig to `WorkDir/creds/` (0600).
  3. Run the helm command. `upgrade` and `status` use `--output json`; after `rollback`, the plugin follows up with `helm status --output json`.
- **Output:** `release`, `namespace`, `revision`, `status`, `chart`, `chart_version`, `app_version`.
- **Requires:** `bin:helm`, plus `bin:git` for git chart sources.

**Example: build, deploy, smoke test, and roll back on failure.** With `atomic: true`, Helm already rolls back a failed upgrade on its own. The `catch` covers failures *after* a successful upgrade, such as the smoke test.

```json
"steps": [
  { "id": "build", "plugin": "buildah",
    "with": { "repo": { "url": "https://git.internal/team/app.git", "ref": "${{ inputs.ref }}",
                        "credential": "secret://team/git-ro" },
              "image": "registry.internal/team/app",
              "tags": ["${{ inputs.ref }}"],
              "registry_credentials": ["secret://team/registry-push"] } },
  { "id": "deploy", "type": "group",
    "steps": [
      { "id": "upgrade", "plugin": "helm",
        "with": { "release": "app", "namespace": "team-app",
                  "chart": { "oci": "oci://registry.internal/charts/app", "version": "2.3.0" },
                  "values": { "image": { "repository": "registry.internal/team/app",
                                         "digest": "${{ steps.build.outputs.digest }}" } },
                  "cluster": { "kubeconfig": "secret://team/kube-prod" } } },
      { "id": "smoke", "plugin": "ssh",
        "with": { "node": "ops-bastion", "command": "curl -fsS https://app.internal/healthz" } }
    ],
    "catch": [
      { "id": "rollback", "plugin": "helm",
        "with": { "action": "rollback", "release": "app", "namespace": "team-app",
                  "cluster": { "kubeconfig": "secret://team/kube-prod" } } }
    ],
    "rethrow": true }
]
```

---

## 7. Agent ↔ controller protocol

### 7.1 Transport

- `wss://<controller>/agent/v1/connect`. Plain `ws://` is only allowed with `--insecure-dev`.
- **Authentication** is in the upgrade request header `Authorization: Bearer <pool token>` plus `X-Damson-Agent-Id: <instance id>`.
  - Pool tokens are created by admins per pool, stored hashed (SHA-256), and revocable.
  - The instance id is the pod name or hostname plus a boot nonce.
  - Optional mTLS can be layered on top (config only).
- **Frames:** binary WebSocket frames. Each frame is one MessagePack-encoded JSON-RPC 2.0 object (request, notification, or response). Maximum frame size is 4 MiB.
- **Both sides** can send requests and notifications.

### 7.2 Methods

| Direction | Method | Kind | Purpose |
|---|---|---|---|
| A→C | `agent.hello` | request | Protocol version, agent version, pool, labels, capacity, capabilities, full plugin manifests (§6), **list of executions currently running or with undelivered results** |
| C→A | (hello response) | | Accepted protocol version, heartbeat interval, limits, reconciliation decisions (continue / cancel per attempt_id) |
| A→C | `agent.heartbeat` | notification | Every 10s: running attempt_ids, free slots, CPU, memory, load, spool size |
| C→A | `step.execute` | request | `attempt_id`, step path, plugin name and version, resolved input, secret bundle, node connection info, timeout, trace context. The response means accepted or rejected. |
| A→C | `log.append` | notification | `attempt_id`, `seq_from`, entries `[ts, stream, line]`; batched every 200 ms or 64 KiB |
| C→A | `log.ack` | notification | `attempt_id`, highest contiguous `seq` persisted (lets the agent free its buffer) |
| A→C | `step.progress` | notification | Optional progress percentage and message |
| A→C | `step.complete` | request | `attempt_id`, status, outputs, error, timings. **Retried until acknowledged**, including across reconnects. |
| C→A | `step.cancel` | request | `attempt_id`, grace period |
| C→A | `agent.drain` | notification | Stop accepting new work (controller or agent shutdown) |
| C→A | `agent.reconnect` | notification | The controller is shutting down; reconnect via the LB after jitter |
| A→C | `node.hostkey_observed` | notification | TOFU host key report |

#### Wire format (`internal/protocol`)

Params and results are MessagePack maps with snake_case keys; unknown keys are ignored so a newer peer can add fields within protocol N and N-1. Standard JSON-RPC error codes are used (`-32700` parse, `-32600` invalid request, `-32601` method not found, `-32602` invalid params, `-32603` internal). Notification handlers run in arrival order; request handlers run concurrently.

- `log.append` entries are arrays of at least three elements `[ts, stream, line]`; extra trailing elements are ignored (forward compatibility within N and N-1), fewer than three is an error.
- Application error codes in the `-32000` range (additive; a peer that does not know a code treats it as a generic error, so N-1 compatibility holds): `-32000` `CodeServerOverloaded`, `-32001` `CodeStaleSession` (hello refused: a newer `session_seq` owns the agent), `-32002` `CodePoolConflict` (the agent id is registered in another pool), `-32003` `CodeUnsupportedProtocol` (hello `protocol` is neither N nor N-1).
- Error `-32000` (`CodeServerOverloaded`) is returned when a peer already has 64 incoming requests in flight (per-peer cap, `DefaultMaxInFlight`); the handler is not run and the caller may retry later.
- Null ids: a request with `"id": null` is invalid and answered with `-32600` and id null. A response with a null or absent id is dropped and never answered, so two peers cannot echo errors forever. `DecodeMessage` rejects such responses (`ErrNullIDResponse`), so a peer's own `-32700` reply (id null) must be read with the raw `Decode`.
- `Peer.Run` may be called once per peer; a second call returns `ErrAlreadyRunning`.
- Because request handlers run concurrently, ordering between requests is not guaranteed: a `step.cancel` sent right after `step.execute` can be handled first. Receivers must tolerate cancel-before-execute (record the cancel and apply it when the execute arrives).
- Handler panics are contained and answered with `-32603`; only the panic type and stack are logged, never the value, because handlers may carry secret bundles.

| Method | Fields |
|---|---|
| `agent.hello` params | `protocol`, `session_seq`, `agent_version`, `pool`, `labels`, `max_concurrent`, `capabilities`, `manifests` (the §6 manifest as a map), `executions` (`[{attempt_id, state: running\|finished}]`) |
| `agent.hello` result | `protocol` (accepted), `heartbeat_interval_ms`, `limits` (`max_frame_bytes`, `log_batch_bytes`, `log_batch_interval_ms`), `reconciliation` (`[{attempt_id, action: continue\|cancel}]`) |
| `agent.heartbeat` | `running_attempts`, `free_slots`, `cpu_percent`, `memory_bytes`, `load1`, `spool_bytes` |
| `step.execute` params | `attempt_id`, `execution_id`, `step_path`, `plugin`, `plugin_version`, `input` (object), `secrets` (`{ref -> {field -> value}}`, §9.3), `nodes` (`{name -> NodeInfo}`, §11), `timeout_ms`, `trace` (`traceparent`, `tracestate`) |
| `step.execute` result | `accepted`, `reason` (set when rejected) |
| `log.append` | `attempt_id`, `seq_from`, optional `gap_to` (uint64, omitted when unset), `entries`: each entry is the array `[ts_unix_ns, stream, line]`. Without `gap_to`, entry i has seq `seq_from + i`. With `gap_to` the agent declares seqs `seq_from..gap_to` inclusive dropped and never to be sent; entry i then has seq `gap_to + 1 + i`. The controller treats a declared gap as contiguous when it computes `log.ack`. Additive and optional, so N-1 compatible: a peer that ignores `gap_to` sees a hole it must already tolerate. |
| `log.ack` | `attempt_id`, `seq` |
| `step.progress` | `attempt_id`, `percent` (0 to 100), `message` |
| `step.complete` params | `attempt_id`, `status` (`succeeded`, `failed`, `canceled`, `timed_out`), `outputs`, `error` (`code`, `message`, `retryable`, optional `exit_code`), `started_at_ns`, `finished_at_ns` |
| `step.complete` result | `acknowledged`, `stale` (result discarded by fencing; the agent stops retrying either way) |
| `step.cancel` params / result | `attempt_id`, `grace_ms` / `known` |
| `agent.drain` | `reason` |
| `agent.reconnect` | `jitter_ms`, `reason` |
| `node.hostkey_observed` | `attempt_id`, `node`, `key_type`, `host_key`, `fingerprint` |

The controller applies `node.hostkey_observed` only if the attempt exists, belongs to the reporting agent and is unfinished, and `node` is one of the step's node names (`Manifest.NodeNames` of the step input; ADR-0046, ADR-0048). It re-parses the key and recomputes type and fingerprint (a mismatch is dropped), and stores the canonical `authorized_keys` form without comment. Drops are silent (INFO log) (ADR-0046).

The `secrets` member is the only place secret values cross the wire. The Go type redacts itself in `fmt`, `slog` and `encoding/json` output.

### 7.3 Reliability rules

- **Fencing.** Every Execution has a unique `attempt_id` (UUIDv7). The controller applies `step.complete`, logs, and leases only if the `attempt_id` matches the Execution's current attempt and the execution is not terminal. Stale results are acknowledged and discarded.
- **Leases.** Each running Execution has a `lease_expires_at` of now + 60s.
  - Heartbeats renew leases for every attempt_id they list. Renewal is batched, with one UPDATE per controller every 10s.
  - The **reaper** marks expired leases as `lost`.
- **Reconnect.** The agent reconnects with exponential backoff (1s → 30s, full jitter) and keeps executing during the gap.
  - On reconnect, `agent.hello` lists the running and finished-but-unacked attempts.
  - The controller takes ownership (`controller_id`) and replies continue or cancel for each. It answers `continue` for a `finished` attempt that is still the current attempt of an execution owned by that agent (the agent redelivers `step.complete`). A current, non-terminal attempt of that agent (`running`, `dispatched`, or `finished` and awaiting redelivery) is **adopted** (`AdoptExecutions`: `controller_id` and the lease move to this controller, in one CAS fenced on the agent row's `controller_id` and `session_seq`, which Postgres share-locks so a concurrent newer hello cannot slip in) and answered `continue`, so a reconnect to another controller keeps the step running. Everything else (unknown, terminal, another agent's, or lost the fence to a newer session) is answered `cancel`. A store failure during reconciliation fails the hello, and the agent reconnects, so a database blip never cancels running steps.
  - Ownership is ordered by `session_seq` (a per-process connection counter sent in `agent.hello`), not wall clocks: only a hello with a strictly higher `session_seq` takes over the agent row.
- **Log buffering.** The agent keeps unacked log batches in memory, bounded at 32 MiB, then spills to a bounded local spool file (256 MiB default).
  - If the spool overflows, the oldest unacked batches are dropped. The hub replaces them in its queue by one placeholder that is sent as `log.append` with `seq_from` = first dropped seq, `gap_to` = last dropped seq minus one, and a `System` log line carrying the last dropped seq, so no seq is lost and acks stay contiguous. The placeholder lives in the hub, so it is delivered even after the attempt's pipe has closed.
  - The spool file is unlinked right after creation (stale `damson-spool-*` files from a crashed agent are swept at start) and holds masked job output in plaintext, mode 0600.
  - The spool is best-effort: an agent crash loses unacked logs. That is an accepted trade-off of stateless agents.
- **Capacity.** The agent advertises `max_concurrent` (default: CPU count × 4). The free-slot count is a soft in-memory value seeded by `agent.hello` and refreshed by heartbeats. It can briefly exceed reality, and the agent's "no free slot" rejection (the execution is requeued) is the authority, so "never dispatches beyond free slots" is best effort.
- **Execution on the agent.** Pinned behavior of the agent executor (`internal/agent`); no message changes.
  - `step.execute` is accepted only for a built-in plugin with exactly the requested name and version and a free slot; otherwise it is rejected with a reason. A repeated execute for a known attempt is accepted without running twice.
  - A `step.cancel` for an attempt the agent does not hold leaves a tombstone (15 minutes, bounded), so a `step.execute` that overtakes or follows it is rejected. A cancelled plugin has `grace_ms` (default 5s) to return; after that the step is reported `canceled` and the plugin keeps its slot until it returns.
  - Log `seq` starts at 1 per attempt and increases by one per entry. Unacknowledged batches are resent in order after a reconnect, so ingest must be idempotent.
  - A finished result stays in memory and in the `agent.hello` `executions` list until `step.complete` is acknowledged or stale. A reconciliation `cancel` for a finished attempt discards it. The attempt's logs are dropped after the acknowledgement only when none is unsent. Delivery failures of one result (error answer, acknowledgement with neither `acknowledged` nor `stale`) back off per result (exponential, jittered, capped at 30s) without blocking other results; only transport errors end a delivery round.
  - Acknowledged and discarded attempts are remembered with the cancel tombstones (15 minutes, bounded), so a late or retried `step.execute` for them is rejected as `already completed`.
  - Reconciliation does not cancel a flagged attempt (`executions.cancel_requested_at`, §8.3 step 7): it answers `continue` as for any current attempt, because a reconciliation `cancel` would discard a result the agent already finished. The controller that adopts the attempt sends the `step.cancel` on its next poll.
  - Memory bounds: outputs of one step are limited to 1 MiB (larger fails the step), error messages to 8 KiB, so `step.complete` always fits `MaxFrameSize`; at most 1024 unacknowledged results are kept, for at most 6 hours, oldest dropped first.
  - Secret values live only in the executor's memory; masking covers log lines, progress, errors, string outputs, host key reports and the plugin logger (§9.4). Overlapping secrets are masked as the union of their matches. `endpoint`, `region` and `path_style` of an S3-shaped secret are not masked when strictly shaped (ADR-0051). Values shorter than 4 bytes are not masked in text (they would destroy output); the step log gets one `System` line saying so, never the value. A secret split across two `Log` calls is not masked: plugins must log whole lines.
- **Protocol versioning.** `agent.hello` carries `protocol: 1`. The controller supports N and N-1.

---

## 8. Orchestration and HA

### 8.1 Principles

- **The database is the only source of truth.** Controllers are interchangeable and hold only caches and live sockets.
- **No consensus inside Damson.** Coordination uses:
  - row-level compare-and-swap (`UPDATE … WHERE state = $expected` + rows-affected check)
  - `SELECT … FOR UPDATE SKIP LOCKED` (Postgres)
  - one **leader lease** row per singleton duty
- **SQLite mode runs exactly one controller.** This is enforced with a `leader_leases` row named `sqlite-controller` (§8.4): the controller takes it at startup and renews it, a second controller on the same database refuses to start while it is held (and waits out the lease of one that crashed, at most the lease TTL), and a graceful stop releases it. It is a misconfiguration guard, not a fence (correctness rests on compare-and-swap), and it protects a database on local disk only, not one on a network file system.

### 8.2 The engine is a pure function

```
Decide(spec JobVersionSpec, state RunState, now time.Time) -> []Action
Action = CreateStepInstance | ScheduleExecution | SettleStep | SkipStep | CompleteGroup | CompleteRun | CancelExecution | TimeoutRun | ScheduleWake
```

- Implemented as `(*engine.Engine).Decide(job, state, now) ([]Action, error)`; the `Engine` only holds the compiled-expression environment. `Decide` walks the step lists recursively by path: `StartRun`, `FailStep` (a step or group whose `when` or `with` failed to evaluate), `SkipStep` (which also creates the instance, `skipped` or `cancelled`), `CreateStepInstance` with `Kind: "group"` for a group and `CompleteGroup` when its steps are done (ADR-0037).
- `when` follows the implicit `success() &&` rule of §5.2/§5.3 rule 3.
- No I/O, fully deterministic, exhaustively table-tested, including golden tests over JSON fixtures.
- All control-flow semantics in §5.3 live here and nowhere else.

### 8.3 Run lifecycle

1. **Trigger** (UI, API, schedule, or webhook):
   - validate options
   - resolve targets
   - check concurrency (`queue` | `skip` | `cancel_previous`)
   - insert the Run
   - enqueue River job `run.advance{run_id}`
   - all in **one transaction**
2. **`run.advance`** (River, any controller):
   - Load the run state.
   - Call `Decide`.
   - Apply actions transactionally, with optimistic concurrency on `runs.version`.
   - River uniqueness is by `(run_id, runs.version)`: identical enqueues collapse, but an enqueue after a state change is kept even while an advance is running. River cannot exclude `running` from the unique states, and a job keyed on `run_id` alone would swallow a wake-up that arrives after the running advance read its state. Every event that must wake a run bumps `runs.version` in its own transaction (`Tx.TouchRun`), and the advance applies with a CAS on that version, so concurrent advancement is safe and a lost wake-up is impossible.
   - A periodic sweep (default 1m, any controller, and once at start) re-enqueues every `pending` or `running` run as a safety net for crash windows and runs created before the queue existed.
   - A River job scheduled for the job-level timeout's deadline (`ScheduleWake`, args `wake` = the deadline in ms, version 0) is queued in the transaction that starts the run. A worker that runs it before its own clock reaches the deadline snoozes (a snooze is not an attempt). If it is lost, the sweep times the run out within one interval.
   - A sweep job is unique by `(run_id, runs.version, sweep epoch)`, the epoch being the time cut to the sweep interval. A controller that dies mid-advance leaves its job `running` at an unchanged version; the sweep's job has a key of its own and is not swallowed, so the run moves again within one sweep interval. River's rescuer (`JobTimeout` 1m, `RescueStuckJobsAfter` 2m) only clears the stranded rows, and `MaxAttempts` is 5.
3. **Dispatch** (every controller, for its own sockets):
   - A loop wakes on a notify hint or every 1s.
   - Claims queued, due Executions that match the pool of its connected agents with free slots. The claim filters by pool only; plugin and capability matching happens after the claim:
     ```sql
     UPDATE executions SET state='dispatched', agent_id=$1, controller_id=$2, attempt_id=$3,
            lease_expires_at=now()+interval '60s'
     WHERE id IN (SELECT id FROM executions
                  WHERE state='queued' AND scheduled_at<=now() AND pool = ANY($4)
                  ORDER BY priority, scheduled_at
                  FOR UPDATE SKIP LOCKED LIMIT $5)
     RETURNING ...
     ```
   - Then resolves secrets (§9) and sends `step.execute`.
   - An execution claimed by an agent that cannot run it is requeued with a 5s delay. Known limitation: incapable agents in the pool churn it.
   - Definite failures (agent rejected, plugin unresolved) CAS the Execution back to `queued` at once.
   - Delivery is at-most-once on an ambiguous send. If `step.execute` times out or the socket closes after the write, the controller sends `step.cancel` for the attempt and requeues only if the cancel is answered. Otherwise the execution stays `dispatched` and the lease reaper marks it `lost` (the agent also tombstones a cancelled attempt, so a late execute is rejected).
   - Dispatch rounds run per agent, asynchronously, with one in flight per agent. After a transport failure the rest of that agent's claimed batch is requeued immediately.
4. **Running:** logs flow (§10), and heartbeats renew leases.
5. **Complete:** `step.complete`, then a CAS on `attempt_id` to set the terminal state and store outputs and error, then enqueue `run.advance`. The store finishes the step in the same transaction only for a `succeeded` or `canceled` attempt; a failed, timed-out or lost attempt leaves it pending for the engine (rule 6, retries). The agent gets its ack after the commit. An error the agent reports is stored on the execution as `{"kind", "message"}` JSON (kind = the agent's error code, which is `timeout` for `timed_out`), the shape of the engine's own `FailStep`. The step's error always carries a kind (`StepOutcome`): the stored one, else one derived from the final state (`timeout`, `lost` for an expired lease, `error`), so `error.kind` and `retry_on` can match it. Execution errors written as plain text (lease reaper, queued cancel, an unsendable execution) and older rows still read: their kind comes from the state. The API shows `error` (the message) and `error_kind` separately.
6. **Retry:** the engine schedules attempt n+1 (a new Execution with a new attempt_id and `scheduled_at` in the future, which the claim loop skips until due), or emits `SettleStep`, after which the store finishes the step from that attempt (§5.3 rule 7).
7. **Cancel:**
   - Set `runs.cancel_requested_at` (and `runs.cancel_reason`: empty for a user or API cancel, `timeout` for the job-level timeout, which `run.advance` records itself).
   - `run.advance` marks pending steps as cancelled. A queued Execution becomes `canceled` with its step. A `dispatched` or `running` one is flagged (`executions.cancel_requested_at`, not fenced by `attempt_id`, because a claim between the advance's read and its apply rotates it) and the engine stops emitting the cancel once the flag is visible. A queued row never carries the flag.
   - For flagged Executions, the owning controller (`executions.controller_id`) sends `step.cancel` with `grace_ms` (default 5s). The hint is delivered via `LISTEN/NOTIFY` (Postgres) plus a 2s poll fallback. Controllers skip agents that have no socket to them: after the agent reconnects the controller that adopts it sends the cancel, and if it never returns the lease reaper settles the attempt.
   - The agent's `step.complete` is applied as reported through the normal fenced path: `canceled` ends the step and the run; a plugin that finished anyway reports `succeeded` and is applied as such.
   - A flagged attempt that was claimed but never delivered (the agent rejected it, the send failed, or its cancel was answered) is not requeued: it goes `canceled` (`CancelUndelivered`), because a requeued flagged row would run again.
   - `finally` blocks run after cancellation, within the finally grace (`engine.finally_grace`, §5.3 rule 5): protected from `step.cancel` until the grace ends, capped by a `timeout`, with a wake scheduled for its end.

### 8.4 Singleton duties (leader lease)

- `leader_leases(name, holder, term, acquired_at, expires_at)`: one row per singleton duty (today only `reaper`). `holder` is the controller id, with no foreign key (`controller_instances` rows are deleted on shutdown). ADR-0039.
- **Acquire or renew** is one upsert (`LeaderStore.AcquireLeader`) every 5s with a TTL of 15s. It succeeds if the caller is the holder or the current lease has expired (`expires_at < now`). A takeover adds 1 to `term`, a renewal keeps it; `term` never resets. **Release** is `UPDATE ... SET expires_at = now WHERE holder AND term match`, so a graceful stop hands over faster than the TTL.
- **Clock:** the injected Go `now`, on both backends, not the database clock. Skew only shifts failover time (TTL plus skew); it cannot cause flapping.
- **Step-down:** a controller counts as leader only while `now < expires_at - renew`, so one cut off from the database stops before another can take over.
- **Fence:** `Tx.CheckLeader(name, holder, term, now)` returns `ErrStale` unless the lease is held at that term and unexpired. The reaper calls it first in its transaction. It is strict on SQLite (exclusive transaction) and best effort on Postgres READ COMMITTED, which is enough for idempotent duties.
- Duties:
  - **scheduler** (§12.3)
  - **reaper** (expired leases → `lost`; dead `controller_instances` cleanup)
  - **retention** (old runs, logs, audit; per-project policy)
- Each duty is idempotent, so a brief double-leader overlap during failover is harmless.
- Per-controller duties stay on every controller: dispatch, lease renewal, cancels, adoption, and the sweep (a River unique key). The scheduler (M7) will need the lease plus a unique `(schedule_id, fire_at)`.
- Reaper and complete transactions touch rows in a fixed order: step rows first, then each distinct run once, sorted by run id.

### 8.5 Controller registry and cross-controller routing

- `controller_instances(id, internal_url, started_at, last_seen)`, heartbeated every 5s.
- The reaper leader deletes rows whose `last_seen` is older than 1 minute (crashed controllers never deregister). Agent rows are not touched.
- **Log tail:** a UI request to controller X for an execution owned by controller Y is proxied to `Y/internal/v1/executions/{id}/tail` (ADR-0040).
  - The endpoint is on a separate internal listener (`controller.internal_listen`, default `:8081`), started only when a cluster secret is set and never in the dev role. The public mux answers 404 for `/internal/`.
  - Requests are signed with an HMAC of a cluster shared secret (`controller.cluster_secret`, with `cluster_secret_previous` for rotation). Postgres controllers must set the secret and `controller.internal_url`; SQLite and dev need neither.
  - X tries its own ring first, then the owner named by the execution's `controller_id` and `controller_instances.internal_url`. There is no live tail when the owner has not been seen for 15s, or the execution is terminal, unowned or ours. The owner serves only its own ring and never forwards.
  - Any failure means no live tail: the SSE log stream polls stored chunks and dedupes by `seq`, and retries the live tail every 5s so an adopting controller is picked up.
  - Transport is plain HTTP, so log lines cross the controller network unencrypted. It needs a private network; TLS can follow.
- **Graceful shutdown:**
  1. Stop the HTTP listener for new work.
  2. Send `agent.reconnect` to all sockets.
  3. Flush log buffers.
  4. Deregister.
  
  Running executions continue on the agents and re-attach to other controllers.

### 8.6 Scale notes (1,000 agents)

- About 100 heartbeats/s in total, handled in memory. Database writes are batched (last_seen and lease renewals once per 10s per controller).
- Agent telemetry goes to OTel metrics, and the last snapshot is stored on the `agents` row in a batch.
- Three controllers with roughly 333 sockets each is trivial for Go. The real load is log ingestion (§10).

---

## 9. Secrets

### 9.1 References

- Format: `secret://<scope>/<name>[#<field>]`, for example `secret://infra/billing/deploy-key#private_key`. The scope is all segments but the last.
- A job may reference secrets in its own project or any **ancestor** project (prefix match on a `/` boundary). Siblings, children and other projects are denied. The dispatcher enforces this before any secret is read; a denied ref fails the execution as `unknown secret`, so existence is not revealed. API spec validation rejects literal refs that are not authorized.
- Inventory-level credentials live under the reserved `secret://_inventory/...` scope (`_inventory` is allowed only as the whole scope). They are usable only as the credential of the node a step targets, never as a literal `secret://_inventory/...` in step input, by any job whose project node scope includes the node.
- Node scope today (ADR-0043): dispatch asks the `inventory.Scope` hook, and `AllNodes` matches every node until M8 builds the project tree and the CEL `node_scope` (§12.1); there is no `projects.node_scope` column yet. A missing node and a node outside the scope both fail the step permanently as `unknown node`, so existence is not revealed. A disabled node fails permanently as `node is disabled`; a node deleted after the run started fails permanently as `unknown node`. Host keys at dispatch (ADR-0046): `strict` sends `host_key` (empty fails at connect); `tofu` sends the pinned `host_key`, else the observed key, else none; a node with policy `insecure` fails permanently as `host key policy not available`. The controller handles `node.hostkey_observed` fenced like `step.complete` and stores the first key only (`ObserveNodeHostKey`); `DELETE /api/v1/nodes/{name}/host-key` forgets it. A database error requeues. Plugins with `TargetMode = nodes` (ADR-0048) get every named node resolved, scoped and credentialed this way; `step.execute.nodes` is keyed by name. The node credential goes through the same secret authorization, with `_inventory` allowed only as a node credential.
- Grammar (ADR-0042):
  - A scope segment matches `[a-z0-9][a-z0-9-]{0,62}`, 1 to 8 segments, so a leading `_` is impossible.
  - `name` matches `[A-Za-z0-9][A-Za-z0-9._-]{0,127}` and has no `..`.
  - `field` matches `[a-z][a-z0-9_]{0,63}`.
  - At most 512 bytes, ASCII only.
- Types (the type of a secret cannot change):
  - `generic`: any fields.
  - `password`: `username`, `password` (required).
  - `ssh_key`: `username`, `private_key` (required, multiline), `passphrase`.
  - `token`: `token` (required).
  - Other types (`s3`, `registry`, `kubeconfig`) are not implemented yet. Until `s3` is, an S3 connection for the `transfer` plugin is a `generic` secret with the fields listed in section 6.3.
- Limits: 64 KiB per field, 256 KiB per secret, 32 fields.

### 9.2 Backends

```go
type SecretStore interface {
    Get(ctx context.Context, ref Ref) (Secret, error)
    Put(ctx context.Context, ref Ref, s Secret) error   // may return ErrReadOnly
    Delete(ctx context.Context, ref Ref) error
    List(ctx context.Context, projectPath string) ([]Metadata, error) // never values
}
```

- **`db` (default).** AES-256-GCM envelope encryption (ADR-0042).
  - Each secret version gets a random 32-byte DEK. `data` is AES-256-GCM over the JSON `{field: value}` with a random 12-byte nonce; `wrapped_dek` is a 12-byte wrap nonce followed by AES-256-GCM(KEK, DEK).
  - The KEK is 32 bytes in standard base64, from `DAMSON_KEK` or the file `secrets.kek_file` (both set is a config error). `DAMSON_KEK_PREVIOUS` or `secrets.kek_previous_file` holds the previous key during rotation.
  - `kek_id` = `hex(SHA-256("damson-kek-id/v1" || key))[:16]`, stored per version. Only `kek_id`s appear in logs and errors.
  - AAD of the data is `"damson-secret/v1\0" + scope + "\0" + name + "\0" + version`; the wrap uses the same AAD plus `"\0" + kek_id`. A ciphertext moved to another row or version fails to decrypt.
  - Canary: the `instance_settings` key `secrets.kek_canary` holds `{kek_id, nonce, ct}` of a fixed plaintext under AAD `"damson-canary/v1"`. Controller start: no KEK and no canary disables secrets (WARN; dispatch of a `secret://` step fails permanently); no KEK with a canary refuses to start; a KEK without a canary stores it (a canary written by a peer is verified); a canary under the active key starts; under the previous key starts with a WARN to run `damson admin rotate-kek`; under neither refuses to start.
  - The last 10 versions are kept. Listing never reads encrypted columns.
  - `damson admin rotate-kek` (env `DAMSON_KEK` = new key, `DAMSON_KEK_PREVIOUS` = old key) checks the canary, then pages through `secret_versions` whose `kek_id` is not the active one (keyset on `secret_id, version`, batches of 100) and re-wraps each DEK under the new key with `UPDATE … WHERE secret_id=? AND version=? AND kek_id=old`; the data ciphertext is untouched. It is resumable and a second run is a no-op. Rows under a key that is neither active nor previous count as failed (non-zero exit) and the canary is then not moved; otherwise the canary is re-wrapped last (CAS on its version). Output has counts and the active `kek_id` only.
  - Losing the KEK means losing the secrets. The docs must say so loudly.
- **`openbao`** (Vault-API compatible). KV v2.
  - Ref mapping: `<mount>/data/<prefix>/<project-path>/<name>`.
  - Controller auth: token, AppRole, or Kubernetes auth.
  - Optional `read_only: true`, so secrets are managed in OpenBao only.
  - No caching by default. Optional TTL cache: 30s, in memory, per controller.
- Configure a backend globally in v1. Per-project backends come later; the interface already allows it.

### 9.3 Delivery

- At dispatch, the controller resolves every `secret://` ref in the step input and the referenced nodes' credentials into a **secret bundle** `{ref → fields}`.
- The bundle is sent inside `step.execute` over TLS.
- It is never persisted by the controller (only refs are stored) and never written to disk by the agent, except as short-lived 0600 credential files that Ansible requires, on tmpfs when available.
- The agent drops the bundle when the execution ends.
- `--dev-nodes` seeds its secrets into the secret store at startup (ADR-0044); there is no separate dev path for refs.

### 9.4 Masking

- The agent builds a masker from all secret field values in the bundle, except the non-secret S3 connection fields `endpoint`, `region` and `path_style` of a secret with non-empty `access_key` and `secret_key` when the value has a strict shape (ADR-0051; anything failing a guard is masked, `access_key` and `session_token` stay masked), including base64 and URL-encoded variants and each line of multi-line values longer than 4 characters.
- It masks every log line before sending, and scans outputs. Any output value containing a secret is replaced with `***` and a warning is logged.
- Only the agent masks (ADR-0047). The controller does not re-mask: after `step.execute` is sent it keeps no secret values, hashes or derived material (the bundle is wiped after send), and what it stores and serves (log chunks, ring buffer, live tail, outputs, errors, progress, host key reports) is exactly what the agent sent.
- The agent masks at every exit (log lines, progress, plugin logger, errors, outputs, host key reports), fails closed after the bundle is wiped, and only masked lines reach the spool.
- Known limits: values shorter than 4 bytes; lines of 4 characters or fewer inside multi-line values; secrets split across `Log` calls; values the job transforms (hex, reversed, re-encoded); a compromised agent, which holds the plaintext anyway. Masking is defense in depth, not a guarantee.

---

## 10. Logs

- **Path:** plugin → emitter → masker → agent batcher (200 ms / 64 KiB) → `log.append` (MessagePack) → owning controller.
- **Controller, per running execution:**
  - A **ring buffer** of the last 5,000 lines for live tail.
  - A **chunk writer** that flushes to the LogStore every 5s or 1 MiB, whichever comes first, and inserts a `log_chunks` row (`execution_id, seq_from, seq_to, object_key, bytes, ts_first, ts_last`).
  - After the row commits, a `log.ack` goes to the agent.
- **On completion:** a final flush, then optional compaction into a single gzipped object.
- **LogStore interface:** `Put(key, bytes)`, `Get(key)`, `Delete(prefix)`.
  - `db` (**default**, including HA production): chunks stored as bytea or blob in a dedicated `log_chunks_data` table. This keeps the required infrastructure to Postgres only. Retention deletes are batched to limit bloat.
  - `fs`: dev, or a single controller with a shared volume.
  - `s3` (optional): any S3-compatible endpoint, for installations whose log volume would bloat the database. Switching is a config change; old chunks stay readable through their stored backend id.
- **Object key:** `logs/<execution-id>/<attempt-id>/<seq_from>-<seq_to>.ndjson[.gz]` (the execution id is globally unique, so the project and run ids are not part of the key; sequence numbers restart for every attempt, so the attempt id is part of the key, and claiming an execution under a new attempt deletes the previous attempt's chunk rows and objects). Each line is `{"t":<unix ns>, "s":"out|err|sys", "l":"..."}`; the sequence number is implicit (line i of a chunk has `seq_from + i`), so a chunk holds a contiguous run and a declared gap splits a flush into two chunks.
- **UI read path:** `GET /api/v1/executions/{id}/logs?after_seq=N` via SSE.
  - Serves stored chunks first, then switches to live tail from the owning controller's ring buffer. If another controller owns the execution, the tail is proxied over the signed internal endpoint (8.5); when that is unavailable the stream keeps polling stored chunks and retries every 5s.
  - Gaps are reported explicitly.
- **Retention** is per project (default 30 days), enforced by the retention duty plus optional S3 lifecycle rules.

---

## 11. Inventory API

Nodes are stored in the `nodes` table and managed through the API below (ADR-0043). The dev-only `--dev-nodes` file is a seeder (ADR-0044): at startup it syncs its nodes into the table as source `devfile` and writes its secrets to the secret store; dispatch and targets then use the store as without the flag. Runs freeze their targets at trigger (ADR-0045).

Inventory is maintained by external systems (for example a ServiceNow CMDB sync running elsewhere). Damson only exposes an API.

**Node fields:** `name` (unique), `address`, `port`, `transport` (`ssh` | `winrm`), `os` (`linux` | `windows` | …), `pool`, `tags` (string list), `attributes` (string → string/number/bool), `credential` (secret ref), `host_key`, `host_key_policy`, `source` (read-only), `external_id`, `enabled`.

**Endpoints:**

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/v1/nodes?source=&enabled=&cursor=&limit=` | List and filter; `selector` (≤4096 chars) is a CEL selector as in `spec.targets.selector` over the node map with empty `inputs`, no project scope; at most 2000 nodes scanned per request, so follow `next_cursor` even when a page is short; invalid selector is `422` at `query.selector` |
| `GET` | `/api/v1/nodes/{name}` | Get one |
| `POST` | `/api/v1/nodes` | Create (`source=manual`) |
| `PATCH` | `/api/v1/nodes/{name}?force=` | Update fields; optional `if_version` |
| `DELETE` | `/api/v1/nodes/{name}?force=&if_version=` | Delete |
| `PUT` | `/api/v1/inventory/sources/{source}/nodes?dry_run=bool&prune=bool` | **Full-state sync** for one source; body `{nodes: [SyncNode]}`. Upserts all nodes (matched by `external_id`, falling back to `name`). With `prune`, it deletes nodes of that source missing from the body. Returns `{dry_run, created, updated, deleted, unchanged, conflicts}` (node names; conflicts are `{name, owned_by}`). Any conflict (a desired name held by another source or by an own node that stays) is a `409` of type `urn:damson:problem:inventory-sync-conflict` with nothing written, also in dry run. Idempotent. |
| `POST` | `/api/v1/inventory/sources/{source}/nodes:batch?dry_run=bool` | Incremental; body `{upserts: [SyncNode], deletes: [{external_id \| name}]}`. Never prunes. A delete matches the source's own nodes by `external_id`, else by `name`; a missing node is ignored; it never reaches another source. Upsert and delete of the same node is a `422`. Same response and conflicts as `PUT`. |

Both sync routes need the admin token, accept at most 16 MiB and 5000 nodes, and run in one transaction (a concurrently changed node re-plans up to 3 times, then `409`). Push-only: Damson has no schedule or pull.

**Validation** (`inventory.Validate`)

- `name`: `^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$` (same as the `for_each` lane key); case-only duplicates conflict. Put an FQDN in `address`.
- `address`: an IP (no brackets or zone) or an RFC 1123 hostname (no port, scheme or user; at most 253 bytes).
- `port`: 1..65535; defaults 22 for `ssh`, 5986 for `winrm`.
- `transport`: `ssh` or `winrm`. `os`: `^[a-z][a-z0-9_-]{0,31}$` or empty. `pool`: a lowercase name or empty (no foreign key).
- `tags`: each at most 63 bytes, `^[A-Za-z0-9][A-Za-z0-9._:/=-]{0,62}$` and not starting with `secret:`; at most 64 tags; deduplicated and sorted.
- `attributes`: at most 64 keys (identifiers); values are a string up to 1 KiB, a finite number (stored as a float64; integers above 2^53 are refused) or a bool; no `secret://` strings; at most 16 KiB encoded.
- `credential`: empty or a whole secret ref (no `#field`) in `_inventory` or project scope; existence is checked at dispatch.
- `host_key`: empty or exactly one `authorized_keys` key, normalized without comment; not allowed for `winrm`. `host_key_policy`: `strict`, `tofu` or `insecure`. A `strict` node without a `host_key` is accepted at write time (synced nodes may get their key later) and fails at connect.
- `source`: `^[a-z0-9][a-z0-9-]{0,62}$` (`manual` and `devfile` are reserved for the API and the dev seeder). `external_id`: at most 255 bytes, no control characters, empty for `manual`. Both are in unique indexes, so their size is bounded here. Absolute FQDNs (trailing dot) and hostnames with underscores are not valid addresses.

**API semantics** (ADR-0043)

- All nodes routes need the admin token (§12.2), reads included; a missing and a wrong token get the same `401`.
- `POST` creates a `manual` node: `name` and `address` are required; `port` defaults to 22 (`ssh`) or 5986 (`winrm`), `transport` to `ssh`, `host_key_policy` to `strict`, `enabled` to true. `source`, `external_id`, `version` and `host_key_observed` are read-only and a body that sets them is a `422`.
- `PATCH` takes every field as optional; `tags` and `attributes` replace the whole value, and an empty `credential` or `host_key` clears it. `name`, `source` and `external_id` cannot change (`422`). With `if_version` a moved node is a `409`; without it the handler merges against the version it read and retries up to 3 times. Updates and deletes address the row by id and version.
- A node response has every column plus `host_key_fingerprint` and `host_key_observed_fingerprint` (`SHA256:…`), `version`, `created_at` and `updated_at`. `credential` is a ref only.
- Status codes: `201` create, `200` patch, `204` delete, `404`, `409` (duplicate name including case-only, duplicate `(source, external_id)`, stale version), `422` with locations `body.<field>`.
- `manual` and `devfile` are reserved sources. `name` is limited to 63 characters because it is a `for_each` path segment.

**Rules**

- A node belongs to exactly one source. Writing a node whose source is not `manual` without `force=true` is a `409` of type `urn:damson:problem:inventory-source-conflict`. With `force=true` the write happens but keeps `source`, so the owner's next sync overwrites the edit. Until M8 `force` needs only the admin token.
- Until service account tokens exist (M8), sync calls use the admin token; later they use **service account tokens** with the `inventory:write` scope.
- Node changes don't affect running runs, which froze their targets at start.

---

## 12. Triggers, projects, and access control

### 12.1 Projects

- A tree stored with a materialized `path`. Each project has:
  - `access_rules`
  - `node_scope`: a CEL selector over nodes; it inherits by AND with ancestors
  - `retention`
  - its secrets

### 12.2 Authentication

- **UI:** OIDC authorization code flow with PKCE.
  - Server-side sessions stored in the database, so any controller serves any request. HttpOnly, Secure, SameSite=Lax cookie.
  - Claims are snapshotted into the session and refreshed on token refresh.
- **API:** `Authorization: Bearer` with either:
  - a **service account token**: random, hashed, scoped by project subtree and role, with optional expiry
  - an **OIDC JWT access token** from the configured issuer (optional; for machine-to-machine)
- **Bootstrap admin:** a config CEL rule over claims, for example `'damson-admins' in claims.groups`, plus a one-time local recovery token printed at first start.
- **Interim admin token (until OIDC, M8):** the secrets API and the nodes API (§14) are guarded by an **admin bootstrap token** (`dma_` plus base64url of 32 random bytes), created with `damson admin admin-token create` and printed once. Only its SHA-256 hash is stored, in `instance_settings` key `auth.admin_token_hash`; creating a new one replaces it (the old one stops working at once) and `admin-token revoke` clears it. The API reads the hash on every request. Pool tokens are never accepted here and the admin token is not accepted on `/agent/v1/connect`. M8 keeps it as the local recovery token. Until M8 it is one shared token for all admins.

### 12.3 Triggers

| Trigger | Details |
|---|---|
| **UI button** | A run dialog generated from `spec.options` (JSON Schema form) |
| **API** | `POST /api/v1/projects/{path}/jobs/{name}/runs` with `{options}` |
| **Schedule** | `schedules(job_id, cron, timezone, options, enabled, misfire: run_once|skip, next_fire_at)`. The scheduler duty ticks every 10s: it selects due schedules, creates runs (unique on `(schedule_id, fire_time)`), and advances `next_fire_at`. |
| **Webhook** | `POST /hooks/{token}` |

**Webhook details**

- The token is 32 random bytes in base64url (43 chars). It is **shown once** at creation, stored as SHA-256, and looked up by hash.
- A job can have multiple named webhooks, each revocable, with `last_used_at`.
- Optional `options_mapping`: a CEL map from `{body, headers, query}` to options, validated against `spec.options`.
- Returns `202 {run_id}`.
- Rate-limited per token.
- The token is a credential. Logs must redact `/hooks/*` paths.

### 12.4 Authorization

- **Roles:** `viewer` < `runner` < `editor` < `admin`.
  - `runner` can trigger and cancel.
  - `editor` can change jobs, schedules, and webhooks.
  - `admin` can change access rules, secrets, and child projects.
- **Rules** are CEL expressions over `claims` (raw OIDC claims) and `user` (`sub`, `email`, `groups`):
  ```json
  { "role": "runner", "rule": "'billing-ops' in claims.groups" }
  ```
- They attach to **projects** (inherited by descendants) and optionally to **jobs** (additive for that job only).
- **Effective role** = the maximum role granted by any matching rule on the job, the project, or any ancestor.
- Rules are compiled once per change, and evaluation is cached per session for 60s.
- Every mutation and trigger writes an **audit event**: actor, action, target, details JSON, request id.

---

## 13. Jobs as code (prepared for Git)

**Folder layout:**

```
jobs/
  infra/
    project.json                 # project metadata, access_rules, node_scope
    billing/
      project.json
      deploy-report-tool.job.json
```

**`damson apply -f ./jobs --source files:main [--prune] [--dry-run]`**

- The CLI bundles the tree and calls `POST /api/v1/apply`.
- The server validates everything first (all-or-nothing), diffs by checksum, creates new JobVersions only for changed specs, and sets `managed_by = files:main`.
- With `--prune`, it deletes jobs managed by that source that are no longer present.

**Other rules**

- Jobs managed by a source are **read-only in the UI**, which offers "export" and shows the source.
- `damson export --project infra` writes the same layout.
- **Future Git sync:** a duty that fetches a configured repo and ref and feeds the *same* apply engine with `source = git:<name>`. No changes to the apply logic are needed.

---

## 14. HTTP API (outline)

All routes are under `/api/v1`, with the OpenAPI document at `/api/v1/openapi.json`.

- `projects` — CRUD, tree, access rules
- `jobs` — CRUD, versions, `versions/{v}/graph`, validate (dry-run validation of a spec; also returns its graph), runs
- `runs` — list, get (with step tree), `graph` (graph plus current step states, `{graph, steps[]}`), cancel, rerun (same version and options), SSE `/runs/{id}/events`
- `executions` — get, logs (SSE)
- `schedules`, `webhooks`
- `nodes` — list (`GET /nodes`), get, create (`POST`, `source=manual`), update (`PATCH`, optional `if_version`), delete; `?force=true` is needed to write a node of another source (it keeps the source). All nodes routes need the admin token (§12.2), reads included. The sync routes (`PUT /inventory/sources/{source}/nodes` and `:batch`) are in §11; they need the admin token too.
- `runs/{id}/targets` — admin token; the nodes frozen at trigger, cursor-paged (`limit` default 50, max 200), never credential or host key
- `agents` — list, get (telemetry), drain; `pools` and pool tokens
- `secrets` — metadata list (`GET /secrets`), create or add a version (`PUT /secrets/{scope}/{name}`, write-only values, optional `if_version`), delete; `scope` is one percent-encoded path segment. `GET /secret-types` is public. All other secrets routes need the admin token (§12.2) and answer 503 when no KEK is configured.
- `plugins` — manifests with input and output schemas (drives the UI forms)
- `service-accounts`, `tokens`
- `audit`
- `apply`

**Errors** use RFC 9457 problem+json. **Pagination** is cursor-based.

**Status spelling.** The store, the API and the agent protocol use `canceled`; the engine uses `cancelled` (§5.3) and calls a not-yet-started run `queued` (stored as `pending`). The mapping lives in one place, `internal/engine/status_map.go`.

---

## 15. Frontend

- **SvelteKit SPA** with `adapter-static`, built into `web/build` and embedded via `go:embed`. Served by every controller at `/`.
- **Pages:**
  - project tree
  - job list and detail
  - job editor: CodeMirror JSON, validated live against the job spec schema and plugin schemas from `/api/v1/plugins`
  - run dialog: an `@sjsf/form` generated from `spec.options`
  - run view: step tree with statuses, lanes, attempts, outputs, and a virtualized log viewer with ANSI colors, live via SSE
  - agents: pools, telemetry, drain
  - nodes: filter by CEL selector
  - secrets: metadata only, write-only values
  - schedules and webhooks
  - access rules
  - audit
- **Plugin UIs are generated from schemas.** No plugin ships custom frontend code in v1. A visual step-form editor (per-plugin `@sjsf/form` from the input schema) is a v1.1 nicety; the JSON editor is the v1 baseline.
- The API client is generated (`openapi-typescript`). Hand-written fetch code is not allowed.
- `@sjsf/form` has a single maintainer, so wrap it behind one internal `SchemaForm.svelte` component. That way it can be swapped out.

### 15.1 Job and run graph

An activity-diagram view (UML-like) of a job's structure. It appears in three places:
- **Job detail:** the structure of the current version.
- **Job editor:** a live preview beside the JSON editor.
- **Run view:** the same graph with live status.

**Visual vocabulary**

| Spec construct | Rendered as |
|---|---|
| Run start / end | Filled circle / ringed circle |
| Plugin step | Rounded box: step id, plugin icon and name, a one-line input summary (command, image, release…) |
| Sequential steps | Vertical flow with arrows |
| `when` other than the default | Decision diamond before the step (drawn from the `when` attribute and the `skip` edge), with the CEL condition as the edge label. The skip path bypasses the step. |
| `group` | Labeled container box |
| `catch` / `finally` | Columns inside the group box, joined by a red "on failure" edge and a grey "always" edge |
| `for_each` | Fork bar → stacked container labeled "× N lanes (max_parallel M)" → join bar. In the run view it shows lane counts by status ("N lanes: 50 failed, 200 succeeded") and a bounded table lists at most 200 instances. |
| `retry` | Badge on the step (`↻ 4`). In a run: current attempt (`2/4`) and a countdown to the next retry. |
| `continue_on_error` | Dashed border |

**Where the graph comes from**

- `internal/engine` exposes `BuildGraph(job) Graph` (ADR-0041): nodes and edges, pure and deterministic. The UI never derives control flow from the spec.
  - Node kinds: `start`, `end`, `step`, `group`, `catch`, `finally`; ids `$start` and `$end`. Edge kinds: `sequence`, `skip`, `catch`, `finally`.
  - Node ids are step paths. Children of a `for_each` group use the template id `<group>/for_each[*]/<id>` (`*` is never a lane key), so the graph does not grow with the lanes. `GraphNodeOf(path)` maps an instance path to `(node, lane)`; a lane row `g/for_each[k]` maps to node `g`.
  - `when` is a node attribute plus a `skip` edge that bypasses the step (label: the CEL condition). Catch and finally are container nodes `<g>/catch` and `<g>/finally`; the `catch` edge ("on failure") runs from the last body child, the `finally` edges ("always") from the catch container and the last body child.
  - Nodes carry no `with`: no step input, no secret surface.
- `GET …/versions/{v}/graph` returns it.
- `POST …/validate` returns it (`graph`) for valid unsaved specs, so the editor preview updates as you type (debounced).
- `GET /runs/{id}/graph` returns `{graph, steps[]}`: the static graph of the run's pinned version plus the current state of each step instance, keyed by `node` and `lane`.
- **Run view status overlay:** pending, queued, running (animated), succeeded, failed, skipped, retry-wait, cancelled, lost.
  - Updates arrive on the run's SSE stream as `step` events `{path, node, lane, status, attempt, …}`; the graph is fetched once.
- **Clicking a node** opens a side panel with:
  - resolved inputs (secret refs shown as refs)
  - outputs and error
  - attempts, with agent and timings
  - the log viewer for the selected attempt

**Rendering and layout**

- **Rendering:** `@xyflow/svelte` (Svelte Flow) with custom node components. It provides pan, zoom, minimap, and fit-to-view.
- **Layout:** the spec is structured (nested sequential lists, no arbitrary edges), so layout is a small recursive box-layout function, roughly 150 LOC:
  - each list stacks vertically
  - groups wrap their children
  - `catch` and `finally` are placed as columns to the right

  It's deterministic, needs no general graph layout library, and keeps positions stable between runs. ELK.js was not chosen because its EPL-2.0 license complicates GPL distribution, and dagre handles nested groups poorly.
- **Large fan-outs:** above 20 lanes, lanes collapse into a summary (counts by status), with a virtualized lane list on expand.

---

## 16. Deployment

- **One binary and one container image** (`damson`), plus two **agent images** built from the same binary:
  - `damson-agent` adds `ansible-core`, `git`, and `openssh-client`.
  - `damson-agent-build` adds `buildah`, `helm`, and `git`, for build pools.
  
  Agents detect the available binaries at startup and advertise them as capabilities. Custom plugin sets are built with `damson build --with …` on top of either image.
- **Kubernetes:** a Helm chart.
  - Controller Deployment (replicas ≥ 2, PodDisruptionBudget, readiness on database plus the migrations check).
  - Agent Deployment(s) per pool (HPA-friendly; `terminationGracePeriodSeconds` covers the drain timeout).
  - Secrets for the KEK, the pool tokens, and the cluster secret.
- **VMs:** systemd units for `damson controller` and `damson agent`. The binary has no runtime dependencies (the Ansible plugin needs ansible on that agent host).
- **Mixed:** VM agents connect to the k8s ingress `wss://`. k8s agents connect to a VM controller. Both are fine.
- **Postgres connections:** the pool sets `idle_in_transaction_session_timeout` to 30s (overridable in the DSN), so a controller host that dies mid-transaction cannot hold row locks until TCP keepalive notices. No transaction waits on anything but the database. Servers should also set `tcp_keepalives_*`.
- **Migrations:** `damson migrate up` runs explicitly, or controllers run it at start behind a database advisory lock (Postgres) with `--auto-migrate`.
- **Air-gapped:** no runtime internet access. Offline bundle: images + Helm chart + binaries + checksums.
- **Dev:** `damson dev` runs a controller and an agent in one process with SQLite, the fs log store, and the db secret backend. Docker Compose provides Postgres, an S3-compatible server, and sshd and FTP test targets.

---

## 17. Observability

- **OTel traces:** Run (root span), then StepInstance, then Execution.
  - The trace context is passed in `step.execute`, so agent spans for plugin work join the trace.
- **Metrics:** `damson_runs_total{status}`, `damson_step_duration_seconds{plugin,status}`, `damson_queue_depth{pool}`, `damson_agents_connected{pool}`, `damson_agent_slots_free{pool}`, `damson_log_bytes_total`, `damson_ws_messages_total{method}`, `damson_leases_lost_total`.
- **Endpoints:** `/healthz` (process), `/readyz` (DB and log store reachable, migrations current), `/metrics`.
- **Logs:** `slog` JSON with `run_id`, `execution_id`, `attempt_id`, `agent_id`, `request_id`.

---

## 18. Security summary

- TLS everywhere: `wss://` for agents, HTTPS for the UI and API. Internal controller endpoints (ADR-0040) are on a separate listener and use HMAC-SHA256 with a cluster secret (timestamp window 30s, bound to caller and target controller, read-only). They use plain HTTP, so log lines between controllers are unencrypted and the network between controllers must be private.
- Secrets are encrypted at rest (db backend) or kept in OpenBao. They are delivered just-in-time, never persisted on agents, and masked by the agent in logs, progress, errors and outputs before they leave it; the controller never holds values after dispatch (ADR-0047).
- SSH host keys are strict by default.
- All tokens (pool, service account, webhook, session) are random and stored hashed. For the interim admin token (§12.2) only the hash is stored, never the token.
- CEL evaluation has cost limits, and no I/O functions are exposed.
- Plugin inputs are validated against their schemas on the controller before dispatch.
- The audit log covers all mutations, triggers, and secret reads (metadata only).

---

## 19. Repository layout

```
cmd/damson/                 # main: calls pkg/damsoncli
pkg/damsoncli/              # public entry point of the CLI (also used by custom binaries from damson build)
internal/
  cli/                      # the damson command line: controller | agent | dev | admin | plugin | build | version
  buildcli/                 # damson build
  config/
  controller/               # wiring, HTTP server, dispatch loop, duties, internal endpoints
  agent/                    # connection mgmt, executor, batcher, spool, masker
  protocol/                 # JSON-RPC+MessagePack framing, message types, versioning
  engine/                   # pure Decide(), BuildGraph(), spec model, spec validation
  expr/                     # CEL environments, interpolation, cost limits
  store/                    # Store interface + postgres/ and sqlite/ implementations
    queries/{shared,postgres,sqlite}/
    migrations/{postgres,sqlite}/
  secrets/                  # SecretStore interface, db/, openbao/, masking
  logstore/                 # fs/, db/, s3/
  auth/                     # oidc, sessions, tokens, CEL policy
  inventory/
  scheduler/
  api/                      # Huma handlers, problem+json
  telemetry/
plugins/
  ssh/  winrm/  transfer/  ansible/  buildah/  helm/
  all/                      # registers the built-in plugins
  internal/remote/          # shared SSH/SFTP/SCP/FTP/S3 endpoint code
  internal/gitcheckout/     # shared git clone helper (ansible, buildah, helm)
pkg/pluginsdk/              # public SDK: Plugin, Manifest, Emitter, errors (minimal dependencies)
  plugintest/               # test harness for plugin authors
examples/plugins/hello/     # minimal out-of-tree plugin, built into a custom binary in CI
web/                        # SvelteKit app
deploy/helm/  deploy/compose/  deploy/systemd/
docs/                       # Sphinx documentation site (reST, uv project); ADRs in docs/architecture/decisions/
testdata/                   # spec fixtures, golden files
```

---

## 20. Testing strategy

- **Engine:** table-driven and golden tests over JSON spec plus event-sequence fixtures. Cover every rule in §5.3, including retries, lanes, catch/finally, and cancel.
- **Store:** one test suite, run against **both** SQLite and Postgres (testcontainers).
- **Protocol:** tests for the framing codec; property tests for fencing and lease logic.
- **Plugins:** integration tests against containers (`openssh-server`, an FTP server, an S3-compatible server, and an Ansible target over SSH). WinRM uses a unit-tested request builder plus a manual or nightly job against a Windows VM. Buildah and Helm run nightly against a local registry container and a `kind` cluster.
- **Plugin SDK:** every built-in plugin passes the `plugintest` contract suite (schema generation, cancellation, secret masking, output-schema conformance). CI builds `examples/plugins/hello` into a custom binary with `damson build --with` (offline, `make plugin-contract`).
- **Masking (ADR-0047):** agent wire-level tests over raw frames (including reconnect and spool resend) prove no secret value leaves the agent; end-to-end tests check every controller sink on both databases. An HA move reconnects the same agent process, so the reconnect and resend tests are the proof there.
- **Graph:** golden tests for `BuildGraph` over the same spec fixtures the engine uses, plus snapshot tests for the layout function.
- **HA chaos test (CI, from M3 on):** 3 controllers + Postgres + 5 agents. Run 200 jobs while randomly killing controllers and agents, then assert:
  - every run reaches a terminal state
  - no execution completes twice
  - logs are contiguous or show explicit gaps
  - **First slice (`make chaos`, `internal/chaos`, build tag `chaos`, Postgres only, CI job `chaos`):** two in-process controllers built by `internal/controller/node` on one database, agents behind a routable load balancer that can sever or reroute them. Scenarios: an agent moves to another controller mid-step (adopted, never lost, runs and completes once), and a result finished while disconnected is redelivered to another controller and applied once. Random kills of controllers and agents, the 3x5x200 soak and killing Postgres come later.
- **UI:** Playwright smoke tests for login (mock OIDC), triggering a run, and watching logs.

---

## 21. Milestones (vertical slices)

| # | Slice | Done when |
|---|---|---|
| M0 | Skeleton | Binary + config, both store backends + migrations, `/healthz`, CI (lint, test both DBs), embedded empty SPA, `pkg/pluginsdk` v0 (interface, manifest, errors, `plugintest`) |
| M1 | Agent link | WS + MessagePack JSON-RPC, pool tokens, hello/heartbeat/telemetry, reconnect; agents page |
| M2 | First run end to end | Job spec v1 (linear), `ssh` plugin built on the public SDK, plugin registry from agent manifests, dispatch with leases and fencing, live logs (db log store), run page, API trigger |
| M3 | Engine complete + HA | `when`, groups, try/catch/finally, retries/backoff, timeouts, cancel, `for_each` lanes; multi-controller on Postgres; reaper/leader leases; cross-controller tail; job and run graph (`BuildGraph` + Svelte Flow view with live status); **chaos test green** |
| M4 | Secrets + inventory | db secret backend, delivery, masking; inventory API including source sync; targets/selectors; host key policies; replaces the M2 dev-only `devnodes` sources (ADR-0034, superseded by ADR-0044: the dev file is a seeder) |
| M5 | Transfer + WinRM + plugin tooling | `transfer` (SFTP/SCP/FTP/FTPS/S3, copy/move, verify), `winrm`; `damson build --with`, `examples/plugins/hello`, `damson plugin new/run/validate` |
| M6 | Ansible, Buildah, Helm | Shared git checkout; Ansible inventory and callback events; Buildah build and push with digest output; Helm upgrade/rollback/status; `damson-agent` and `damson-agent-build` images; build → deploy example job |
| M7 | Triggers | Schedules, webhooks, service accounts and tokens, run dialog from options |
| M8 | AuthN/Z | OIDC, sessions, project tree, CEL access rules, audit log |
| M9 | Production readiness | S3 log store, OpenBao backend, `apply`/`export`, Helm chart, systemd units, offline bundle, docs |

---

## 22. Open decisions (track as ADRs)

Each item is tracked as a Proposed ADR in `docs/architecture/decisions/` (ADR-0022 to ADR-0031; item 1 → 0022, 2 → 0023, 3 → 0024, 4 → 0025, 5 → 0026, 6 → 0031, 7 → 0027, 8 → 0028, 9 → 0029, 10 → 0030). The runtime `.so` plugin constraints from §6 are ADR-0032.

1. **GPL-3.0 vs AGPL-3.0.** AGPL also covers modified versions offered as a hosted service.
2. **Namespace checks for the name:** GitHub org, Go module path, Docker Hub/GHCR, `damson.dev` domain, trademark search.
3. **YAML as an accepted input format** for `apply` (JSON stays canonical).
4. **Kerberos for WinRM** (post-v1).
5. **Parallel groups** (`type: parallel`) beyond node lanes.
6. **Manual approval steps** (gates) and **notifications** (email, Slack, Teams) as plugins.
7. **Per-project secret backends.**
8. **Directory and glob transfers.**
9. **Multi-arch image builds** (`buildah --platform` with qemu or native builders) and **image signing** (cosign).
10. **Helm diff preview** before upgrade (requires the helm-diff plugin in the build-agent image).

---

## 23. Rejected alternatives

| Alternative | Why it was rejected |
|---|---|
| **NATS / JetStream** as the agent transport and queue | It would replace roughly 300 lines of RPC framing and the cross-controller log-tail proxy. But it adds a second stateful component to deploy, secure, and upgrade, and JetStream HA is itself a Raft cluster. Postgres is still required for state, so it adds infrastructure without removing any. Revisit only if log fan-out across controllers becomes a measured bottleneck. |
| **Raft-based storage** (rqlite, dqlite, embedded Raft) | It makes controllers stateful, so they can't be bounced freely, and quorum loss stops all writes. |
| **Out-of-process plugins over gRPC (hashicorp/go-plugin)** | gRPC boilerplate in every plugin, for isolation and multi-language support that aren't needed. |
| **Executable plugins (JSON over stdin/stdout)** | Only valuable for plugins in other languages, which aren't needed. |
| **Actor framework** | Goroutine-per-execution plus a pure engine plus database CAS covers supervision. Durability comes from Postgres, not actors. |
| **Temporal as the engine** | It's a heavy dependency that makes self-hosting and air-gapped installs harder. |
