/**
 * `pstack cloud-init` — fill the cloud-config template and print a ready-to-boot user-data file.
 *
 * The template is embedded at build time (like the control-stack compose file), so this works from
 * a global install with no checkout — the same property `build-image` needed.
 *
 * Placeholders are `{{NAME}}`, deliberately NOT `${NAME}`: the template contains real shell that
 * must survive rendering untouched (`$(dpkg --print-architecture)`, `${STACK}`, `${DOMAIN}` for
 * Compose). Using the same syntax for both would mean the generator eating lines meant for bash.
 *
 * `{{...}}` does collide with one other thing in the file — Docker's Go templates, e.g.
 * `docker inspect -f '{{.State.Health.Status}}'`. They coexist because the pattern here is
 * `[A-Z_]+` only: a Go template carries dots and lower case, so neither the substitution nor the
 * leftover-check below can touch it. Keep placeholder names SHOUT_CASE and that stays true.
 */

import CLOUD_INIT_TEMPLATE from '../templates/cloud-init.tpl.yaml' with { type: 'text' };
import pkg from '../package.json';

/**
 * The distros the generator can target.
 *
 * What actually differs is smaller than it looks: how Docker is installed and how its service is
 * enabled. Everything else — cloud-init's own `users`/`groups`/`packages` stages, the Bun installer,
 * pstack, the compose files — is identical across them. So a distro is a table row, not a template.
 *
 * Docker sources, and why they differ:
 *   - ubuntu/debian/fedora: Docker's OWN repositories, which is the vendor-recommended path and what
 *     the original Ubuntu-only template already did. Fedora avoids `dnf config-manager` on purpose —
 *     its syntax CHANGED between dnf4 and dnf5 (Fedora ≥41), and downloading the .repo file directly
 *     sidesteps the divergence entirely.
 *   - suse/arch/alpine: Docker publishes no repository for these; the distro's own packages are the
 *     supported path. Their compose package may ship a standalone `docker-compose` binary rather than
 *     the CLI plugin, which is why the template carries a plugin-symlink fallback after this block.
 *
 * Alpine is the odd one out twice: OpenRC instead of systemd, and no bash in the base image — the
 * template's own runcmd lines use `bash -c`, so bash (and sudo, for the `preview` user's sudoers
 * entry) ride in through EXTRA_PACKAGES.
 */
export const DISTROS = ['ubuntu', 'debian', 'fedora', 'suse', 'arch', 'alpine'] as const;
export type Distro = (typeof DISTROS)[number];

type DistroProfile = {
  /** Example image name for the header's `hcloud server create` line. */
  imageHint: string;
  /** Extra entries for cloud-init's `packages:` stage. */
  extraPackages: string[];
  /** runcmd fragment installing docker + compose. Complete YAML list items, 2-space indented. */
  pkgSetup: string;
  /** runcmd fragment enabling + starting the docker service. */
  dockerEnable: string;
  /** Rendered into the file header when the target needs a warning. */
  note?: string;
};

const APT_SETUP = (repo: 'ubuntu' | 'debian'): string =>
  [
    '  - install -m 0755 -d /etc/apt/keyrings',
    `  - curl -fsSL https://download.docker.com/linux/${repo}/gpg -o /etc/apt/keyrings/docker.asc`,
    '  - chmod a+r /etc/apt/keyrings/docker.asc',
    '  - >',
    '    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc]',
    `    https://download.docker.com/linux/${repo} $(. /etc/os-release && echo "$VERSION_CODENAME") stable"`,
    '    > /etc/apt/sources.list.d/docker.list',
    '  - apt-get update',
    '  - DEBIAN_FRONTEND=noninteractive apt-get install -y docker-ce docker-ce-cli containerd.io',
    '      docker-buildx-plugin docker-compose-plugin',
  ].join('\n');

const SYSTEMD_ENABLE = '  - systemctl enable --now docker';

