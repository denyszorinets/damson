/** A project search: a plain text matches anywhere in the path, `/regex/` is a regular expression. */
export interface ProjectMatcher {
	/** Whether the query is empty: nothing to filter by. */
	empty: boolean;
	/** The problem with the query (an invalid regular expression), or null. */
	error: string | null;
	test: (path: string) => boolean;
}

const NONE = (): boolean => false;

export function projectMatcher(query: string): ProjectMatcher {
	const q = query.trim();
	if (!q) return { empty: true, error: null, test: () => true };
	// A query is a regular expression only when it reads /pattern/flags with real flags;
	// "/acme/pay" is plain text.
	const re = /^\/(.+)\/([dgimsuvy]*)$/.exec(q);
	if (re) {
		try {
			// g and y make test() stateful across paths, so they are dropped; matching is case-insensitive.
			const flags = re[2].replace(/[gy]/g, '');
			const rx = new RegExp(re[1], flags.includes('i') ? flags : flags + 'i');
			return { empty: false, error: null, test: (path) => rx.test(path) };
		} catch (e) {
			return {
				empty: false,
				error: e instanceof Error ? e.message : 'Invalid regular expression',
				test: NONE
			};
		}
	}
	const needle = q.toLowerCase();
	return { empty: false, error: null, test: (path) => path.toLowerCase().includes(needle) };
}
