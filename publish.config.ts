/**
 * Release config for pstack, consumed by `@samyx/publish-kit`.
 *
 * NOTE — no `import { defineConfig } from '@samyx/publish-kit/config'`.
 * In `@samyx/publish-kit@0.0.0-pre.1` that subpath is unusable: the package's `files` ships
 * `dist/`, but its `exports` map still points at `./src/config/define.ts`, which is not in the
 * tarball — so the import fails with "Cannot find module .../src/config/define.ts". Since
 * `defineConfig` is an identity function (a types-only helper), a plain object is equivalent and
 * keeps working after the package is fixed. Restore the import once a release resolves `/config`
 * to `dist/`.
 *
 * This is a SINGLE package at the repo root, not a monorepo. Discovery expands workspace globs
 * from `package.json#workspaces` and then filters by `packages.include`, so the root package must
 * appear in BOTH — that is the only reason `workspaces: ["."]` exists in package.json.
 *
 * The package ships a BUNDLE, never the source tree. `pstack` installs globally, so shipping `src/`
 * would make every user pay TypeScript parsing per invocation, download docs/examples/skills they
 * did not ask for, and turn the internals into the public surface. `scripts/build.ts` emits
 * `dist/cli.js` + `dist/index.js` (~74 KB, `--target=bun`, minified, sourcemapped) with the UI and
 * the control-stack template inlined as text imports, so nothing resolves relative to source at
 * runtime.
 *
 * Not `--compile`: a standalone executable bakes in the Bun runtime (~60 MB) per platform, which for
 * npm means a 5-platform optionalDependencies matrix or a postinstall download. Bun is already a
 * requirement (`engines.bun`) and there is no Node fallback to preserve — Bun.serve/YAML/spawn/file
 * have no Node equivalent.
 */
export default {
  packageManager: 'bun',
  versioning: 'lockstep',

  /**
   * Single root package, selected BY NAME.
   *
   * `include` is matched against the package's relative dir OR its name. The root package's
   * relative dir is the empty string, so no path glob (`.`, `./`, `*`) can ever select it —
   * matching on the name is the only form that works for a root-level package.
   */
  packages: { include: ['@samyx/preview-stacks'] },

  dependencies: {
    // Zero runtime dependencies by design — Bun's stdlib covers YAML parsing and HTTP. Everything
    // in devDependencies is tooling and must not reach the shipped manifest.
    dropDevDependencies: true,
  },

  overrides: {
    // Never ship the release tooling's own surface, nor the workspaces shim above.
    dropFields: ['publishConfig', 'workspaces'],
  },

  // `bun scripts/build.ts`, also wired as `prepublishOnly` so a stale or missing bundle cannot be
  // published even if a release is cut by hand.
  build: { command: 'bun scripts/build.ts' },

  /** Re-check the registry after publishing rather than trusting the CLI's exit code. */
  verify: { enabled: true },

  // npm defaults a NEW scoped package to restricted, which makes a "successful" publish invisible
  // to everyone else.
  access: 'public',
} as const;
