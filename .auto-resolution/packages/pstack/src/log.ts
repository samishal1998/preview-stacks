/**
 * A logging seam.
 *
 * The CLI wants progress on stdout; the HTTP API wants the same events captured per job and
 * streamed to a browser. Rather than have the orchestrator write to `console.log` and let the
 * server scrape it, both consumers pass a sink.
 *
 * (This replaces the `ponytail:` note in stack.ts — the seam was deliberately deferred until a
 * second consumer existed. The API is that consumer.)
 */

export type LogEvent = {
  /** Monotonic within a sink; useful for resuming an SSE stream. */
  seq: number;
  at: number;
  level: 'info' | 'step' | 'warn' | 'error';
  message: string;
};

export type Sink = {
  emit(level: LogEvent['level'], message: string): void;
};

/** Writes to stdout. What the CLI uses. */
export function consoleSink(): Sink {
  let seq = 0;
  return {
    emit(level, message) {
      seq++;
      if (level === 'error') console.error(message);
      else console.log(message);
    },
  };
}

/** Discards everything. Used by tests that assert on results rather than output. */
export function nullSink(): Sink {
  return { emit() {} };
}

/**
 * Buffers events and notifies subscribers. What the API uses: the job holds the buffer so a
 * browser attaching late still gets the whole history, then live updates.
 */
export function bufferSink(onEvent?: (e: LogEvent) => void): Sink & { events: LogEvent[] } {
  const events: LogEvent[] = [];
  let seq = 0;
  return {
    events,
    emit(level, message) {
      const e: LogEvent = { seq: ++seq, at: Date.now(), level, message };
      events.push(e);
      onEvent?.(e);
    },
  };
}
