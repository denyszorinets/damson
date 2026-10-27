---
name: frontend-debugging
description: >
  Debug and verify the web UI with Playwright. Use when changing anything
  under web/, when a UI bug is reported, when you need to see what the UI
  actually renders, inspect console errors or network calls, or when adding
  or fixing end-to-end tests. Covers the Playwright MCP browser tools and the
  Playwright test suite in web/e2e.
---

# Frontend Debugging with Playwright

Never claim a UI change works without seeing it in a browser. Use the
Playwright MCP tools to look and interact, then capture the verified behavior
as a Playwright test in `web/e2e/` so it stays verified.

## Environment facts

- Browser: Google Chrome at `/opt/google/chrome/chrome`, installed by the
  devcontainer `Containerfile`. Playwright's own installer does not support
  Fedora, so do not run `npx playwright install`.
- The devcontainer runs as root, so Chrome needs `--no-sandbox`. `.mcp.json`
  configures the MCP server with it. `web/playwright.config.ts` should add it
  when running as root.
- If the MCP browser fails with "Running as root without --no-sandbox", the
  MCP server was started with an old config. Ask the human to reconnect it
  (`/mcp`), and use the test suite in the meantime.
- MCP artifacts (screenshots, snapshots, console logs) go to `.playwright-mcp/`,
  which is git-ignored.

## Start the app

Run the backend and the Vite dev server in the background. If the UI needs a
real login, also start the identity provider (see the keycloak skill):

```bash
nohup scripts/dev-keycloak.sh > /tmp/keycloak.log 2>&1 &   # only for real logins
<start the backend in dev mode, pointed at the dev issuer> &
cd web && bun run dev --host localhost --port 5173 &
```

Open **http://localhost:5173**, not 127.0.0.1, because the OIDC redirect URI is
registered for localhost. Configure Vite (`web/vite.config.ts`) to proxy `/api`,
WebSocket and SSE endpoints and `/healthz` to the backend. Sign in as one of
the development users.

## Look at the UI (Playwright MCP)

1. `browser_navigate` to `http://localhost:5173/<route>`.
2. `browser_snapshot` returns the accessibility tree. Prefer it over
   screenshots for finding elements and reading text.
3. Interact with `browser_click`, `browser_type`, `browser_fill_form` and
   `browser_press_key`, using refs from the snapshot.
4. Run `browser_console_messages` (level `error`) after every navigation and
   interaction. A clean console is part of "works".
5. Use `browser_network_requests` to check API calls, status codes and
   payloads.
6. Take a `browser_take_screenshot` only for visual or layout questions (theme,
   spacing, overflow). When styling changes, check both light and dark
   (`browser_emulate_media`).
7. `browser_resize` to 390×844 to check the phone layout.

## Debugging loop

1. Reproduce in the browser via MCP and note the exact steps.
2. Read console errors and failing network requests first; most UI bugs show
   up there.
3. Write a failing Playwright test that reproduces the bug (`web/e2e/`).
4. Fix it, then re-run the test and re-check in the browser.

## Live tests when the MCP browser is unavailable

Keep Playwright tests that drive the real dev stack, including the real login
form, in a separate suite such as `web/e2e-live/`. Use them to verify behavior
in a real browser when the MCP server cannot start Chrome. They collect console
errors, and you can inspect the `page.screenshot()` output in
`web/test-results/` with the Read tool. Seed data through the API first, using
a development token and curl.

## End-to-end tests

```bash
cd web
bun run test:e2e                      # build, serve, run tests
bun run test:e2e -- --debug           # step through (needs a display)
bun run test:e2e -- e2e/home.test.ts  # single file
```

Conventions:

- Tests live in `web/e2e/*.test.ts`. Unit tests (Vitest) stay next to the code
  in `src/**/*.spec.ts`.
- Stub backend calls with `page.route()` unless the test is explicitly an
  integration test against a running backend. Tests must not depend on
  external services.
- Sign in with a fake OIDC provider fixture (for example
  `e2e/fixtures/oidc.ts`) that routes the config, discovery, authorize, token
  and logout endpoints.
- Select elements by role and accessible name
  (`getByRole('button', { name: 'Save' })`). Use `data-testid` only for
  elements without a meaningful role.
- Assert with web-first assertions (`await expect(locator).toHaveText()`),
  never with sleeps.
- For live views (SSE, WebSocket), assert on rendered updates, not on timing.
- On failure, traces and screenshots are kept in `web/test-results/`. Open a
  trace with `bunx playwright show-trace <trace.zip>`.

## Done means

- The behavior is verified in the browser via MCP, with a clean console.
- A Playwright test covers the user-visible behavior.
- The project's web checks (lint, type check, unit tests) pass.
