/**
 * Differential mode: the same scenario against binary A, then B, over the SAME data path (wiped
 * between), and the two traces compared step by step.
 *
 * A trace step is `{ n, method, path, status, headers, body }` with the body masked; the docker
 * shim's recorded argv is appended as one extra pseudo-step, because the API's runners are quiet
 * and the commands it issues are otherwise invisible to any HTTP test — and they are the spec.
 *
 * Everything that may legitimately differ between two runs lives HERE and nowhere else: the masks
 * (ids, secrets, timestamps, ports) and KNOWN_DEVIATIONS. A deviation that is no longer observed
 * FAILS the run — the list cannot quietly outlive the difference it documents.
 */
import { Shim } from './docker-shim.ts';
import type { Booted } from './server.ts';

/** Response headers that are part of the contract; the rest (date, content-length, …) are not. */
export const HEADERS = [
  'content-type',
  'cache-control',
  'location',
  'set-cookie',
  'x-pstack-wake',
  'retry-after',
  'x-pstack-event',
  'x-pstack-delivery',
  'x-pstack-redelivery',
  'x-pstack-signature',
];

export const MASKS: Array<{ re: RegExp; to: string; why: string }> = [
  { re: /\b(up|down|verify|sleep|wake)-(.+?)-(\d+)-([a-z0-9]{1,6})(?![a-z0-9-])/g, to: '$1-$2-$3-<rand>', why: 'job ids: action-stack-seq-random (jobs.ts); the random tail is 1–6 base36 chars and the stack may carry dashes' },
  { re: /evt_[a-z0-9_]+/g, to: 'evt_<id>', why: 'event ids (events.ts), including the evt_test_ shape' },
  { re: /pstack_(ses|pat)_[0-9a-f]{64}/g, to: 'pstack_$1_<secret>', why: 'session and token secrets (auth.ts)' },
  { re: /whsec_[0-9a-f]{48}/g, to: 'whsec_<secret>', why: 'webhook signing secrets (webhooks.ts)' },
  { re: /eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/g, to: '<jwt>', why: 'share tokens (share.ts)' },
  { re: /"(at|since|startedAt|endedAt|createdAt|updatedAt|expiresAt|lastUsedAt|lastAt|lastLoginAt|timestamp|durationMs|waitedMs)": ?(\d{1,13}|null)/g, to: '"$1": <ts>', why: 'ms epochs and durations' },
  { re: /127\.0\.0\.1:\d+/g, to: '127.0.0.1:<port>', why: 'ephemeral ports of the server, receivers and providers' },
  { re: /(state|code_challenge|nonce)=[A-Za-z0-9_-]+/g, to: '$1=<rand>', why: 'PKCE state and challenge in a Location' },
  { re: /\/tmp\/[^"' \n]+|\/private\/var\/folders\/[^"' \n]+|\/var\/folders\/[^"' \n]+/g, to: '<tmp>', why: 'temp paths in bodies and argv' },
];

export function maskText(text: string): string {
  let out = text;
  for (const m of MASKS) out = out.replace(m.re, m.to);
  return out;
}

export type Step = { n: number; method: string; path: string; status: number; headers: Record<string, string>; body: string };

/**
 * A documented difference between the two implementations. The step is identified by scenario
 * and index; `why` is for the reader. Listed deviations must still DIFFER, or the run fails — a
 * stale entry is a divergence nobody is watching any more.
 */
export const KNOWN_DEVIATIONS: Array<{ scenario: string; step: number; why: string }> = [];

export class Session {
  readonly steps: Step[] = [];
  constructor(
    readonly s: Booted,
    readonly shim?: Shim,
  ) {}
  get base() {
    return this.s.base;
  }
  get H() {
    return this.s.H;
  }

