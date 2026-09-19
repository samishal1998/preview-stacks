#!/usr/bin/env bun
/**
 * A small Chrome DevTools Protocol driver for the headless Chrome that serve.sh starts.
 *
 * As a module (write the driver in $RUN, never in the repo or /tmp):
 *
 *   import { open } from '/Volumes/S1/code/preview-stacks/.claude/skills/pstack-ui-verify/scripts/cdp.ts';
 *   const p = await open();                       // label "default": reads its run.env, opens a new tab
 *   await p.login(p.env.SPA);                     // p.env.API for the basic UI
 *   await p.nav(`${p.env.SPA}/control`);
 *   await p.waitFor((t) => document.body.textContent.includes(t), 'Logging');
 *   await p.viewport(320); await p.scheme('dark');
 *   console.log(await p.facts(), await p.shot('control-320-dark'));
 *   await p.close();
 *
 * As a command: `bun cdp.ts shots [label] spa|basic [/path ...]` — logs in, then per path a full-page
 * screenshot at 1280 and 320, light and dark, and one JSON line of facts per screenshot.
 *
 * ev() takes a FUNCTION and its arguments and ships `(fn)(...args)`: Bun hands back fn's transpiled
 * source, so nothing is assembled from strings (hand-built nested template literals broke parsing
 * before). The function runs in the page, so it must be self-contained: it sees its arguments and
 * the page's globals, never this module's variables. A string is still accepted as a raw expression.
 */
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const ROOT = process.env.PSTACK_UIV_ROOT ?? join(process.env.HOME ?? '', '.claude/projects/-Volumes-S1-code-preview-stacks/ui-verify');

type Fn = (...args: any[]) => unknown;

/** run.env as an object: API, SPA, CDP, TOKEN, ADMIN_USER, ADMIN_PASSWORD, SHOTS, RUN, LABEL. */
export function runEnv(label = 'default'): Record<string, string> {
  let text: string;
  try {
    text = readFileSync(join(ROOT, label, 'run.env'), 'utf8');
  } catch {
    throw new Error(`no run.env for "${label}": run serve.sh start ${label} first`);
  }
  return Object.fromEntries(text.split('\n').filter((l) => l.includes('=')).map((l) => [l.slice(0, l.indexOf('=')), l.slice(l.indexOf('=') + 1)]));
}

