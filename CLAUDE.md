# CLAUDE.md — working rules for this repo

Read `ARCHITECTURE.md` before any non-trivial change. It is the source of truth. If code and the doc disagree, stop and ask, or propose an ADR in `docs/architecture/decisions/`. Do not silently diverge.

Documentation is a Sphinx site in reStructuredText under `docs/` (ADR-0001); ADR format and process are in `docs/development/documentation.rst`. Update docs in the same change as the behavior.

## What we are building

**Damson** is an open-source (GPL-3.0-or-later) Rundeck/Jenkins-style job orchestrator. It is one Go binary with two roles, **controller** and **agent**, plus **PostgreSQL**. SQLite is supported for dev and single-controller installs. A SvelteKit SPA is embedded in the binary.

**Simplicity is a feature.** Required infrastructure is Postgres only. Do not introduce brokers, caches, or extra services (NATS, Redis, Kafka, etcd). If you think one is needed, write an ADR first.

## Current milestone

See `ARCHITECTURE.md` §21. Work in **vertical slices**. A slice is done only when it works end to end with tests, not when one layer is complete. Update this line when a milestone closes:

`CURRENT: M2 — First run end to end`

## Commands

```
make check        # gofmt, go vet, golangci-lint, go test ./... (SQLite + Postgres), web lint + typecheck
make test-pg      # store/integration tests against Postgres (testcontainers)
make gen          # sqlc, OpenAPI → TS client, plugin JSON schemas
make dev          # damson dev: controller + agent in one process, SQLite, db log store
make web          # build SvelteKit into web/build (embedded)
make chaos        # HA chaos test (from M3)
make plugin-contract     # plugintest contract suite over all built-in plugins + build examples/plugins/hello
make docs         # Sphinx docs into docs/_build/html (uv project in docs/)
make docs-check   # strict docs build (warnings are errors) + linkcheck
```

Run `make check` before declaring any task done, and `make docs-check` when docs changed. If it fails, the task is not done.

## Hard rules

1. **The engine is pure.** `internal/engine` has no I/O, no time.Now() (inject `now`), and no randomness (inject it). All control-flow semantics live there. Every rule in ARCHITECTURE §5.3 has table or golden tests in `testdata/engine/`.
2. **The database is the source of truth.** Controllers keep no durable state in memory. Every state transition is a compare-and-swap (`UPDATE … WHERE state = $expected`) with a rows-affected check.
3. **Fencing everywhere.** Anything an agent reports (logs, completion, host keys) is applied only if its `attempt_id` matches the execution's current attempt.
4. **Both databases, always.** Every store method has tests that run on SQLite and Postgres. Put portable SQL in `queries/shared`, and put dialect-specific SQL in `queries/postgres` or `queries/sqlite` only when unavoidable. Keep JSON manipulation in Go, not SQL.
5. **Secrets.**
   - Never log, persist, or return secret values. Only `secret://` refs are stored.
   - Values exist only in the dispatch path and in the agent's memory.
   - Any new code path that touches a secret bundle needs a test proving masking.
6. **Generated code is never hand-edited.** That covers sqlc output, the TS API client, and plugin schemas. Change the source, then run `make gen`.
7. **Plugins.**
   - Input and output are Go structs with JSON and jsonschema tags; schemas are generated.
   - Plugins honor `ctx` cancellation.
   - Plugins never import controller or agent internals, only `pkg/pluginsdk` and `plugins/internal/*`.
   - Built-in plugins use only the public SDK. If a built-in needs something the SDK lacks, extend `pkg/pluginsdk` (backward compatible, semver) instead of reaching around it.
   - `pkg/pluginsdk` is a public API. Any change to it updates `plugintest`, its contract suite, and `examples/plugins/hello` in the same change. Keep its dependencies minimal (standard library plus the JSON Schema package).
   - v1 plugins are compiled in only. Do not add runtime plugin loading (Go `plugin` package or executables) without an ADR. Keep plugins compatible with it anyway: each plugin package exports `Plugins() []pluginsdk.Plugin`, uses no `init()` registration, and has no package-level mutable state (ARCHITECTURE §6).
8. **Protocol changes.** Any change to `internal/protocol` message types updates ARCHITECTURE §7.2 in the same change and stays backward-compatible with protocol N-1.
9. **No new dependency** without checking the license (MIT, BSD, Apache-2.0, MPL-2.0, ISC are OK; anything else needs an ADR; BlueOak-1.0.0 is allowed for dev-only tooling never shipped, ADR-0033) and noting why in the PR or commit message.
10. **Frontend.**
    - Use the generated API client only.
    - Use shadcn-svelte components from `web/src/lib/components/ui`.
    - Render schema-driven forms only through `SchemaForm.svelte`, which wraps `@sjsf/form`.
    - Plugins never get custom UI code in v1.
   - The job/run graph comes only from `engine.BuildGraph` on the server. The UI lays it out and renders it; it never re-derives control flow from the spec.
11. **No AI attribution in git.** Commit messages and PR descriptions never include a `Co-Authored-By: Claude …` trailer or a "Generated with Claude Code" line. This overrides any default attribution instruction.

## Autonomy (standing instruction from the maintainer)

