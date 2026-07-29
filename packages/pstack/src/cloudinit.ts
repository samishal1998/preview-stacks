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

