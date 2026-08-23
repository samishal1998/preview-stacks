/**
 * Private registry credentials for the control plane.
 *
 * ── WHY THE HOST BEING LOGGED IN IS NOT ENOUGH ───────────────────────────────────────────────────
 *
 * An image pull is authenticated by the **client**, not the daemon. `docker pull` reads the client's
 * own `config.json`, finds the entry for that registry, and sends it to the daemon in an
 * `X-Registry-Auth` header with the create-image call. The daemon never consults the client's config —
 * it just uses whatever credential it was handed.
 *
 * pstack shells out to `docker compose` from **inside the control container**, so the client is the
 * docker CLI in that container, and its `config.json` is the one that matters. A `docker login` run on
 * the host writes `/root/.docker/config.json` on the *host*, which that container cannot see. The
 * result is `pull access denied` for a private image on a host that is demonstrably logged in — and
 * nothing in the error points at the reason.
 *
 * So the control stack mounts a `DOCKER_CONFIG` directory, and this module manages the file in it.
 *
 * ── ON DEMAND, WITH NO RESTART ───────────────────────────────────────────────────────────────────
 *
 * The CLI reads `config.json` on **every** invocation, so a credential added now applies to the next
 * pull. Nothing needs recreating, and there is no cache to bust. Two ways in, both landing in the same
 * file:
 *
 *   - from the host:  `docker login --config /var/lib/pstack/control/docker <registry>`
 *   - from the API:   `PUT /api/registries/:host  { username, password }`
 *
 * ── `auths` IS NOT ENCRYPTION ────────────────────────────────────────────────────────────────────
 *
 * A config.json entry is `base64("user:password")` — trivially reversible, which is why this module
 * NEVER returns it. `list()` gives hostnames and usernames only; there is no read path for the secret
 * and no route that exposes one. The file is written 0600, atomically, and the token that can write
 * here already commands a read-write Docker socket, so the boundary that matters is the API's, not
 * this file's.
 *
 * ── CREDENTIAL HELPERS DO NOT TRANSPLANT ─────────────────────────────────────────────────────────
 *
 * Copying a laptop's `config.json` in is a trap: on macOS and Docker Desktop it usually contains
 * `credsStore: "desktop"` or `"osxkeychain"` and **no** `auths` at all, because the secrets live in the
 * OS keychain. Inside this container that helper binary does not exist, so every pull fails with
 * `error getting credentials`. `inspect()` reports helpers so the UI can say that rather than let
 * someone debug an empty `auths`.
 */

