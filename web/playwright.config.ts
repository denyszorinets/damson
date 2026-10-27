import { defineConfig } from '@playwright/test';

// Chrome is provided by the devcontainer (see .claude/skills/frontend-debugging);
// Playwright's own browser installer does not support Fedora.
const chrome = '/opt/google/chrome/chrome';
const asRoot = typeof process.getuid === 'function' && process.getuid() === 0;

export default defineConfig({
	testDir: 'e2e',
	webServer: { command: 'npm run build && npm run preview -- --port 4173', port: 4173 },
	use: {
		baseURL: 'http://localhost:4173',
		launchOptions: {
			executablePath: process.env.CHROME_PATH ?? chrome,
			args: asRoot ? ['--no-sandbox'] : []
		}
	}
});
