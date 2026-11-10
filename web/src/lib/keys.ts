/** The index a navigation key moves to inside a list of `count` items, or null when the key is not one. */
export function nextIndex(current: number, count: number, key: string): number | null {
	if (count <= 0) return null;
	switch (key) {
		case 'ArrowDown':
		case 'ArrowRight':
			return (current + 1) % count;
		case 'ArrowUp':
		case 'ArrowLeft':
			return (current - 1 + count) % count;
		case 'Home':
			return 0;
		case 'End':
			return count - 1;
		default:
			return null;
	}
}

/** Whether a key press happened while typing, so a shortcut must leave it alone. */
export function isTyping(target: EventTarget | null): boolean {
	const el = target as HTMLElement | null;
	if (!el || !el.tagName) return false;
	return el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName);
}

/**
 * An action for a navigation list: arrow keys, Home and End move focus between its
 * links. Tab still walks them all; this only adds the shortcuts.
 */
export function arrowNav(node: HTMLElement): { destroy: () => void } {
	function onkeydown(e: KeyboardEvent) {
		if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
		const links = Array.from(node.querySelectorAll<HTMLElement>('a[href]'));
		const at = links.indexOf(document.activeElement as HTMLElement);
		if (at < 0) return;
		const to = nextIndex(at, links.length, e.key);
		if (to === null) return;
		e.preventDefault();
		links[to].focus();
	}
	node.addEventListener('keydown', onkeydown);
	return { destroy: () => node.removeEventListener('keydown', onkeydown) };
}
