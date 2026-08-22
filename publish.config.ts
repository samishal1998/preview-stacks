/**
 * Release config, consumed by `@samyx/publish-kit`: the two npm packages (the advanced UI and the
 * client SDK). `@samyx/preview-stacks` — the control plane — is a Go binary released on GitHub by
 * GoReleaser (.goreleaser.yaml) and is no longer published to npm; its package.json stays as the
 * lockstep version of record (private, so publish-kit bumps it and never publishes it).
 *
 * NOTE — no `import { defineConfig } from '@samyx/publish-kit/config'`.
 * In `@samyx/publish-kit@0.0.0-pre.1` that subpath is unusable: the package's `files` ships
 * `dist/`, but its `exports` map still points at `./src/config/define.ts`, which is not in the
 * tarball. `defineConfig` is an identity function (a types-only helper), so a plain object is
 * equivalent. Restore the import once a release resolves `/config` to `dist/`.
 */
export default {
  packageManager: 'bun',
  versioning: 'lockstep',

  /**
   * The publishable packages, selected BY NAME: the list reads as what gets published, not where
   * it happens to live.
   *
   * The UI is a genuinely separate release: `pstack build-image --ui` fetches it from npm inside
   * the image build, so a user opting into the advanced UI never installs it on the host.
   */
  packages: {
    include: [
      '@samyx/preview-stacks-ui',
      // The API client. Its own package because the people who want it — a CI script calling the
      // control plane — must not have to install a control plane to get it.
      '@samyx/preview-stacks-client',
    ],
  },

  dependencies: {
    // Everything in devDependencies is tooling and must not reach the shipped manifest.
    dropDevDependencies: true,
  },

  overrides: {
    // Never ship the release tooling's own surface, nor the workspaces shim above.
    dropFields: ['publishConfig', 'workspaces'],
  },

  // Each package builds itself via its own `prepublishOnly`, so nothing here needs to know that
  // one is a Vite app and the other a bun bundle.
  build: { skip: true },

  /** Re-check the registry after publishing rather than trusting the CLI's exit code. */
  verify: { enabled: true },

  // npm defaults a NEW scoped package to restricted, which makes a "successful" publish invisible
  // to everyone else.
  access: 'public',
} as const;