const DISTRO_PROFILES: Record<Distro, DistroProfile> = {
  ubuntu: {
    imageHint: 'ubuntu-24.04',
    extraPackages: [],
    pkgSetup: APT_SETUP('ubuntu'),
    dockerEnable: SYSTEMD_ENABLE,
  },
  debian: {
    imageHint: 'debian-12',
    extraPackages: [],
    pkgSetup: APT_SETUP('debian'),
    dockerEnable: SYSTEMD_ENABLE,
  },
  fedora: {
    imageHint: 'fedora-40',
    extraPackages: [],
    // The .repo file straight into yum.repos.d — NOT `dnf config-manager`, whose flag syntax changed
    // between dnf4 and dnf5. A curl works identically on both.
    pkgSetup: [
      '  - curl -fsSL https://download.docker.com/linux/fedora/docker-ce.repo -o /etc/yum.repos.d/docker-ce.repo',
      '  - dnf -y install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin',
    ].join('\n'),
    dockerEnable: SYSTEMD_ENABLE,
  },
  suse: {
    imageHint: 'opensuse-leap-15.6',
    extraPackages: [],
    // Docker publishes no SUSE repository; the distro packages are the supported path.
    pkgSetup: '  - zypper --non-interactive install docker docker-compose docker-buildx',
    dockerEnable: SYSTEMD_ENABLE,
    note: 'openSUSE installs Docker from the distro repositories (Docker publishes none for SUSE).',
  },
  arch: {
    imageHint: 'arch-linux (cloud image)',
    extraPackages: ['sudo'],
    // -Syu, not -Sy: a partial sync followed by an install is the classic Arch breakage.
    pkgSetup: '  - pacman -Syu --noconfirm docker docker-compose docker-buildx',
    dockerEnable: SYSTEMD_ENABLE,
    note:
      'Arch cloud images vary in cloud-init support — verify yours ships cloud-init before booting ' +
      'with this file. Docker comes from the distro repositories.',
  },
  alpine: {
    imageHint: 'alpine-3.20 (cloud image)',
    // The template runs `bash -c` in several runcmd lines and grants the preview user sudo; neither
    // ships in the Alpine base image.
    extraPackages: ['bash', 'sudo'],
    pkgSetup: '  - apk add --no-cache docker docker-cli-compose docker-cli-buildx',
    // OpenRC, not systemd.
    dockerEnable: ['  - rc-update add docker boot', '  - service docker start'].join('\n'),
    note:
      'Alpine: OpenRC (not systemd), musl (the Bun installer detects and installs its musl build), ' +
      'and cloud-init support varies by image — verify yours ships it. bash and sudo are added to ' +
      'the package list because this file depends on both.',
  },
};

export type CloudInitAnswers = {
  domain: string;
  acmeEmail: string;
  /** Optional: most providers inject their own key at boot (hcloud --ssh-key). */
  sshKey?: string;
  dashboardPassword: string;
  challenge: 'http01' | 'dns01';
  dnsProvider?: string;
  ui: 'basic' | 'advanced';
  /** Optional git URL cloned to /opt/preview/config for driving the CLI from the host. */
  configRepo?: string;
  /** Target distro; decides the Docker install/enable fragments. Default ubuntu. */
  distro?: Distro;
};

export class CloudInitError extends Error {}

/** Hex, because a `$` in a password would break the single-quoted shell that hashes it. */
export function randomPassword(bytes = 12): string {
  const b = new Uint8Array(bytes);
  crypto.getRandomValues(b);
  return [...b].map((n) => n.toString(16).padStart(2, '0')).join('');
}

function validate(a: CloudInitAnswers): void {
  if (!/^[a-z0-9.-]+\.[a-z]{2,}$/i.test(a.domain)) {
    throw new CloudInitError(`domain "${a.domain}" does not look like a hostname`);
  }
  if (!/^[^@\s]+@[^@\s]+\.[a-z]{2,}$/i.test(a.acmeEmail)) {
    throw new CloudInitError(`acme email "${a.acmeEmail}" does not look like an address`);
  }
  // Optional, because a provider usually injects one already (`hcloud server create --ssh-key`) and
  // demanding a second copy just to render a file is friction. But if one IS given it is checked:
  // a malformed key produces a booted host you cannot log into, which is the one failure here with
  // no cheap recovery.
  if (a.sshKey && !/^(ssh-(rsa|ed25519)|ecdsa-sha2-\S+) \S+/.test(a.sshKey)) {
    throw new CloudInitError(
      'ssh key must be an authorized_keys line, e.g. "ssh-ed25519 AAAA… you@laptop"',
    );
  }
  if (a.challenge === 'dns01' && !a.dnsProvider) {
    throw new CloudInitError('dns01 needs a provider code (see https://go-acme.github.io/lego/dns/)');
  }
  if (/'/.test(a.dashboardPassword)) {
    // It is interpolated into a single-quoted shell command in the template.
    throw new CloudInitError("dashboard password must not contain a single quote");
  }
}

