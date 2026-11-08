import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { defineConfig } from '@playwright/test';

// The login tests: `damson dev` with OIDC against the fake provider
// (internal/auth/oidctest/cmd/fakeidp), which approves every login at once as
// the user last set with /_login-as. Run with `npx playwright test -c playwright.oidc.config.ts`.
const chrome = '/opt/google/chrome/chrome';
const asRoot = typeof process.getuid === 'function' && process.getuid() === 0;
const noSandbox = asRoot || Boolean(process.env.CI);

process.env.DAMSON_E2E_DIR ??= mkdtempSync(join(tmpdir(), 'damson-e2e-oidc-'));
const dir = process.env.DAMSON_E2E_DIR;
const host = '127.0.0.1';
const appPort = 4175;
const idpPort = 4191;
const app = `http://${host}:${appPort}`;
const idp = `http://${host}:${idpPort}`;

export default defineConfig({
	testDir: 'e2e-oidc',
	globalTeardown: './e2e/teardown.ts',
	workers: 1, // the provider's "next user" and the database are shared
	expect: { timeout: 20_000 },
	webServer: [
		{
			command: `cd .. && go build -o "${dir}/fakeidp" ./internal/auth/oidctest/cmd/fakeidp && "${dir}/fakeidp" ${host}:${idpPort}`,
			url: `${idp}/.well-known/openid-configuration`,
			timeout: 120_000
		},
		{
			command: `npm run build && cd .. && go build -o "${dir}/damson" ./cmd/damson && "${dir}/damson" dev`,
			env: {
				DAMSON_DATABASE__DSN: `file:${dir}/damson.db`,
				DAMSON_CONTROLLER__LISTEN: `${host}:${appPort}`,
				DAMSON_AUTH__COOKIE_INSECURE: 'true',
				DAMSON_AUTH__OIDC__ISSUER: idp,
				DAMSON_AUTH__OIDC__CLIENT_ID: 'damson',
				DAMSON_AUTH__OIDC__CLIENT_SECRET: 'damson-secret',
				DAMSON_AUTH__OIDC__REDIRECT_URL: `${app}/auth/callback`,
				DAMSON_AUTH__OIDC__POST_LOGOUT_REDIRECT_URL: `${app}/`,
				DAMSON_AUTH__OIDC__ADMIN_RULE: `'damson-admins' in claims.groups`
			},
			url: `${app}/healthz`,
			timeout: 180_000
		}
	],
	use: {
		baseURL: app,
		launchOptions: {
			executablePath: process.env.CHROME_PATH ?? chrome,
			args: noSandbox ? ['--no-sandbox'] : []
		}
	}
});
