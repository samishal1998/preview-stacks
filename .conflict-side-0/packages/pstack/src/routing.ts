/**
 * Traefik dynamic configuration files — list, read, write, delete.
 *
 * WHAT THIS IS FOR. Traefik takes configuration from two providers: the **docker** provider (labels
 * on containers — how every per-PR router is declared) and the **file** provider, which watches a
 * directory and reloads on change. The file provider is where everything that is not a container
 * lives: middleware (basicAuth, rate limits, IP allow-lists), TLS options, the catch-all fallback
 * router, and routes to something running outside compose. Splitting it across several files is
 * supported and is the point of a watched *directory* rather than one file — one file per concern,
 * each independently editable.
 *
 * Until now that directory was created by `init` and then never touched again, so the only way to add
 * a middleware was to SSH in. This module is the API behind doing it from the UI.
 *
 * ── WHY THIS NEEDS MORE CARE THAN IT LOOKS ───────────────────────────────────────────────────────
 *
 * Traefik's own documented behaviour: an unparseable file in the watched directory produces a parse
 * error **for the directory**, and the rest of it can be discarded with it. The visible symptom is not
 * "my new middleware is broken", it is *routes elsewhere disappearing*. So a careless write here does
 * not break the thing you were editing — it breaks other people's previews.
 *
 * Three consequences, all load-bearing:
 *
 *   1. **Validate before touching disk.** Parse it, and require the top-level keys to be Traefik's.
 *      A typo'd `htttp:` is valid YAML that configures nothing, and Traefik will not tell you.
 *   2. **Write atomically.** `writeFile` truncates then fills, and the watcher can fire on the
 *      truncated file. Write a temp file in the same directory and `rename` it — rename is atomic
 *      within a filesystem, so the watcher only ever sees a whole file.
 *   3. **Never put anything in that directory that is not dynamic config.** No `.bak`, no temp file
 *      left behind, no dotfile — Traefik would try to parse it. This is why there is no on-disk
 *      version history: the obvious place to keep it is the one place it must not go. `write()`
 *      returns the previous content instead, so a caller can offer an immediate revert.
 *
 * ── WHAT IT CANNOT BREAK ─────────────────────────────────────────────────────────────────────────
 *
 * `control.<domain>` and `api.<domain>` are declared as **docker labels on the pstack container**, not
 * in this directory (see init.ts). So no file written here can lock you out of the UI or the API that
 * would let you fix it. That is not luck; keep it that way.
 */

import { readdir, readFile, rename, rm, stat, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

export class RoutingError extends Error {}

/**
 * A filename that is safe as a path component AND that Traefik will actually read.
 *
 * `.yml`/`.yaml` only: the file provider reads YAML and TOML, but allowing both formats here would
 * mean validating both, and one format is enough. No leading dot (a dotfile is still parsed, and is
 * harder to notice), no directory separator, no `..`.
 */
const NAME = /^[a-z0-9][a-z0-9._-]*\.ya?ml$/;

/** Traefik's top-level dynamic-configuration sections. Anything else is a typo or a mistake. */
const SECTIONS = new Set(['http', 'tcp', 'udp', 'tls']);

export function assertValidRoutingName(name: string): void {
  if (!NAME.test(name) || name.includes('..')) {
    throw new RoutingError(
      `invalid filename "${name}" — must be lowercase, end in .yml or .yaml, and contain no path ` +
        `separators (it becomes a file in Traefik's watched directory)`,
    );
  }
}

/**
 * Reject a file that would break the directory, or silently do nothing.
 *
 * Returns the parsed value on success, so a caller does not parse twice.
 *
 * The "silently does nothing" half matters as much as the parse half: `htttp:` and `middlewares:` at
 * the top level are both perfectly good YAML that Traefik ignores, and an ignored file is a support
 * ticket that starts "I added the middleware and nothing happened".
 */
export function validateRoutingContent(content: string): Record<string, unknown> {
  if (!content.trim()) {
    throw new RoutingError('the file is empty — delete it instead of storing an empty one');
  }

  let parsed: unknown;
  try {
    parsed = Bun.YAML.parse(content);
  } catch (err) {
    // Traefik's failure here is a parse error for the whole DIRECTORY, so this is the check that
    // stops one bad edit taking other routes down with it.
    throw new RoutingError(`not valid YAML: ${(err as Error).message}`);
  }

  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new RoutingError(
      'the top level must be a mapping of Traefik sections, e.g. `http:` with `routers:` under it',
    );
  }

  const keys = Object.keys(parsed as Record<string, unknown>);
  if (keys.length === 0) {
    throw new RoutingError('no sections — expected at least one of http, tcp, udp, tls');
  }
  const unknown = keys.filter((k) => !SECTIONS.has(k));
  if (unknown.length > 0) {
    throw new RoutingError(
      `unknown top-level section(s): ${unknown.join(', ')}. Traefik reads only ` +
        `${[...SECTIONS].join(', ')} — it would load this file and silently apply nothing. ` +
        `Middlewares and routers go *under* \`http:\`.`,
    );
  }

  return parsed as Record<string, unknown>;
}