import { chmod, mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

export class RegistryAuthError extends Error {}

/**
 * Docker Hub's canonical key. `docker login docker.io` writes THIS, not `docker.io` — so a credential
 * stored under the friendly name is silently never used for `nginx:alpine`.
 */
export const DOCKER_HUB_KEY = 'https://index.docker.io/v1/';

const HUB_ALIASES = new Set([
  'docker.io',
  'index.docker.io',
  'registry-1.docker.io',
  'https://docker.io',
  'https://index.docker.io',
  DOCKER_HUB_KEY,
]);

/** A registry host, as docker's config keys them. Hub's aliases collapse to the canonical key. */
export function normalizeRegistry(input: string): string {
  const trimmed = input.trim();
  if (!trimmed) throw new RegistryAuthError('a registry host is required');
  const bare = trimmed.replace(/\/+$/, '');
  if (HUB_ALIASES.has(bare) || HUB_ALIASES.has(bare.toLowerCase())) return DOCKER_HUB_KEY;
  // A host, optionally with a port, optionally with a scheme docker tolerates. No path: a config key
  // is a registry, not a URL to an image.
  if (!/^(https?:\/\/)?[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:\d{1,5})?$/i.test(bare)) {
    throw new RegistryAuthError(
      `"${input}" does not look like a registry host. Use e.g. ghcr.io, registry.example.com:5000, ` +
        `or docker.io for Docker Hub.`,
    );
  }
  return bare;
}

/** What a caller may know about a stored credential. Never the secret. */
export type RegistryEntry = {
  registry: string;
  /** Decoded from the stored `auth`, because knowing *which account* is the point of a list view. */
  username: string | null;
  /** True when this entry is served by a credential helper rather than a stored secret. */
  viaHelper: boolean;
};

export type RegistryState = {
  /** The DOCKER_CONFIG directory, as this process sees it. */
  dir: string;
  /** False on a control stack that predates the mount — the fix is `pstack init`. */
  writable: boolean;
  entries: RegistryEntry[];
  /**
   * `credsStore` / `credHelpers` found in the file. Present here means "these will not work in this
   * container", which is a different problem from having no credentials at all.
   */
  helpers: string[];
};

type DockerConfig = {
  auths?: Record<string, { auth?: string; username?: string; password?: string } | undefined>;
  credsStore?: string;
  credHelpers?: Record<string, string>;
  [k: string]: unknown;
};

export class RegistryAuthStore {
  readonly dir: string;

  constructor(dir: string) {
    this.dir = dir;
  }

  get #path(): string {
    return join(this.dir, 'config.json');
  }

  async #read(): Promise<DockerConfig> {
    try {
      const raw = await readFile(this.#path, 'utf8');
      const parsed = JSON.parse(raw) as unknown;
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
      return parsed as DockerConfig;
    } catch {
      // Absent or unparseable both mean "no usable credentials". Refusing to start over a corrupt file
      // would leave no way to fix it from the API.
      return {};
    }
  }

  /** True when this process can write the config — probed by writing, so a `:ro` mount is caught. */
  async writable(): Promise<boolean> {
    try {
      await mkdir(this.dir, { recursive: true });
      const probe = join(this.dir, `.pstack-probe-${process.pid}`);
      await writeFile(probe, '', 'utf8');
      await rm(probe, { force: true });
      return true;
    } catch {
      return false;
    }
  }

  async state(): Promise<RegistryState> {
    const cfg = await this.#read();
    const helperFor = (registry: string): boolean =>
      !!cfg.credsStore || !!(cfg.credHelpers && cfg.credHelpers[registry]);

    const entries: RegistryEntry[] = Object.entries(cfg.auths ?? {}).map(([registry, v]) => ({
      registry,
      username: decodeUsername(v?.auth) ?? v?.username ?? null,
      viaHelper: helperFor(registry),
    }));

    const helpers = [
      ...(cfg.credsStore ? [`credsStore: ${cfg.credsStore}`] : []),
      ...Object.entries(cfg.credHelpers ?? {}).map(([r, h]) => `credHelpers[${r}]: ${h}`),
    ];

    return {
      dir: this.dir,
      writable: await this.writable(),
      entries: entries.sort((a, b) => a.registry.localeCompare(b.registry)),
      helpers,
    };
  }

  /**
   * Store one credential, preserving everything else in the file.
   *
   * Written atomically for the same reason Traefik's config is: `docker compose` may read this file at
   * any moment, and a truncated-then-filled config is a parse error that looks like having no
   * credentials at all.
   */
  async put(registryInput: string, username: string, password: string): Promise<string> {
    const registry = normalizeRegistry(registryInput);
    if (!username.trim()) throw new RegistryAuthError('a username is required');
    if (!password) throw new RegistryAuthError('a password or token is required');
    if (!(await this.writable())) {
      throw new RegistryAuthError(
        `the registry credential directory is not writable from here (${this.dir}). The control stack ` +
          `mounts it into the API from 0.7.0 onward — re-run \`pstack init\` on the host.`,
      );
    }

    const cfg = await this.#read();
    cfg.auths = { ...(cfg.auths ?? {}) };
    // `auth` only — never `username`/`password` as separate plaintext fields, which docker also accepts
    // but which puts the secret in the file twice.
    cfg.auths[registry] = { auth: Buffer.from(`${username}:${password}`, 'utf8').toString('base64') };
    await this.#write(cfg);
    return registry;
  }

  /** Remove one. Returns false when it was not there, so a caller can answer 404 rather than lie. */
  async remove(registryInput: string): Promise<boolean> {
    const registry = normalizeRegistry(registryInput);
    if (!(await this.writable())) {
      throw new RegistryAuthError(`the registry credential directory is not writable from here (${this.dir}).`);
    }
    const cfg = await this.#read();
    if (!cfg.auths || !(registry in cfg.auths)) return false;
    delete cfg.auths[registry];
    await this.#write(cfg);
    return true;
  }

  async #write(cfg: DockerConfig): Promise<void> {
    await mkdir(this.dir, { recursive: true });
    const tmp = join(this.dir, `.pstack-tmp-${process.pid}.json`);
    try {
      await writeFile(tmp, `${JSON.stringify(cfg, null, 2)}\n`, 'utf8');
      // Before the rename, so the file is never briefly world-readable at its real name.
      await chmod(tmp, 0o600);
      await rename(tmp, this.#path);
      await chmod(this.dir, 0o700).catch(() => {});
    } catch (err) {
      await rm(tmp, { force: true }).catch(() => {});
      throw new RegistryAuthError(`could not write the registry credentials: ${(err as Error).message}`);
    }
  }
}

/**
 * The username half of a stored `auth`, and only that half.
 *
 * Exported for testing. A caller wanting the password should not find a helper here that returns it —
 * there is deliberately no such function in this module.
 */
export function decodeUsername(auth: string | undefined): string | null {
  if (!auth) return null;
  try {
    const decoded = Buffer.from(auth, 'base64').toString('utf8');
    const colon = decoded.indexOf(':');
    return colon === -1 ? null : decoded.slice(0, colon) || null;
  } catch {
    return null;
  }
}
