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
| Log storage | `fs` / `db` / `s3` backends (`aws-sdk-go-v2` S3 client, S3-compatible endpoints) | |
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
| **StepInstance** | One logical step in a run. Fan-out and `for_each` create one instance per node. It has a stable `path`, such as `main/for_each[web-01]/install`. |
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
| `type: "group"` | A container with `steps`, optional `catch`, `finally`, `rethrow`, `for_each`, `max_parallel`, `fail_threshold` |
| `when` | A CEL condition. The default is `success()` (all previous steps in scope succeeded or were handled). If the expression calls any status function (`success()`, `failure()`, `always()`, `cancelled()`), the implicit `success()` is off; otherwise it is evaluated as `success() && (when)`. |
| `continue_on_error` | A failure is recorded but does not fail the enclosing scope. |
| `timeout` | Per attempt. The job-level `timeout` covers the whole run. |
| `retry` | `max_attempts`, `backoff{initial, multiplier, max, jitter}`, `retry_on` (CEL over `error`), `on_lost` (bool) |
| `runs_on` | Optional agent selection: `{ "pool": "dc1", "labels": {...} }`. The default is the pool of the target node, or any pool. |

### 5.3 Control-flow semantics

The engine evaluates these rules deterministically:

1. Steps in a list run **sequentially**.
2. Before a step runs, its `when` is evaluated. If false, the step is `skipped`.
3. If a step fails and has no `continue_on_error`, the rest of that list is skipped, except for steps whose `when` explicitly allows it by calling a status function (`failure()`, `always()` or `cancelled()`). Any status function in `when` turns off the implicit `success() &&` that is otherwise prepended.
4. **Group with `catch`.** If any step in `steps` fails, `catch` runs with `error` bound to the failing step's error.
   - If `catch` succeeds and `rethrow` is false, the group is `succeeded (handled)`.
   - If `rethrow` is true, the group fails after `catch` and `finally`.
5. **`finally`** always runs, whether the group succeeded, failed, or was cancelled (with a short grace period on cancel). A failure in `finally` fails the group.
6. **`for_each: "targets"`** (or any CEL list expression) runs the group body once per item, in independent *lanes*, with `node` bound.
   - Inside a lane, `steps.<id>` refers to that lane's steps.
   - `max_parallel` limits concurrent lanes.
   - `fail_threshold` is the number of failed lanes after which pending lanes are cancelled.
7. **Retries.** A failed attempt whose error matches `retry_on` (default: `connection`, `timeout`, `lost`) and has attempts left gets a new Execution scheduled at `now + backoff(attempt)`. `lost` is retried only if the plugin declares itself `idempotent`, or the step sets `retry.on_lost: true`.
8. **Status functions** available in `when`:
   - `success()`: everything previous in scope succeeded or was handled.
   - `failure()`: something previous in scope failed.
   - `always()`: always true.
   - `cancelled()`: the run has been cancelled.

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
  | `node` | Inside `for_each` over targets: `name`, `address`, `tags`, `attributes` |
  | `targets` | The resolved node list |
  | `steps.<id>` | `status`, `outputs`, `error`, `started_at`, `finished_at`; lane-local inside `for_each` |
  | `steps.<id>.lanes[<key>]` | Access to a specific lane's results from outside the group |
  | `error` | Inside `catch`: `step`, `kind`, `message`, `exit_code` |

- **Secrets are never values in the CEL context.** They are referenced only as `secret://...` strings in plugin fields whose schema marks them `x-damson-secret: true`. They are resolved at dispatch time (§9).
- **Run options** marked `x-damson-secret: true` in `spec.options` accept only `secret://` references (anything else is rejected at trigger time, and such an option may not default to a plaintext value). Options that are not marked are stored and returned verbatim, so authors must mark credentials.
- CEL programs run with a cost limit and a size limit on results.
- **Outputs** are JSON objects, validated against the plugin's output schema, with a maximum of 256 KiB per execution. Larger data belongs in files or object storage.

### 5.5 Targets and node selection

