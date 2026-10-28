import { rmSync } from 'node:fs';

// Removes the temporary directory (binary and SQLite database) created in playwright.config.ts.
export default function globalTeardown() {
	const dir = process.env.DAMSON_E2E_DIR;
	if (dir) rmSync(dir, { recursive: true, force: true });
}
