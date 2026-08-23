/**
 * Named specs — store a spec once, reference it from many deployments.
 *
 * WHAT THIS FIXES. Before this, every deployment carried its own copy of the spec, so 50 open PRs
 * meant 50 byte-identical YAML files and a fix to the teardown logic had to be re-submitted 50
 * times. Worse, variables were not stored at all: `?PR=7` had to be repeated on every call, and
 * passing a different value to `down` than to `up` silently tore down a *different stack* — the
 * exact orphan class this project exists to prevent.
 *
 * So a deployment now has two possible shapes:
 *
 *   inline      { spec: "<yaml>" }              its own copy, as before — still valid, still useful
 *                                               for a one-off that shares nothing
 *   referencing { specName: "web", vars: {…} }  points at a named spec; the vars are STORED
 *
 * Layout, alongside `deployments/`:
 *
 *   $PSTACK_DATA/specs/<name>/
 *     spec.yml       the spec source
 *     compose.yml    the compose file it references, when one was submitted with it
 *     meta.json      { name, kind, createdAt, updatedAt, description? }
 *
 * Same reasoning as the deployment registry: a directory of YAML, not a database. It is a cache of
 * intent, never the source of truth about what exists — that stays in Docker and in each axis's own
 * `assert_*` probe.
 */

import { mkdir, readdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { parseSpec, type Kind } from './spec.ts';

export type SpecMeta = {
  name: string;
  /** Resolved from the stored source at write time, so a list view needs no re-parse. */
  kind: Kind;
  description?: string;
  createdAt: number;
  updatedAt: number;
  /** Variable names the spec interpolates but does not define — what a caller MUST supply. */
  requiredVars: string[];
};

export type StoredSpec = SpecMeta & {
  dir: string;
  specPath: string;
};

/** Same alphabet as a deployment id: becomes a directory name, so no traversal and no whitespace. */
const NAME = /^[a-z0-9][a-z0-9._-]{0,63}$/;

export class SpecStoreError extends Error {}

export function assertValidSpecName(name: string): void {
  if (!NAME.test(name) || name.includes('..')) {
    throw new SpecStoreError(
      `invalid spec name "${name}" — must match ${NAME} (lowercase, no traversal, no spaces)`,
    );
  }
}

/**
 * Which `${VAR}` references a spec cannot satisfy on its own.
 *
 * Parsing needs every variable defined, so this probes with a sentinel environment and collects the
 * names the parser rejects, one round at a time. Reported up front, a caller learns "this spec needs
 * PR and GIT_SHA" from a list view instead of discovering it from a 400 on deploy.
 *
 * Bounded: a spec whose own `env:` block is self-referential could otherwise loop.
 */
export function findRequiredVars(source: string): string[] {
  const found = new Set<string>();
  for (let i = 0; i < 32; i++) {
    try {
      // Only the sentinels — NOT process.env. A variable that happens to exist in the server's
      // environment would otherwise be silently satisfied here and then be missing on a machine
      // where it does not, which is the worst kind of works-on-my-box.
      parseSpec(source, Object.fromEntries([...found].map((k) => [k, 'x'])));
      return [...found].sort();
    } catch (err) {
      const m = /undefined variable\(s\) (.+?)\./.exec((err as Error).message);
      if (!m) return [...found].sort(); // a different error — not our problem to report here
      const names = m[1]!.match(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g) ?? [];
      const before = found.size;
      for (const n of names) found.add(n.slice(2, -1));
      if (found.size === before) return [...found].sort(); // no progress; stop rather than spin
    }
  }
  return [...found].sort();
}

export class SpecStore {
  readonly root: string;

  constructor(dataDir: string) {
    this.root = join(dataDir, 'specs');
  }

  #dir(name: string): string {
    assertValidSpecName(name);
    return join(this.root, name);
  }

  async list(): Promise<SpecMeta[]> {
    let entries: string[];
    try {
      entries = await readdir(this.root);
    } catch {
      return [];
    }
    const out: SpecMeta[] = [];
    for (const name of entries) {
      const meta = await this.#readMeta(name).catch(() => null);
      if (meta) out.push(meta);
    }
    return out.sort((a, b) => b.updatedAt - a.updatedAt);
  }

  async get(name: string): Promise<StoredSpec | null> {
    const dir = this.#dir(name);
    try {
      await stat(join(dir, 'spec.yml'));
    } catch {
      return null;
    }
    return { ...(await this.#readMeta(name)), dir, specPath: join(dir, 'spec.yml') };
  }

  /** The raw source, for showing or editing. */
  async source(name: string): Promise<string> {
    const s = await this.get(name);
    if (!s) throw new SpecStoreError(`no such spec: ${name}`);
    return readFile(s.specPath, 'utf8');
  }

  /**
   * Create or replace a named spec.
   *
   * Validated before anything is written, with sentinel values for whatever it interpolates — a
   * named spec is a template, so it must be storable without knowing a particular PR's values.
   */
  async put(
    name: string,
    specYaml: string,
    opts: { composeYaml?: string; description?: string } = {},
  ): Promise<StoredSpec> {
    const dir = this.#dir(name);

    const requiredVars = findRequiredVars(specYaml);
    let kind: Kind;
    try {
      kind = parseSpec(specYaml, Object.fromEntries(requiredVars.map((k) => [k, 'x']))).kind;
    } catch (err) {
      // Nothing has touched disk yet, so there is no rollback to do — the failure is clean.
      throw new SpecStoreError(`spec rejected: ${(err as Error).message}`);
    }

    await mkdir(dir, { recursive: true });
    const specPath = join(dir, 'spec.yml');
    await writeFile(specPath, specYaml, 'utf8');
    if (opts.composeYaml !== undefined) {
      await writeFile(join(dir, 'compose.yml'), opts.composeYaml, 'utf8');
    }

    const now = Date.now();
    const prev = await this.#readMeta(name).catch(() => null);
    const meta: SpecMeta = {
      name,
      kind,
      description: opts.description ?? prev?.description,
      createdAt: prev?.createdAt ?? now,
      updatedAt: now,
      requiredVars,
    };
    await writeFile(join(dir, 'meta.json'), JSON.stringify(meta, null, 2), 'utf8');
    return { ...meta, dir, specPath };
  }

  /**
   * Delete a named spec.
   *
   * Callers MUST check for referencing deployments first — a dangling reference would leave a
   * deployment that can never be torn down, since resolving it needs the spec. The API enforces it;
   * this layer stays mechanical.
   */
  async remove(name: string): Promise<void> {
    await rm(this.#dir(name), { recursive: true, force: true });
  }

  async #readMeta(name: string): Promise<SpecMeta> {
    const raw = await readFile(join(this.#dir(name), 'meta.json'), 'utf8');
    const m = JSON.parse(raw) as SpecMeta;
    // Tolerate a record written before requiredVars existed.
    return { ...m, requiredVars: m.requiredVars ?? [] };
  }
}
