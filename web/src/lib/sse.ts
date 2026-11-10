/**
 * A server-sent event stream with reconnect. EventSource reconnects by itself
 * after a dropped connection and sends Last-Event-ID; the run stream answers
 * every connection with a fresh snapshot, so a handler for it replaces what it
 * knew. When the browser gives up (the connection was refused or answered with
 * an error status) the stream is reopened with a growing delay.
 */
export type StreamState = 'connecting' | 'live' | 'reconnecting' | 'closed';

export interface StreamOptions {
	/** Event handlers by event name; data is the parsed JSON payload. */
	on: Record<string, (data: unknown) => void>;
	/** Event names after which the server ends the stream; no reconnect follows. */
	endOn?: string[];
	onState?: (state: StreamState) => void;
	/** Delay bounds for reopening a refused stream, in ms. */
	minDelay?: number;
	maxDelay?: number;
}

/**
 * Opens the stream and returns a function that closes it for good. A url
 * function is asked again on every reopen, so a stream can resume from where it was.
 */
export function openStream(url: string | (() => string), opts: StreamOptions): () => void {
	const min = opts.minDelay ?? 1000;
	const max = opts.maxDelay ?? 30000;
	let source: EventSource | undefined;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let delay = min;
	let stopped = false;

	const state = (s: StreamState) => opts.onState?.(s);

	const connect = () => {
		if (stopped) return;
		const es = new EventSource(typeof url === 'function' ? url() : url);
		source = es;
		es.onopen = () => {
			delay = min;
			state('live');
		};
		es.onerror = () => {
			if (stopped) return;
			if (es.readyState === EventSource.CLOSED) {
				// Refused or answered with an error: EventSource will not retry.
				es.close();
				state('reconnecting');
				timer = setTimeout(connect, delay);
				delay = Math.min(delay * 2, max);
			} else {
				state('reconnecting'); // the browser is reconnecting
			}
		};
		for (const [name, handler] of Object.entries(opts.on)) {
			es.addEventListener(name, (ev) => {
				let data: unknown;
				try {
					data = JSON.parse((ev as MessageEvent<string>).data);
				} catch {
					return; // a malformed event must not end the stream
				}
				handler(data);
				if (opts.endOn?.includes(name)) {
					stopped = true;
					es.close();
					state('closed');
				}
			});
		}
		state('connecting');
	};

	connect();
	return () => {
		stopped = true;
		clearTimeout(timer);
		source?.close();
		state('closed');
	};
}
