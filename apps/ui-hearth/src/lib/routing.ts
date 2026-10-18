/** Pure rules for the Routing page. */
import type { RuntimeRoute } from '@samyx/preview-stacks-client';

// Mirrors the server (routing.go nameRe): lowercase, .yml/.yaml, no separators, no "..".
const NAME = /^[a-z0-9][a-z0-9._-]*\.ya?ml$/;

/** Empty string when the name is fine, else a short reason. */
export function fileNameError(name: string): string {
  if (!name) return 'Name the file.';
  if (!NAME.test(name) || name.includes('..')) return 'Lowercase, ending in .yml or .yaml.';
  return '';
}

export const STARTER = `http:
  middlewares:
    ip-allow:
      ipAllowList:
        sourceRange:
          - 203.0.113.0/24
`;

/** A rule can match a pattern rather than a host; those are not links. */
export const isPattern = (host: string): boolean => host.startsWith('(pattern)');

export type Target = { text: string; tone: 'neutral' | 'warning' | 'danger' | 'info' };

/** The "Forwards to" cell: the address, or the one reason there is none. */
export function targetOf(r: Pick<RuntimeRoute, 'target' | 'targetReason'>): Target {
  if (r.target) return { text: r.target, tone: 'neutral' };
  if (r.targetReason === 'no-port') return { text: 'no port declared', tone: 'warning' };
  if (r.targetReason === 'not-on-ingress') return { text: 'not on the ingress network', tone: 'danger' };
  if (r.targetReason === 'unknown-node') return { text: 'address not known from this node', tone: 'info' };
  return { text: 'not known', tone: 'neutral' };
}
