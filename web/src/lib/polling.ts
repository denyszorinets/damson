/**
 * Runs `fn` now and then every `intervalMs`, skipping ticks while the tab is
 * hidden and refreshing immediately when it becomes visible again. Calls never
 * overlap. `fn` gets a signal that aborts on stop. Returns a stop function; call it on destroy.
 */
export function poll(fn: (signal: AbortSignal) => Promise<void>, intervalMs: number): () => void {
	let stopped = false;
	let running = false;
	const ctrl = new AbortController();
	let timer: ReturnType<typeof setTimeout> | undefined;

	const tick = async () => {
		if (stopped || running) return;
		running = true;
		try {
			await fn(ctrl.signal);
		} catch {
			// fn reports its own errors; a thrown one must not stop polling.
		} finally {
			running = false;
			schedule();
		}
	};
	const schedule = () => {
		clearTimeout(timer);
		if (stopped) return;
		timer = setTimeout(() => {
			if (document.hidden) schedule();
			else void tick();
		}, intervalMs);
	};
	const onVisible = () => {
		if (!document.hidden) void tick();
	};

	document.addEventListener('visibilitychange', onVisible);
	void tick();
	return () => {
		stopped = true;
		ctrl.abort();
		clearTimeout(timer);
		document.removeEventListener('visibilitychange', onVisible);
	};
}
