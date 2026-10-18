/** Pure rules for the spec editor and the stored-spec pages. No Vue, no DOM. */
import { recordFromPairs, type VarPair } from './vars';

/** Same shape the server accepts for a deployment id and a stored-spec name. */
const NAME = /^[a-z0-9][a-z0-9._-]{0,63}$/;
export const validName = (s: string): boolean => NAME.test(s) && !s.includes('..');

export type SubmitForm = { source: 'inline' | 'stored'; spec: string; compose: string; specName: string };

/** Why Submit is blocked, or '' when it is not. */
export function blocker(id: string, f: SubmitForm): string {
  if (!id) return 'Enter an id.';
  if (!validName(id)) return 'Id: lower case letters, digits, . _ - (max 64).';
  if (f.source === 'stored') return f.specName ? '' : 'Choose a spec.';
  return f.spec.trim() ? '' : 'Enter a spec.';
}

/**
 * `vars` are stored with the deployment on current servers; `env` validates this submission on
 * every server. Sending both keeps the form working against either build.
 */
export function submitBody(f: SubmitForm, vars: VarPair[]): Record<string, unknown> {
  const rec = recordFromPairs(vars);
  const body: Record<string, unknown> = { vars: rec, env: rec };
  if (f.source === 'stored') body.specName = f.specName;
  else {
    body.spec = f.spec;
    if (f.compose.trim()) body.compose = f.compose;
  }
  return body;
}

/** The preview domain, guessed from the page host: previews live beside the UI, one label up. */
export function previewDomain(host: string): string {
  if (host === 'localhost' || host.endsWith('.localhost') || /^[\d.]+$/.test(host) || host.includes(':')) return 'preview.example.com';
  const labels = host.split('.');
  return labels.length >= 3 ? labels.slice(1).join('.') : host;
}

export const exampleSpec = (domain: string): string => `version: 1
kind: isolated

# The stack identity: the compose project name, and $STACK inside every hook.
# \${PR} is interpolated once, at resolve time, from the variables sent with the request.
stack: pr-\${PR}

env:
  # The base every generated hostname hangs off: the app is served at app-pr-\${PR}.${domain}
  PREVIEW_DOMAIN: ${domain}

compose:
  file: compose.yml
  # Every service sits behind a profile so a bare \`up\` starts nothing.
  profiles: [app]

requires:
  - name: traefik-network
    assert: docker network inspect traefik >/dev/null 2>&1
    hint: run \`pstack init\` on the host first

axes:
  # Hooks run from the deployment directory, which holds only spec.yml and compose.yml:
  # use inline shell or absolute paths.
  - name: scratch
    up: mkdir -p /tmp/$STACK
    assert_live: test -d /tmp/$STACK
    down: rm -rf /tmp/$STACK
    # assert_gone must fail closed: a bare \`! <probe>\` reports "gone" whenever the probe itself fails.
    assert_gone: |
      test -d /tmp || exit 1
      ! test -d /tmp/$STACK
`;

export const EXAMPLE_COMPOSE = `services:
  app:
    image: nginx:alpine
    profiles: [app]
    labels:
      # The one label routing needs. Any traefik.* label opts the service out of generated routing.
      - pstack.routing.port=80
`;

/** Dropped files: a name containing "compose" is the compose file, anything else is the spec. */
export function routeFiles(files: Array<{ name: string; text: string }>): { spec?: string; compose?: string } {
  const out: { spec?: string; compose?: string } = {};
  for (const f of files) out[/compose/i.test(f.name) ? 'compose' : 'spec'] = f.text;
  return out;
}

/** Variables saved on a deployment row (older clients' types do not declare them). */
export const rowVars = (row: unknown): Record<string, string> | undefined => (row as { vars?: Record<string, string> } | undefined)?.vars;

/** Ids of deployments that reference a stored spec. */
export const usersOf = (deployments: Array<{ id: string; specName?: string | null }>, name: string): string[] =>
  deployments.filter((d) => d.specName === name).map((d) => d.id);

export function filterSpecs<T extends { name: string; description?: string }>(specs: T[], q: string): T[] {
  const n = q.trim().toLowerCase();
  return n ? specs.filter((s) => s.name.toLowerCase().includes(n) || (s.description ?? '').toLowerCase().includes(n)) : specs;
}
