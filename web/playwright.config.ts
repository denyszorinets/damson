import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { defineConfig } from '@playwright/test';

// Chrome is provided by the devcontainer (see .claude/skills/frontend-debugging);
// Playwright's own browser installer does not support Fedora.
const chrome = '/opt/google/chrome/chrome';
const asRoot = typeof process.getuid === 'function' && process.getuid() === 0;
const noSandbox = asRoot || Boolean(process.env.CI);

// The tests run against `damson dev` (controller + one agent in one process,
// SQLite) serving the embedded build, so they exercise the real API.
// Created once: workers re-evaluate this file but inherit the env var.
process.env.DAMSON_E2E_DIR ??= mkdtempSync(join(tmpdir(), 'damson-e2e-'));
const dir = process.env.DAMSON_E2E_DIR;
const port = 4174;
const host = '127.0.0.1';

export default defineConfig({
	testDir: 'e2e',
	globalTeardown: './e2e/teardown.ts',
	expect: { timeout: 20_000 }, // the first poll can precede the agent's connection
	webServer: {
		command: `npm run build && cd .. && go build -o "${dir}/damson" ./cmd/damson && "${dir}/damson" dev`,
		env: {
			DAMSON_DATABASE__DSN: `file:${dir}/damson.db`,
			DAMSON_CONTROLLER__LISTEN: `${host}:${port}`
		},
		url: `http://${host}:${port}/healthz`,
		timeout: 180_000
	},
	use: {
		baseURL: `http://${host}:${port}`,
		launchOptions: {
			executablePath: process.env.CHROME_PATH ?? chrome,
			args: noSandbox ? ['--no-sandbox'] : []
		}
	}
});