  /** Make a request and record it. */
  async fetch(method: string, path: string, init: { body?: unknown; headers?: Record<string, string>; raw?: string } = {}): Promise<{ status: number; body: Record<string, unknown>; text: string; headers: Headers }> {
    const headers = { ...this.H, ...(init.headers ?? {}) };
    const res = await fetch(`${this.base}${path}`, { method, headers, body: init.raw ?? (init.body === undefined ? undefined : JSON.stringify(init.body)), redirect: 'manual' });
    const text = await res.text();
    const h: Record<string, string> = {};
    for (const k of HEADERS) {
      const v = res.headers.get(k);
      if (v !== null) h[k] = maskText(v);
    }
    this.steps.push({ n: this.steps.length, method, path: maskText(path), status: res.status, headers: h, body: maskText(text) });
    let body: Record<string, unknown> = {};
    try {
      body = JSON.parse(text);
    } catch {
      /* not json */
    }
    return { status: res.status, body, text, headers: res.headers };
  }

  /** Poll WITHOUT recording until `pred` holds, then record the final response once. */
  async until(method: string, path: string, pred: (body: Record<string, unknown>, status: number) => boolean, ms = 10_000): Promise<Record<string, unknown>> {
    const deadline = Date.now() + ms;
    for (;;) {
      const res = await fetch(`${this.base}${path}`, { method, headers: this.H });
      const text = await res.text();
      let body: Record<string, unknown> = {};
      try {
        body = JSON.parse(text);
      } catch {
        /* not json */
      }
      if (pred(body, res.status) || Date.now() > deadline) {
        const h: Record<string, string> = {};
        for (const k of HEADERS) {
          const v = res.headers.get(k);
          if (v !== null) h[k] = maskText(v);
        }
        this.steps.push({ n: this.steps.length, method, path: maskText(path), status: res.status, headers: h, body: maskText(text) });
        return body;
      }
      await Bun.sleep(20);
    }
  }

  /**
   * Let the clock move. Lists are ordered by ms timestamps (`ORDER BY created_at DESC`), and two
   * rows written in the same millisecond come back in an order no implementation promises — so a
   * scenario that creates several rows in a row steps the clock between them.
   */
  tick(): Promise<void> {
    return Bun.sleep(3);
  }

  /** Wait for a job, recording only its terminal state. */
  waitJob(id: string) {
    return this.until('GET', `/api/jobs/${id}`, (b) => !!b.job && (b.job as { state: string }).state !== 'running');
  }

  /** The docker argv the implementation issued, as one pseudo-step. */
  finish(): Step[] {
    if (this.shim) {
      this.steps.push({ n: this.steps.length, method: 'docker', path: '(argv)', status: 0, headers: {}, body: maskText(this.shim.calls().join('\n')) });
    }
    return this.steps;
  }
}

export type Scenario = { name: string; shim?: string; run: (s: Session) => Promise<void> };

/**
 * The first differing step, or null. `skip` names the steps a documented deviation covers: those are
 * reported separately (so the run can assert the deviation is still real) and the comparison goes
 * on past them — a deviation must not hide every step after it.
 */
export function compare(a: Step[], b: Step[], skip: Set<number> = new Set()): { index: number; a?: Step; b?: Step; skipped: number[] } | null {
  const n = Math.max(a.length, b.length);
  const skipped: number[] = [];
  for (let i = 0; i < n; i++) {
    const x = a[i];
    const y = b[i];
    if (!x || !y) return { index: i, a: x, b: y, skipped };
    const same = x.method === y.method && x.path === y.path && x.status === y.status && x.body === y.body && JSON.stringify(x.headers) === JSON.stringify(y.headers);
    if (same) continue;
    // A known deviation still has to be the SAME request with the same status — only the body may differ.
    if (skip.has(i) && x.method === y.method && x.path === y.path && x.status === y.status) {
      skipped.push(i);
      continue;
    }
    return { index: i, a: x, b: y, skipped };
  }
  return skipped.length ? { index: -1, skipped } : null;
}