export type RoutingFile = {
  name: string;
  /** Bytes. Enough for a list view to show something is there without reading every file. */
  size: number;
  /** Epoch milliseconds — mtime. */
  updatedAt: number;
};

/**
 * The watched directory, and whether this process can write to it.
 *
 * `writable` is probed rather than assumed because the pstack container only gained this mount in
 * 0.4.0: on a host whose control stack predates it the directory is simply absent from the
 * container, and the API must say "re-run `pstack init`" rather than fail with ENOENT.
 */
export class RoutingStore {
  readonly dir: string;

  constructor(dir: string) {
    this.dir = dir;
  }

  /** True when the directory exists and this process may write in it. */
  async writable(): Promise<boolean> {
    try {
      const s = await stat(this.dir);
      if (!s.isDirectory()) return false;
      // Probe by writing, not by reading a mode bit: the mount may be present but `:ro`, which is
      // exactly the misconfiguration worth detecting, and a mode check would not catch it.
      const probe = join(this.dir, `.pstack-write-probe-${process.pid}`);
      await writeFile(probe, '', 'utf8');
      await rm(probe, { force: true });
      return true;
    } catch {
      return false;
    }
  }

  async list(): Promise<RoutingFile[]> {
    let entries: string[];
    try {
      entries = await readdir(this.dir);
    } catch {
      return [];
    }
    const files: RoutingFile[] = [];
    for (const name of entries) {
      if (!NAME.test(name)) continue; // never present a file Traefik would not read
      try {
        const s = await stat(join(this.dir, name));
        if (s.isFile()) files.push({ name, size: s.size, updatedAt: Math.trunc(s.mtimeMs) });
      } catch {
        /* vanished between readdir and stat — nothing to report */
      }
    }
    return files.sort((a, b) => a.name.localeCompare(b.name));
  }

  async read(name: string): Promise<string> {
    assertValidRoutingName(name);
    try {
      return await readFile(join(this.dir, name), 'utf8');
    } catch {
      throw new RoutingError(`no such routing file: ${name}`);
    }
  }

  /**
   * Validate, then replace atomically. Returns the previous content, or null if it is new.
   *
   * The temp file goes in the SAME directory because `rename` is only atomic within a filesystem —
   * across one it degrades to copy-then-delete, which is the partial-file window this exists to
   * avoid. It is named so that Traefik will not read it (no `.yml` suffix) and is removed on failure,
   * because a leftover file in this directory is exactly what must never happen.
   */
  async write(name: string, content: string): Promise<string | null> {
    assertValidRoutingName(name);
    validateRoutingContent(content);
    if (!(await this.writable())) {
      throw new RoutingError(
        `Traefik's dynamic directory is not writable from here (${this.dir}). The control stack ` +
          `mounts it into the API from 0.4.0 onward — re-run \`pstack init\` on the host to pick ` +
          `up the mount.`,
      );
    }

    const target = join(this.dir, name);
    const previous = await readFile(target, 'utf8').catch(() => null);
    const tmp = join(this.dir, `.pstack-tmp-${process.pid}-${name}.part`);
    try {
      await writeFile(tmp, content, 'utf8');
      await rename(tmp, target);
    } catch (err) {
      await rm(tmp, { force: true }).catch(() => {});
      throw new RoutingError(`could not write ${name}: ${(err as Error).message}`);
    }
    return previous;
  }

  /** Returns the removed content, so a caller can offer an undo. */
  async remove(name: string): Promise<string> {
    assertValidRoutingName(name);
    const content = await this.read(name);
    if (!(await this.writable())) {
      throw new RoutingError(`Traefik's dynamic directory is not writable from here (${this.dir}).`);
    }
    await rm(join(this.dir, name), { force: true });
    return content;
  }
}
