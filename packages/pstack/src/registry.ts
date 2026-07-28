/**
 * The deployment registry — what turns pstack from a CLI into a control plane.
 *
 * The CLI acts on ONE spec file you point it at. The control plane instead holds MANY submitted
 * deployments and acts on them by id, so a host can serve several projects and many PRs at once.
 *
 * Storage is a directory tree, not a database:
 *
 *   $PSTACK_DATA/deployments/<id>/
 *     spec.yml       the submitted spec
 *     compose.yml    the submitted compose file, when one was sent with it
 *     meta.json      { id, kind, createdAt, updatedAt }
 *
 * Why no database: the registry is a cache of intent, never the source of truth about what exists.
 * Truth lives in Docker and in each axis's own `assert_*` probe — the same rule the CLI follows.
 * A lost registry means "I forgot what you asked for", not "the host is now inconsistent", and it
 * is recoverable by re-submitting. A directory of YAML is also greppable, diffable, and trivially
 * backed up, which a sqlite file is not.
 */

import { mkdir, readdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { loadSpec, type Kind, type Stack } from './spec.ts';

export type DeploymentMeta = {
  id: string;
  kind: Kind;
  createdAt: number;
  updatedAt: number;
  /**
   * The named spec this deployment uses, when it references one instead of carrying its own copy.
   * Undefined for an inline deployment.
   */
  specName?: string;
  /**
   * Variables STORED with the deployment.
   *
   * This is the fix for a genuine footgun: variables used to travel as `?query` params on every
   * call, so `up` with `PR=7` and a later `down` with `PR=8` (or none) tore down a DIFFERENT stack
   * and orphaned the first — the exact leak class this project exists to prevent. Storing them
   * makes the deployment self-describing: `down` resolves the same stack `up` created, always.
   *
   * Request-time variables still override these, so a one-off can still be forced, but nothing
   * REQUIRES the caller to remember.
   */
  vars?: Record<string, string>;
};

export type Deployment = DeploymentMeta & {
  dir: string;
  specPath: string;
};

/**
 * Ids become directory names and are echoed into shell hooks via the resolved stack, so restrict
 * them to an alphabet with no traversal, no whitespace and no shell metacharacters. Rejecting here
 * is the difference between a 400 and a path-traversal write.
 */
const ID = /^[a-z0-9][a-z0-9._-]{0,63}$/;

export class RegistryError extends Error {}

export function assertValidId(id: string): void {
  if (!ID.test(id) || id.includes('..')) {
    throw new RegistryError(
      `invalid deployment id "${id}" — must match ${ID} (lowercase, no traversal, no spaces)`,
    );
  }
}

export class Registry {
  readonly root: string;

  constructor(dataDir: string) {
    this.root = join(dataDir, 'deployments');
  }

  #dir(id: string): string {
    assertValidId(id);
    return join(this.root, id);
  }

  async list(): Promise<DeploymentMeta[]> {
    let entries: string[];
    try {
      entries = await readdir(this.root);
    } catch {
      return []; // nothing submitted yet
    }
    const out: DeploymentMeta[] = [];
    for (const id of entries) {
      const meta = await this.#readMeta(id).catch(() => null);
      if (meta) out.push(meta);
    }
    return out.sort((a, b) => b.updatedAt - a.updatedAt);
  }

  async get(id: string): Promise<Deployment | null> {
    const dir = this.#dir(id);
    try {
      await stat(join(dir, 'spec.yml'));
    } catch {
      return null;
    }
    const meta = await this.#readMeta(id);
    return { ...meta, dir, specPath: join(dir, 'spec.yml') };
  }

  /**
   * Create or replace a deployment.
   *
   * The spec is parsed before anything is written, so a malformed submission is rejected without
   * leaving a half-created deployment behind. `kind` is read from the parsed spec rather than
   * taken from the caller — the file is the authority.
   */
  async put(
    id: string,
    specYaml: string,
    opts: {
      composeYaml?: string;
      env?: Record<string, string | undefined>;
      /** Recorded so `down` resolves the same stack `up` created. See DeploymentMeta.vars. */
      vars?: Record<string, string>;
      /** Set when this deployment references a named spec rather than owning its copy. */
      specName?: string;
    } = {},
  ): Promise<Deployment> {
    const dir = this.#dir(id);
    // Read the existing record FIRST: validation below must see the merged variable set, or a
    // redeploy that supplies only `PR` fails on the `REGION` it was already created with — the
    // merge that preserves it happens further down, too late to help.
    const prev = await this.#readMeta(id).catch(() => null);
    const mergedVars = { ...(prev?.vars ?? {}), ...(opts.vars ?? {}) };
    await mkdir(dir, { recursive: true });

    const specPath = join(dir, 'spec.yml');
    await writeFile(specPath, specYaml, 'utf8');
    if (opts.composeYaml !== undefined) {
      await writeFile(join(dir, 'compose.yml'), opts.composeYaml, 'utf8');
    }

    let spec: Stack;
    try {
      // Validate against the caller's variables; relative paths in the spec resolve from `dir`,
      // which is why compose.yml is written alongside it.
      // Same precedence as `resolve`: process env < stored vars < request env. Omitting `vars`
      // here made `put` reject the exact variables it was about to store.
      spec = await loadSpec(specPath, { ...process.env, ...mergedVars, ...opts.env });
    } catch (err) {
      // Roll back rather than leave an unusable deployment on disk.
      await rm(dir, { recursive: true, force: true }).catch(() => {});
      throw new RegistryError(`spec rejected: ${(err as Error).message}`);
    }

    const now = Date.now();
    const meta: DeploymentMeta = {
      id,
      kind: spec.kind,
      createdAt: prev?.createdAt ?? now,
      updatedAt: now,
      specName: opts.specName ?? prev?.specName,
      // Already merged above, so validation and storage agree on exactly one variable set.
      vars: mergedVars,
    };
    await writeFile(join(dir, 'meta.json'), JSON.stringify(meta, null, 2), 'utf8');
    return { ...meta, dir, specPath };
  }

  /**
   * Forget a deployment. Does NOT tear anything down — removing the record while containers still
   * run would orphan them beyond the control plane's view, which is precisely the leak this
   * project exists to prevent. Callers must `down` first; the API enforces that.
   */
  async remove(id: string): Promise<void> {
    await rm(this.#dir(id), { recursive: true, force: true });
  }

  /** Resolve a stored deployment into a Stack, interpolating with the given variables. */
  async resolve(id: string, env: Record<string, string | undefined> = {}): Promise<Stack> {
    const dep = await this.get(id);
    if (!dep) throw new RegistryError(`no such deployment: ${id}`);
    // Precedence: process env < stored vars < request vars. Stored vars beat the ambient
    // environment so a stray PR=… in the server's own environment cannot hijack a deployment;
    // request vars still win so a one-off override is possible.
    return loadSpec(dep.specPath, { ...process.env, ...(dep.vars ?? {}), ...env });
  }

  async #readMeta(id: string): Promise<DeploymentMeta> {
    const raw = await readFile(join(this.#dir(id), 'meta.json'), 'utf8');
    return JSON.parse(raw) as DeploymentMeta;
  }
}

/** Where the control plane keeps its state. Overridable so tests never touch a real host. */
export function dataDir(): string {
  return process.env.PSTACK_DATA ?? '/var/lib/pstack';
}