/** Render the template. Pure: no prompting, no filesystem — so it is trivially testable. */
export function renderCloudInit(a: CloudInitAnswers): string {
  validate(a);

  const distro = a.distro ?? 'ubuntu';
  if (!DISTROS.includes(distro)) {
    throw new CloudInitError(`unknown distro "${distro}" — expected one of ${DISTROS.join(', ')}`);
  }
  const profile = DISTRO_PROFILES[distro];

  const initFlags: string[] = [];
  if (a.challenge === 'dns01') {
    initFlags.push(`--challenge dns01`, `--dns-provider ${a.dnsProvider}`);
  }
  if (a.ui === 'advanced') initFlags.push('--ui advanced');

  const values: Record<string, string> = {
    DOMAIN: a.domain,
    // The fallback router's rule is a Go regexp inside YAML, so every dot must be escaped or it
    // would match `backend-pr-1Xpreview.example.com` too.
    DOMAIN_RE: a.domain.replace(/\./g, '\\\\.'),
    ACME_EMAIL: a.acmeEmail,
    // Omit the key list entirely rather than emit an empty one: cloud-init would accept
    // `ssh_authorized_keys:` with nothing under it, and the result is a user with no way in and no
    // error to explain it. With the block absent, the provider's own injected key is the only one,
    // which is the normal case.
    SSH_BLOCK: a.sshKey
      ? `    ssh_authorized_keys:\n      - ${a.sshKey}\n`
      : '    # No key here: the provider injects its own at boot (e.g. `hcloud server create --ssh-key`).\n',
    DASHBOARD_PASSWORD: a.dashboardPassword,
    // Continuation lines: the init call is a YAML folded scalar, so each flag goes on its own line
    // at the same indentation.
    INIT_EXTRA_FLAGS: initFlags.length ? '\n    ' + initFlags.join('\n    ') : '',
    // The advanced UI needs its own image built before `init --ui advanced` can find it. Only the
    // build — the UI package is a build-time input fetched INSIDE the image, so installing it on
    // the host as well was gratuitous, and on a host where `bun install -g` fails it turned an
    // optional UI into a boot failure.
    UI_IMAGE_STEP:
      a.ui === 'advanced'
        ? '\n  # The advanced UI is opt-in and needs its own image.\n' +
          '  - /usr/local/bin/pstack build-image --ui'
        : '',
    CONFIG_REPO: a.configRepo ?? '',
    IMAGE_HINT: profile.imageHint,
    // A caveat the operator must read BEFORE booting belongs in the artifact itself, not in the
    // terminal output that scrolled away — this file gets saved and reused.
    DISTRO_NOTE: profile.note
      ? `\n#\n# ── DISTRO NOTE (${distro}) ─────────────────────────────────────────────────────────────────\n` +
        profile.note
          .match(/.{1,92}(\s|$)/g)!
          .map((line) => `# ${line.trim()}`)
          .join('\n')
      : '',
    EXTRA_PACKAGES: profile.extraPackages.length
      ? '\n' + profile.extraPackages.map((pkgName) => `  - ${pkgName}`).join('\n')
      : '',
    PKG_SETUP: profile.pkgSetup,
    DOCKER_ENABLE: profile.dockerEnable,
    // THE PINS — the point of this generator knowing versions at all. Stamped from the running
    // CLI (same pattern as the generated Dockerfiles in image.ts), so the file reproduces the
    // toolchain that rendered it rather than whatever is latest on the day someone reuses it.
    PSTACK_VERSION: pkg.version,
    BUN_VERSION: Bun.version,
  };

  let out = CLOUD_INIT_TEMPLATE as unknown as string;
  for (const [k, v] of Object.entries(values)) {
    out = out.split(`{{${k}}}`).join(v);
  }

  // No config repo: drop the clone line AND the comment that introduces it. Emitting
  // `git clone  /opt/preview/config` would fail and abort the rest of cloud-init; leaving the
  // comment behind would describe a step that is not there.
  if (!a.configRepo) {
    const lines = out.split('\n');
    const idx = lines.findIndex((l) => l.includes('git clone --depth 1  /opt/preview/config'));
    if (idx !== -1) {
      let from = idx;
      while (from > 0 && lines[from - 1]!.trimStart().startsWith('#')) from--;
      lines.splice(from, idx - from + 1);
      out = lines.join('\n');
    }
  }

  const missed = out.match(/\{\{[A-Z_]+\}\}/g);
  if (missed) {
    // Fail rather than hand over a file that boots most of the way and then breaks.
    throw new CloudInitError(`template left unrendered placeholders: ${[...new Set(missed)].join(', ')}`);
  }
  return out;
}

/**
 * Ask for anything not already supplied.
 *
 * `prompt` returns null on EOF, so a piped/CI invocation with missing answers fails loudly here
 * instead of silently rendering an empty domain.
 */
export function ask(label: string, fallback?: string): string {
  const suffix = fallback ? ` [${fallback}]` : '';
  const answer = prompt(`${label}${suffix}:`);
  if (answer === null) {
    throw new CloudInitError(
      `no answer for "${label}" (stdin closed). Pass it as a flag for non-interactive use.`,
    );
  }
  const v = answer.trim() || fallback || '';
  if (!v) throw new CloudInitError(`"${label}" is required`);
  return v;
}

/**
 * Ask for something the answer may legitimately be "nothing".
 *
 * Separate from `ask` because EOF must mean "skip it", not an error: a piped or CI invocation that
 * runs out of input should still produce a file, and an OPTIONAL field is exactly the one place
 * where no answer is a valid answer.
 */
export function askOptional(label: string): string {
  const answer = prompt(`${label} (blank to skip):`);
  return (answer ?? '').trim();
}

