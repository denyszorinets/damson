<script lang="ts">
	import { onMount } from 'svelte';
	import { parseAnsi } from '$lib/ansi';
	import {
		LogBuffer,
		MAX_LOG_ROWS,
		visibleWindow,
		type LogGap,
		type LogLevel,
		type LogLine,
		type LogRow
	} from '$lib/logs';
	import { openStream, type StreamState } from '$lib/sse';

	/**
	 * The log of one execution: the logs endpoint streamed live, ANSI colours, a
	 * virtualized list (only the rows in view are in the DOM), de-duplicated by
	 * sequence, and bounded to the newest MAX_LOG_ROWS lines. A dropped connection
	 * reopens from the last sequence seen.
	 */
	let { executionId }: { executionId: string } = $props();

	const ROW = 20; // px, fixed so the window is arithmetic
	const VIEWPORT = 320;

	const buffer = new LogBuffer();
	let rows = $state<LogRow[]>([]);
	let stats = $state({ total: 0, dropped: 0, lastSeq: 0 });
	let conn = $state<StreamState>('connecting');
	let ended = $state('');
	let scrollTop = $state(0);
	let follow = $state(true);
	let level = $state<LogLevel>('all');
	let box: HTMLDivElement | undefined = $state();
	let frame = 0;

	// Lines arrive in bursts: publish at most once per animation frame.
	function schedule() {
		if (frame) return;
		frame = requestAnimationFrame(() => {
			frame = 0;
			rows = buffer.view(level);
			stats = { total: buffer.total, dropped: buffer.dropped, lastSeq: buffer.lastSeq };
			if (follow && box) {
				requestAnimationFrame(() => box && (box.scrollTop = box.scrollHeight));
			}
		});
	}

	onMount(() => {
		const close = openStream(
			() =>
				`/api/v1/executions/${encodeURIComponent(executionId)}/logs?after_seq=${buffer.lastSeq}`,
			{
				on: {
					log: (d) => buffer.push(d as LogLine) && schedule(),
					gap: (d) => buffer.gap(d as LogGap) && schedule(),
					end: (d) => {
						ended = (d as { state: string }).state;
						schedule();
					}
				},
				endOn: ['end'],
				onState: (s) => (conn = s)
			}
		);
		return () => {
			close();
			if (frame) cancelAnimationFrame(frame);
		};
	});

	// A new filter re-reads the buffer at once and shows the newest rows.
	function setLevel(next: LogLevel) {
		level = next;
		rows = buffer.view(level);
		follow = true;
		schedule();
	}

	const win = $derived(visibleWindow(rows.length, scrollTop, VIEWPORT, ROW));
	const slice = $derived(rows.slice(win.start, win.end));

	function onScroll() {
		if (!box) return;
		scrollTop = box.scrollTop;
		follow = box.scrollHeight - box.scrollTop - box.clientHeight < ROW * 2;
	}

	function latest() {
		follow = true;
		if (box) box.scrollTop = box.scrollHeight;
	}
</script>

<section class="space-y-2" data-testid="log-viewer" aria-label="Log">
	<div class="flex items-center gap-3 text-xs text-muted-foreground">
		<span data-testid="log-count">{stats.total} lines</span>
		{#if stats.dropped}
			<span data-testid="log-dropped"
				>showing the last {Math.min(rows.length, MAX_LOG_ROWS)}; {stats.dropped} earlier not kept</span
			>
		{/if}
		<span class="flex gap-1" role="group" aria-label="Show lines of" data-testid="log-levels">
			{#each ['all', 'stdout', 'stderr'] as const as l (l)}
				<button
					type="button"
					class="rounded border px-1.5 py-0.5"
					class:bg-muted={level === l}
					class:text-foreground={level === l}
					aria-pressed={level === l}
					onclick={() => setLevel(l)}
					data-testid={`log-level-${l}`}>{l}</button
				>
			{/each}
		</span>
		<span data-testid="log-connection">{ended ? `ended: ${ended}` : conn}</span>
		{#if !follow}
			<button type="button" class="underline" onclick={latest} data-testid="log-latest"
				>Jump to latest</button
			>
		{/if}
	</div>
	<!-- A scrollable region must take focus to scroll by keyboard. -->
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div
		bind:this={box}
		onscroll={onScroll}
		class="relative overflow-auto rounded-md border bg-muted/30 font-mono text-xs"
		style={`height:${VIEWPORT}px`}
		role="log"
		tabindex="0"
		aria-label="Execution log"
		data-testid="log-box"
	>
		<div style={`height:${rows.length * ROW}px`} class="relative min-w-max">
			{#each slice as row, i (row.kind === 'line' ? row.seq : `gap-${row.from}`)}
				<div
					class="absolute right-0 left-0 px-3 leading-5 whitespace-pre"
					style={`top:${(win.start + i) * ROW}px;height:${ROW}px`}
					data-testid="log-row"
					data-seq={row.kind === 'line' ? row.seq : undefined}
				>
					{#if row.kind === 'gap'}
						<span class="text-amber-600 italic">lines {row.from} to {row.to} were lost</span>
					{:else}
						<span class={row.stream === 'stderr' ? 'text-red-700 dark:text-red-400' : ''}>
							{#each parseAnsi(row.text) as seg, k (k)}<span class={seg.cls}>{seg.text}</span
								>{/each}
						</span>
					{/if}
				</div>
			{/each}
		</div>
	</div>
</section>