- `spec.targets.selector` is a CEL expression over `node` (name, tags, attributes, pool, os, transport).
  - It is evaluated against the inventory intersected with the project's node scope (§12).
  - It is evaluated once, at run start. The node list is frozen into the run.
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
- Out-of-tree plugins are added with `damson build --with <module>@<version>`. It generates a small `main` package that lists the extra modules, then builds a custom binary, the way `xcaddy` works for Caddy.

**Plugin registry**

- Agents send the full manifests of all plugins compiled into their binary in `agent.hello`.
- The controller keeps them in an in-memory registry derived from the live agent connections (`internal/pluginreg`; added on hello, dropped on disconnect, deduped by name+version+checksum). A persisted `plugin_manifests(name, version, checksum, manifest)` table is deferred to M3 (HA; issue #63). Until it lands, each controller sees only the manifests of the agents connected to it, and after a controller restart plugins that exist only in custom agent binaries are unknown until their agents reconnect. The controller also links the built-in plugins itself, so their schemas are known even when no agent is connected. Plugins that exist only in custom agent binaries become known when such an agent connects.
- **Version pinning:** steps may pin a major version (`"plugin": "helm@1"`). Unpinned means the highest version available on eligible agents.
- **Dispatch** only goes to agents that report the plugin at a compatible version and satisfy its `Requires` capabilities.
- **Schema conflicts:** if agents report different schemas for the same name and version (checksum mismatch), the controller's own manifest wins, otherwise the most reported one; the controller flags the minority in the API and refuses dispatch to it until fixed.
- **Built-ins cannot be shadowed by agents.** An unpinned reference to a name the controller links itself resolves only to a built-in version; an agent-only version of a built-in name (for example a higher `ssh` version) is reachable only with an explicit pin (`ssh@2`). An agent reporting a manifest for a built-in name that differs from the built-in, or a higher version, is logged as a warning (names, versions, checksums only).
- **Validation.** `Lookup` is for authoring time and may name a version no agent can run; the dispatch path validates the resolved input against the manifest returned by `Resolve`. Manifest schemas (size and nesting) and step inputs (size) are bounded, and validation errors never contain input values.

**Developer tooling**

- `damson plugin new <name>` scaffolds the package, input and output structs, a test, and a README.
- `pkg/pluginsdk/plugintest` runs a plugin's `Execute` in a unit test with a fake emitter, fake secrets, and a temp WorkDir, then asserts outputs and masked logs. It also provides a **contract suite** every plugin must pass: schema generation, cancellation (including an optional blocking case), secret masking (plaintext check), and output-schema conformance (validated with `santhosh-tekuri/jsonschema/v6`, a dependency of `plugintest` only).
- `damson plugin run <name> --input input.json --secrets secrets.json` runs one plugin locally without a controller.
- `damson plugin validate` checks the manifest, the schemas, and semver.
- `examples/plugins/hello` is a minimal out-of-tree plugin. CI builds it into a custom binary with `damson build`.

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

const Version = "0.2.0"

type TargetMode string // wire values: "none" | "single_node" | "inventory"; Valid() bool

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
    Port          int            `json:"port,omitempty"`
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
- **Host keys:** the node's `host_key_policy` is one of `strict` (default; requires `host_key` in the inventory), `tofu` (the agent reports the observed key, and the controller stores it on the node), or `insecure` (only allowed when explicitly enabled in controller config).
- **Cancel:** send a signal via the SSH session, then close the session.
- **Success:** a non-zero exit code is an `exit_code` error. `success_exit_codes` can override that.

### 6.2 `winrm`

- **Input:** `node`, `command` *or* `powershell` (sent as an encoded command), `env`, `credential`, `transport` (`https` default, `http` only when explicitly allowed), `auth` (`basic` | `ntlm`), `insecure_skip_verify` (default false).
- **Output:** the same as `ssh`.
- Kerberos is deferred to after v1.

### 6.3 `transfer`

One plugin covers server-to-server copies *and* S3 in both directions.

- **Input:**
  - `mode` is `copy` or `move`.
  - `source` and `destination` each take **one** of:
    - `node: {name, protocol: sftp|scp|ftp|ftps, path}`
    - `s3: {connection, bucket, key}`, where `connection` is a secret ref holding endpoint, region, access key, secret key, and path-style flag
  - `verify` is `none`, `size`, or `sha256` (default `size`).
  - `overwrite` (bool).
  - `mkdirs` (bool).
- **Behavior:**
  - Streaming: `io.Pipe` from the source reader to the destination writer, with the SHA-256 computed in-stream. **No temporary files.**
  - Atomic write: upload to `<path>.damson-partial`, then rename. S3 PUT is already atomic. Multipart is used for objects larger than 64 MiB.
  - If `path` ends with `/`, the source file name is appended.
  - `move` = copy, then verify, then delete the source. The source is deleted only after verification passes.
- **Output:** `destination_path` (or `destination_key`), `bytes`, `sha256`, `etag` and `version_id` (S3).
- **v1 scope:** a single file per step. Globs and directories come in v1.1.
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
| `step.complete` params | `attempt_id`, `status` (`succeeded`, `failed`, `canceled`, `timed_out`), `outputs`, `error` (`code`, `message`, `retryable`), `started_at_ns`, `finished_at_ns` |
| `step.complete` result | `acknowledged`, `stale` (result discarded by fencing; the agent stops retrying either way) |
| `step.cancel` params / result | `attempt_id`, `grace_ms` / `known` |
| `agent.drain` | `reason` |
| `agent.reconnect` | `jitter_ms`, `reason` |
| `node.hostkey_observed` | `attempt_id`, `node`, `key_type`, `host_key`, `fingerprint` |

The `secrets` member is the only place secret values cross the wire. The Go type redacts itself in `fmt`, `slog` and `encoding/json` output.

### 7.3 Reliability rules

- **Fencing.** Every Execution has a unique `attempt_id` (UUIDv7). The controller applies `step.complete`, logs, and leases only if the `attempt_id` matches the Execution's current attempt and the execution is not terminal. Stale results are acknowledged and discarded.
- **Leases.** Each running Execution has a `lease_expires_at` of now + 60s.
  - Heartbeats renew leases for every attempt_id they list. Renewal is batched, with one UPDATE per controller every 10s.
  - The **reaper** marks expired leases as `lost`.
- **Reconnect.** The agent reconnects with exponential backoff (1s → 30s, full jitter) and keeps executing during the gap.
  - On reconnect, `agent.hello` lists the running and finished-but-unacked attempts.
  - The controller takes ownership (`controller_id`), renews leases, and replies continue or cancel for each.
  - Ownership is ordered by `session_seq` (a per-process connection counter sent in `agent.hello`), not wall clocks: only a hello with a strictly higher `session_seq` takes over the agent row.
- **Log buffering.** The agent keeps unacked log batches in memory, bounded at 32 MiB, then spills to a bounded local spool file (256 MiB default).
  - If the spool overflows, the oldest unacked batches are dropped. The hub replaces them in its queue by one placeholder that is sent as `log.append` with `seq_from` = first dropped seq, `gap_to` = last dropped seq minus one, and a `System` log line carrying the last dropped seq, so no seq is lost and acks stay contiguous. The placeholder lives in the hub, so it is delivered even after the attempt's pipe has closed.
  - The spool file is unlinked right after creation (stale `damson-spool-*` files from a crashed agent are swept at start) and holds masked job output in plaintext, mode 0600.
  - The spool is best-effort: an agent crash loses unacked logs. That is an accepted trade-off of stateless agents.
- **Capacity.** The agent advertises `max_concurrent` (default: CPU count × 4). The controller never dispatches beyond the free slots reported.
- **Execution on the agent.** Pinned behavior of the agent executor (`internal/agent`); no message changes.
  - `step.execute` is accepted only for a built-in plugin with exactly the requested name and version and a free slot; otherwise it is rejected with a reason. A repeated execute for a known attempt is accepted without running twice.
  - A `step.cancel` for an attempt the agent does not hold leaves a tombstone (15 minutes, bounded), so a `step.execute` that overtakes or follows it is rejected. A cancelled plugin has `grace_ms` (default 5s) to return; after that the step is reported `canceled` and the plugin keeps its slot until it returns.
  - Log `seq` starts at 1 per attempt and increases by one per entry. Unacknowledged batches are resent in order after a reconnect, so ingest must be idempotent.
  - A finished result stays in memory and in the `agent.hello` `executions` list until `step.complete` is acknowledged or stale. A reconciliation `cancel` for a finished attempt discards it. The attempt's logs are dropped after the acknowledgement only when none is unsent. Delivery failures of one result (error answer, acknowledgement with neither `acknowledged` nor `stale`) back off per result (exponential, jittered, capped at 30s) without blocking other results; only transport errors end a delivery round.
  - Acknowledged and discarded attempts are remembered with the cancel tombstones (15 minutes, bounded), so a late or retried `step.execute` for them is rejected as `already completed`.
  - Memory bounds: outputs of one step are limited to 1 MiB (larger fails the step), error messages to 8 KiB, so `step.complete` always fits `MaxFrameSize`; at most 1024 unacknowledged results are kept, for at most 6 hours, oldest dropped first.
  - Secret values live only in the executor's memory; masking covers log lines, progress, errors, string outputs, host key reports and the plugin logger (§9.4). Overlapping secrets are masked as the union of their matches. Values shorter than 4 bytes are not masked in text (they would destroy output); the step log gets one `System` line saying so, never the value. A secret split across two `Log` calls is not masked: plugins must log whole lines.
- **Protocol versioning.** `agent.hello` carries `protocol: 1`. The controller supports N and N-1.

---

## 8. Orchestration and HA

### 8.1 Principles

- **The database is the only source of truth.** Controllers are interchangeable and hold only caches and live sockets.
- **No consensus inside Damson.** Coordination uses:
  - row-level compare-and-swap (`UPDATE … WHERE state = $expected` + rows-affected check)
  - `SELECT … FOR UPDATE SKIP LOCKED` (Postgres)
  - one **leader lease** row per singleton duty
- **SQLite mode runs exactly one controller.** This is enforced with a lock row at startup.

### 8.2 The engine is a pure function

```
Decide(spec JobVersionSpec, state RunState, now time.Time) -> []Action
Action = CreateStepInstance | ScheduleExecution | SkipStep | CompleteGroup | CompleteRun | CancelExecution
```

- Implemented as `(*engine.Engine).Decide(job, state, now) ([]Action, error)`; the `Engine` only holds the compiled-expression environment. The sequential subset (M2) additionally emits `StartRun` and `FailStep` (a step whose `when` or `with` expression failed to evaluate); `SkipStep` also creates the instance, with status `skipped` or `cancelled`.
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
   - River uniqueness (one available or running `run.advance` per run) avoids concurrent advancement. The version check makes it safe anyway.
3. **Dispatch** (every controller, for its own sockets):
   - A loop wakes on a notify hint or every 1s.
   - Claims queued, due Executions that match the pool, labels, and capabilities of its connected agents with free slots:
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
   - If the agent rejects or the send fails, the Execution is CAS'd back to `queued`.
4. **Running:** logs flow (§10), and heartbeats renew leases.
5. **Complete:** `step.complete`, then a CAS on `attempt_id` to set the terminal state and store outputs and error, then enqueue `run.advance`. The agent gets its ack after the commit.
6. **Retry:** the engine schedules a new Execution with `scheduled_at = now + backoff` and a new attempt_id.
7. **Cancel:**
   - Set `runs.cancel_requested_at`.
   - `run.advance` marks pending steps as cancelled.
   - For running Executions, the owning controller sends `step.cancel`. The hint is delivered via `LISTEN/NOTIFY` (Postgres) plus a 2s poll fallback.
   - `finally` blocks run after cancellation, with a configured grace period.

### 8.4 Singleton duties (leader lease)

- `leader_leases(name, holder, expires_at)`. Acquire or renew with a CAS every 5s, with a TTL of 15s.
- Duties:
  - **scheduler** (§12.3)
  - **reaper** (expired leases → `lost`; dead `controller_instances` cleanup)
  - **retention** (old runs, logs, audit; per-project policy)
- Each duty is idempotent, so a brief double-leader overlap during failover is harmless.

### 8.5 Controller registry and cross-controller routing

- `controller_instances(id, internal_url, started_at, last_seen)`, heartbeated every 5s.
- **Log tail:** a UI request to controller X for an execution owned by controller Y is proxied to `Y/internal/v1/executions/{id}/tail`.
  - Internal endpoints are authenticated with a cluster shared secret (HMAC) and should be on an internal listener or port.
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

- Format: `secret://<project-path>/<name>[#<key>]`, for example `secret://infra/billing/deploy-key#private_key`.
- A job may reference secrets in its own project or any **ancestor** project.
- Inventory-level credentials live under the reserved `secret://_inventory/...` scope. They are usable by any job whose project node scope includes the node.
- Types: `ssh_key` (private_key, passphrase, username), `password` (username, password), `token`, `s3` (endpoint, region, access_key, secret_key, path_style), `registry` (server, username, password), `kubeconfig` (kubeconfig, context), `generic` (arbitrary key/value).

### 9.2 Backends

```go
type SecretStore interface {
    Get(ctx context.Context, ref Ref) (Secret, error)
    Put(ctx context.Context, ref Ref, s Secret) error   // may return ErrReadOnly
    Delete(ctx context.Context, ref Ref) error
    List(ctx context.Context, projectPath string) ([]Metadata, error) // never values
}
```

- **`db` (default).** AES-256-GCM envelope encryption.
  - Each secret version gets a random DEK, which is wrapped by the KEK.
  - The KEK comes from a file or env var (`DAMSON_KEK`, 32 bytes base64). `kek_id` is stored per row.
  - `damson admin rotate-kek` re-wraps the DEKs.
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

### 9.4 Masking

- The agent builds a masker from all secret field values in the bundle, including base64 and URL-encoded variants and each line of multi-line values longer than 4 characters.
- It masks every log line before sending, and scans outputs. Any output value containing a secret is replaced with `***` and a warning is logged.
- The controller re-applies masking defensively on ingest. The bundle is available to it during dispatch; it keeps only hashes of the values for the duration of the run.

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
- **Object key:** `logs/<execution-id>/<seq_from>-<seq_to>.ndjson[.gz]` (the execution id is globally unique, so the project and run ids are not part of the key). Each line is `{"t":<unix ns>, "s":"out|err|sys", "l":"..."}`; the sequence number is implicit (line i of a chunk has `seq_from + i`), so a chunk holds a contiguous run and a declared gap splits a flush into two chunks.
- **UI read path:** `GET /api/v1/executions/{id}/logs?after_seq=N` via SSE.
  - Serves stored chunks first, then switches to live tail from the owning controller's ring buffer (proxied if needed).
  - Gaps are reported explicitly.
- **Retention** is per project (default 30 days), enforced by the retention duty plus optional S3 lifecycle rules.

---

## 11. Inventory API

Inventory is maintained by external systems (for example a ServiceNow CMDB sync running elsewhere). Damson only exposes an API.

**Node fields:** `name` (unique), `address`, `port`, `transport` (`ssh` | `winrm`), `os` (`linux` | `windows` | …), `pool`, `tags` (string list), `attributes` (string → string/number/bool), `credential` (secret ref), `host_key`, `host_key_policy`, `source` (read-only), `external_id`, `enabled`.

**Endpoints:**

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/v1/nodes?selector=<CEL>&source=…` | List and filter |
| `GET` | `/api/v1/nodes/{name}` | Get one |
| `POST` | `/api/v1/nodes` | Create (`source=manual`) |
| `PATCH` | `/api/v1/nodes/{name}` | Update fields |
| `DELETE` | `/api/v1/nodes/{name}` | Delete |
| `PUT` | `/api/v1/inventory/sources/{source}/nodes?dry_run=bool&prune=bool` | **Full-state sync** for one source: upsert all nodes in the body (matched by `external_id`, falling back to `name`). With `prune`, it deletes nodes of that source missing from the body. Returns a diff `{created, updated, deleted, unchanged}`. Idempotent. |
| `POST` | `/api/v1/inventory/sources/{source}/nodes:batch` | Incremental upserts and deletes |

**Rules**

- A node belongs to exactly one source. Writes from another source fail with a conflict error unless `force=true`, which requires admin.
- Sync calls use **service account tokens** with the `inventory:write` scope.
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
- `runs` — list, get (with step tree), `graph` (graph plus current statuses), cancel, rerun (same version and options), SSE `/runs/{id}/events`
- `executions` — get, logs (SSE)
- `schedules`, `webhooks`
- `nodes`, `inventory/sources/*`
- `agents` — list, get (telemetry), drain; `pools` and pool tokens
- `secrets` — metadata list, create or update (write-only values), delete
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
| `when` other than the default | Decision diamond before the step, with the CEL condition as the edge label. The skip path bypasses the step. |
| `group` | Labeled container box |
| `catch` / `finally` | Columns inside the group box, joined by a red "on failure" edge and a grey "always" edge |
| `for_each` | Fork bar → stacked container labeled "× N lanes (max_parallel M)" → join bar. Expandable to per-lane rows in the run view. |
| `retry` | Badge on the step (`↻ 4`). In a run: current attempt (`2/4`) and a countdown to the next retry. |
| `continue_on_error` | Dashed border |

**Where the graph comes from**

- `internal/engine` exposes `BuildGraph(spec) Graph`: nodes and edges whose **ids are step paths**, the same paths StepInstances use.
  - The control-flow semantics stay in one place. The UI never re-implements them.
- `GET …/versions/{v}/graph` returns it.
- `POST …/validate` returns it for unsaved specs, so the editor preview updates as you type (debounced).
- **Run view status overlay:** pending, queued, running (animated), succeeded, failed, skipped, retry-wait, cancelled, lost.
  - Updates arrive on the run's SSE stream as `{path, lane, status, attempt}` events.
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

- TLS everywhere: `wss://` for agents, HTTPS for the UI and API. Internal controller endpoints use HMAC with a cluster secret.
- Secrets are encrypted at rest (db backend) or kept in OpenBao. They are delivered just-in-time, never persisted on agents, and masked in logs and outputs.
- SSH host keys are strict by default.
- All tokens (pool, service account, webhook, session) are random and stored hashed.
- CEL evaluation has cost limits, and no I/O functions are exposed.
- Plugin inputs are validated against their schemas on the controller before dispatch.
- The audit log covers all mutations, triggers, and secret reads (metadata only).

---

## 19. Repository layout

```
cmd/damson/                 # main: controller | agent | dev | migrate | apply | export | admin
internal/
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
- **Plugin SDK:** every built-in plugin passes the `plugintest` contract suite (schema generation, cancellation, secret masking, output-schema conformance). CI builds `examples/plugins/hello` into a custom binary with `damson build`.
- **Graph:** golden tests for `BuildGraph` over the same spec fixtures the engine uses, plus snapshot tests for the layout function.
- **HA chaos test (CI, from M3 on):** 3 controllers + Postgres + 5 agents. Run 200 jobs while randomly killing controllers and agents, then assert:
  - every run reaches a terminal state
  - no execution completes twice
  - logs are contiguous or show explicit gaps
- **UI:** Playwright smoke tests for login (mock OIDC), triggering a run, and watching logs.

---

## 21. Milestones (vertical slices)

| # | Slice | Done when |
|---|---|---|
| M0 | Skeleton | Binary + config, both store backends + migrations, `/healthz`, CI (lint, test both DBs), embedded empty SPA, `pkg/pluginsdk` v0 (interface, manifest, errors, `plugintest`) |
| M1 | Agent link | WS + MessagePack JSON-RPC, pool tokens, hello/heartbeat/telemetry, reconnect; agents page |
| M2 | First run end to end | Job spec v1 (linear), `ssh` plugin built on the public SDK, plugin registry from agent manifests, dispatch with leases and fencing, live logs (db log store), run page, API trigger |
| M3 | Engine complete + HA | `when`, groups, try/catch/finally, retries/backoff, timeouts, cancel, `for_each` lanes; multi-controller on Postgres; reaper/leader leases; cross-controller tail; job and run graph (`BuildGraph` + Svelte Flow view with live status); **chaos test green** |
| M4 | Secrets + inventory | db secret backend, delivery, masking; inventory API including source sync; targets/selectors; host key policies |
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
