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

Implement the milestone's open issues one by one without asking which is next. Per issue: branch from `develop`, implement, check (see Agent team), PR, wait for green CI, merge yourself (`gh pr merge --squash`), verify MERGED, take the next issue. All git operations on feature branches are allowed, including `--force-with-lease`. Never merge into `main`, release, plain `--force`, or bypass red CI. Ask only for "When unsure" topics ARCHITECTURE doesn't settle. Details: `.claude/skills/gitflow/SKILL.md` (Standing Autonomy).

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

## Agent team (token efficiency)

The main session runs `sonnet` (`.claude/settings.json`) and is the **code writer**: it reads the code it needs, writes code and tests, resolves rebase conflicts, and talks to three subagents in `.claude/agents/`. Each subagent starts cold, so brief it with goal, files, acceptance criteria, and the exact command; ask for a compact result.

| Agent | Model | Use it for |
|---|---|---|
| `architect` | Opus | Design decisions CLAUDE.md "When unsure" reserves (job spec, protocol, DB schema, security, engine semantics, fencing, leases, HA, ADRs) and review of a finished diff against the hard rules before merge. Read-only. |
| `toolcaller` | Haiku | `gh` issues, PRs, checks, merges; git fetch, diff, push, rebase attempts; `make check`, `make test-pg`, `make gen`, narrow tests; watching CI; diff digests; "where is X" lookups. Never edits files. |
| `docs-writer` | Sonnet | `docs/` (Sphinx reST), ADR files, ARCHITECTURE.md sections, the `CURRENT:` milestone line, from facts you state. Runs `make docs-check`. |

**Rules**

- **Write code yourself.** Do not spawn agents to implement; the main session already holds the context. Parallel code work is for independent slices only, in separate worktrees.
- **Keep noise out of the main context:** send long output (CI logs, test runs, large diffs) to `toolcaller` and read its summary. Grep before Read; read with offset and limit; don't re-read a file you just edited.
- **Ask `architect` before coding** anything in the "When unsure" list that ARCHITECTURE does not settle, and **before merging** any change that touches the protocol, schema, fencing, secrets, auth, or the engine. Routine changes (plugins, UI, CLI, small store methods) are reviewed by yourself with `git diff` plus green CI.
- **Escalate instead of guessing.** `toolcaller` stops after two failed attempts at a step and reports; `architect` says when the maintainer must decide.
- **Run independent agents in parallel** (for example `toolcaller` watching CI while `docs-writer` writes the page).
- Run the narrowest test first (`go test ./internal/engine/... -run TestRetry`); `make check` once at the end. `/clear` between slices.
- Keep this file short. Detailed instructions belong in the agent files, which load only when used.

## Workflow for each task

1. Restate the slice and its acceptance criteria. Read the relevant ARCHITECTURE sections. For a "When unsure" topic ARCHITECTURE does not settle, get a decision from `architect` first.
2. Write or adjust tests first for the engine, store, and protocol logic.
3. Implement the smallest change that passes.
4. Have `toolcaller` run `make check` (and `make test-pg` if the store changed) and report failures.
5. Review your own diff against the hard rules; for risky areas (see Agent team) have `architect` review it, and fix what it finds.
6. Have `docs-writer` update docs (ARCHITECTURE, ADRs, the milestone line) in the same change.
7. Open the PR; have `toolcaller` watch CI and, once green, merge it (autonomy rules above).
8. Summarize: what changed, what was tested, what is deliberately left out.

## When unsure

Ask, instead of guessing, about anything affecting the job spec format, the protocol, the database schema, or security. These are expensive to change later. For everything else, pick the simplest option that satisfies ARCHITECTURE and note the choice.