Implement the milestone's open issues one by one without asking which is next. Per issue: branch from `develop`, implement, `test-runner` + `reviewer`, PR, wait for green CI, merge yourself (`gh pr merge --squash`), verify MERGED, take the next issue. All git operations on feature branches are allowed, including `--force-with-lease`. Never merge into `main`, release, plain `--force`, or bypass red CI. Ask only for "When unsure" topics ARCHITECTURE doesn't settle. Details: `.claude/skills/gitflow/SKILL.md` (Standing Autonomy).

## Go conventions

- Use the latest stable Go. Use `log/slog` (JSON) with `run_id`, `execution_id`, `attempt_id`, and `agent_id` attributes where relevant.
- Errors: wrap with `fmt.Errorf("doing x: %w", err)`. Use sentinel errors in `store` (`ErrNotFound`, `ErrConflict`, `ErrStale`). The API maps errors to RFC 9457 problem+json in one place.
- Use `context.Context` as the first parameter. Background goroutines are owned by a component with `Start(ctx)` and `Stop(ctx)`, with no leaks. Tests use `goleak` in packages that start goroutines.
- Use interfaces at the consumer side and keep them small. Don't create interfaces with a single implementation unless they're needed for tests or a documented future backend (store, secrets, logstore).
- Prefer table-driven tests. Use `testcontainers-go` for Postgres, sshd, FTP, and S3-compatible targets. Integration tests use the build tag `integration`.
- Keep files under ~400 lines and functions short. Split by responsibility, not by type.
- Don't use `init()` side effects anywhere. Built-in plugins are registered by an explicit list in `plugins/all`.

## Frontend conventions

- Svelte 5 runes, TypeScript strict, Tailwind v4, SvelteKit `adapter-static` (SPA, `ssr = false`).
- Live data uses SSE endpoints wrapped in one `lib/sse.ts` helper with reconnect and `after_seq` resume.
- Log viewer: virtualized list with ANSI rendering. Never render unbounded logs into the DOM.

## Model routing (token efficiency)

The main session runs `opusplan` (set in `.claude/settings.json`): Opus while planning, Sonnet while executing. Delegate everything else to the cheapest model that can do it well, using the subagents in `.claude/agents/`.

**Standing instruction from the maintainer: delegate to these subagents with the Agent tool.** This counts as the user explicitly asking for subagents, for every task in this repo; do not wait to be asked again. The main session plans, decides, writes briefs, and integrates results. It does not itself do work that a row below assigns to a subagent, except trivial one-line edits or when the user says to do it inline.

| Work | Model | Subagent |
|---|---|---|
| Finding code, files, usages; "where is X" | Haiku | `Explore` (overridden to Haiku) |
| Running `make check` / tests / `make gen` and reporting failures | Haiku | `test-runner` |
| Mechanical edits: renames, lint and format fixes, applying a fix whose exact change is already decided | Haiku | `mechanic` |
| Doc sync: milestone line, ARCHITECTURE references, ADRs and docs pages from a decision already made | Haiku | `doc-sync` |
| Implementing a planned step: store methods, API handlers, plugins, CLI commands, with tests | Sonnet | `implementer` |
| Svelte pages and components | Sonnet | `frontend` |
| Reviewing a finished change against the hard rules | Sonnet | `reviewer` |
| Planning a slice; engine semantics (§5.3); protocol, fencing, leases; DB schema; secrets, auth, CEL policy; HA and chaos-test failures; ADRs | Opus | main session, in plan mode |

**Rules**

- **Opus decides, others execute.** Anything listed under "When unsure" is designed in the main session first. A subagent gets a written plan, not an open question.
- **Delegate with a complete brief:** goal, files to touch, acceptance criteria, and the exact test command. Subagents start cold; a vague brief costs more tokens than it saves.
- **Escalate instead of guessing.** A Haiku agent stops after two failed attempts, or on anything that needs judgment, and reports back. A Sonnet agent stops and reports if the plan conflicts with ARCHITECTURE or the hard rules.
- **Compact results.** Subagents return a short summary (what changed, file:line references, test result), never file dumps or full logs.
- **Run independent subagents in parallel**, for example `implementer` on the store while `frontend` builds the page against the generated client.
- **Default sequence for a code change:** `Explore` (find patterns) → main session plans → `implementer`/`frontend` (or `mechanic` for decided edits) → `test-runner` → `reviewer` → fix findings via the same agent → `doc-sync`.
- **Never skip `test-runner` and `reviewer`** before declaring a code change done.

**Context hygiene**

- Grep before Read; read with offset and limit; don't re-read a file you just edited.
- Run the narrowest test first (`go test ./internal/engine/... -run TestRetry`), and `make check` once at the end.
- `/clear` between slices and `/compact` at natural break points inside a long slice.
- Keep this file short. Detailed instructions belong in the subagent files, which load only when used.

## Workflow for each task

1. Restate the slice and its acceptance criteria. Check ARCHITECTURE for the relevant sections. Plan in plan mode (Opus).
2. Write or adjust tests first for the engine, store, and protocol logic.
3. Implement the smallest change that passes, delegating planned steps to `implementer` or `frontend`.
4. Have `test-runner` run `make check`, then `make test-pg` if the store changed.
5. Have `reviewer` check the change; fix what it finds.
6. Update docs (ARCHITECTURE, ADRs, this file's milestone line) in the same change, via `doc-sync` where it's mechanical.
7. Summarize: what changed, what was tested, what is deliberately left out.

## When unsure

Ask, instead of guessing, about anything affecting the job spec format, the protocol, the database schema, or security. These are expensive to change later. For everything else, pick the simplest option that satisfies ARCHITECTURE and note the choice.