export async function open(label = 'default') {
  const env = runEnv(label);
  const target = (await (await fetch(`${env.CDP}/json/new?about:blank`, { method: 'PUT' })).json()) as { id: string; webSocketDebuggerUrl: string };
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((ok, bad) => ((ws.onopen = ok), (ws.onerror = bad)));

  let seq = 0;
  const pending = new Map<number, { ok: (v: any) => void; bad: (e: Error) => void }>();
  const waiters: Array<{ method: string; ok: (params: any) => void }> = [];
  /** Uncaught exceptions, console.error and browser-logged errors (a failed request) since the last nav(). */
  let errors: string[] = [];
  ws.onmessage = (m) => {
    const d = JSON.parse(String(m.data));
    if (d.id) {
      const p = pending.get(d.id);
      pending.delete(d.id);
      if (d.error) p?.bad(new Error(`${d.error.message} ${d.error.data ?? ''}`.trim()));
      else p?.ok(d.result);
      return;
    }
    if (d.method === 'Runtime.exceptionThrown') errors.push(d.params.exceptionDetails.exception?.description ?? d.params.exceptionDetails.text);
    if (d.method === 'Runtime.consoleAPICalled' && d.params.type === 'error') errors.push(d.params.args.map((a: any) => a.value ?? a.description).join(' '));
    if (d.method === 'Log.entryAdded' && d.params.entry.level === 'error') errors.push(`${d.params.entry.text} ${d.params.entry.url ?? ''}`.trim());
    const i = waiters.findIndex((w) => w.method === d.method);
    if (i >= 0) waiters.splice(i, 1)[0].ok(d.params);
  };

  const send = (method: string, params: Record<string, unknown> = {}): Promise<any> =>
    new Promise((ok, bad) => {
      const id = ++seq;
      pending.set(id, { ok, bad });
      ws.send(JSON.stringify({ id, method, params }));
    });
  const once = (method: string, ms: number): Promise<any> =>
    Promise.race([new Promise((ok) => waiters.push({ method, ok })), Bun.sleep(ms).then(() => Promise.reject(new Error(`no ${method} within ${ms}ms`)))]);

  async function ev<T = any>(fn: Fn | string, ...args: unknown[]): Promise<T> {
    const expression = typeof fn === 'string' ? fn : `(${fn})(...${JSON.stringify(args)})`;
    const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
    return r.result.value as T;
  }

  /** Poll until fn returns something truthy (and return it), or throw after ms. For SPA content and route changes. */
  async function waitFor<T = any>(fn: Fn | string, ...args: unknown[]): Promise<T> {
    const deadline = Date.now() + 10_000;
    for (;;) {
      const v = await ev<T>(fn, ...args).catch(() => undefined);
      if (v) return v;
      if (Date.now() > deadline) throw new Error(`waitFor timed out: ${String(fn).slice(0, 160)}`);
      await Bun.sleep(100);
    }
  }

  /** A full navigation: waits for the load event, then a short settle for the SPA's first fetches. */
  async function nav(url: string): Promise<void> {
    errors = [];
    const loaded = once('Page.loadEventFired', 15_000);
    const r = await send('Page.navigate', { url });
    if (r.errorText) throw new Error(`navigate ${url}: ${r.errorText}`);
    await loaded;
    await Bun.sleep(800);
  }

  /**
   * Sign in through POST /api/auth/login from the page's own origin, the request the login form
   * makes. Clears that origin's localStorage first: a stale `pstack.ui.apiBase` sends the SPA past
   * the vite proxy and login fails. When the login form itself is under test, drive the form instead.
   */
  async function login(base: string, user = env.ADMIN_USER, password = env.ADMIN_PASSWORD): Promise<void> {
    await nav(`${base}/login`);
    const status = await ev(
      async (u: string, pw: string) => {
        localStorage.clear();
        const r = await fetch('/api/auth/login', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: u, password: pw }) });
        return r.status;
      },
      user,
      password,
    );
    if (status !== 200) throw new Error(`login ${user} at ${base}: HTTP ${status}`);
  }

  /** Layout width in CSS px. mobile:false so the width is exact — breakpoints and container queries see 320, not a scaled 980. */
  const viewport = (width: number, height = 900) => send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });

  /** prefers-color-scheme. Only reaches the page while its own theme setting is "system" (the default after login() cleared storage). */
  const scheme = (value: 'light' | 'dark') => send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value }] });

  /** A PNG in $RUN/shots/<name>.png; full = the whole scrollable page, not just the viewport. Returns the path. */
  async function shot(name: string, o: { full?: boolean } = {}): Promise<string> {
    const params: Record<string, unknown> = { format: 'png' };
    if (o.full) {
      const { cssContentSize: s } = await send('Page.getLayoutMetrics');
      Object.assign(params, { captureBeyondViewport: true, clip: { x: 0, y: 0, width: s.width, height: s.height, scale: 1 } });
    }
    const { data } = await send('Page.captureScreenshot', params);
    const path = join(env.SHOTS, `${name}.png`);
    await Bun.write(path, Buffer.from(data, 'base64'));
    return path;
  }

  /** A real key press. element.focus() never sets :focus-visible; a keyboard Tab does. modifiers: 8 = shift. */
  async function press(key: string, keyCode: number, modifiers = 0): Promise<void> {
    const k = { key, code: key, windowsVirtualKeyCode: keyCode, modifiers };
    await send('Input.dispatchKeyEvent', { type: 'keyDown', ...k });
    await send('Input.dispatchKeyEvent', { type: 'keyUp', ...k });
  }
  const tab = (shift = false) => press('Tab', 9, shift ? 8 : 0);

  /** Signed out everywhere: every cookie, and all storage for both UIs' origins. */
  async function signOut(): Promise<void> {
    await send('Network.clearBrowserCookies');
    for (const origin of [env.SPA, env.API]) await send('Storage.clearDataForOrigin', { origin, storageTypes: 'all' });
  }

  /** What a reviewer asks first: where am I, what does it say, does it scroll sideways, did anything throw. */
  async function facts() {
    const page = await ev(() => ({
      url: location.href,
      title: document.title,
      heading: document.querySelector('h1, h2')?.textContent?.trim() ?? null,
      width: innerWidth,
      overflowX: document.documentElement.scrollWidth > innerWidth,
      focused: document.activeElement && document.activeElement !== document.body ? document.activeElement.outerHTML.slice(0, 120) : null,
    }));
    return { ...page, errors: [...errors] };
  }

  async function close(): Promise<void> {
    ws.close();
    await fetch(`${env.CDP}/json/close/${target.id}`).catch(() => undefined);
  }

  await send('Page.enable');
  await send('Runtime.enable');
  await send('Log.enable');
  return { env, send, ev, waitFor, nav, login, viewport, scheme, shot, press, tab, signOut, facts, close, errors: () => [...errors] };
}

if (import.meta.main) {
  const [cmd, label = 'default', ui = 'spa', ...paths] = process.argv.slice(2);
  if (cmd !== 'shots' || (ui !== 'spa' && ui !== 'basic')) {
    console.error('usage: bun cdp.ts shots [label] spa|basic [/path ...]');
    process.exit(2);
  }
  const p = await open(label);
  const base = ui === 'spa' ? p.env.SPA : p.env.API;
  try {
    await p.signOut();
    await p.login(base);
    for (const path of paths.length ? paths : ['/']) {
      await p.viewport(1280);
      await p.nav(base + path);
      const slug = path.replace(/^\/|\/$/g, '').replace(/\W+/g, '-') || 'root';
      for (const width of [1280, 320])
        for (const s of ['light', 'dark'] as const) {
          await p.viewport(width);
          await p.scheme(s);
          await Bun.sleep(300);
          const file = await p.shot(`${ui}-${slug}-${width}-${s}`, { full: true });
          console.log(JSON.stringify({ file, ...(await p.facts()) }));
        }
    }
  } finally {
    await p.close();
  }
}
