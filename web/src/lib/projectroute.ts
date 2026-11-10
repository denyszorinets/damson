/**
 * Project URLs are /projects/<path>/-/<tab>. SvelteKit allows a rest parameter
 * only last, so one catch-all route reads the path up to the "-" segment and
 * the tab after it. A project path never holds a "-" segment of its own.
 */
export interface ProjectRoute {
	path: string;
	/** The tab segments after "-"; empty for the project's front page. */
	tab: string[];
}

const SEP = '-';

export function parseProjectRoute(rest: string | undefined): ProjectRoute {
	const parts = (rest ?? '').split('/').filter((s) => s !== '');
	const at = parts.indexOf(SEP);
	if (at < 0) return { path: parts.join('/'), tab: [] };
	return { path: parts.slice(0, at).join('/'), tab: parts.slice(at + 1) };
}

/** The rest parameter for a project page, or one of its tabs. */
export function projectRest(path: string, ...tab: string[]): string {
	return tab.length ? `${path}/${SEP}/${tab.join('/')}` : path;
}

export interface Crumb {
	path: string;
	label: string;
}

/** One crumb per project on the way to path, itself included. */
export function breadcrumb(path: string): Crumb[] {
	const parts = path.split('/').filter((s) => s !== '');
	return parts.map((label, i) => ({ path: parts.slice(0, i + 1).join('/'), label }));
}
