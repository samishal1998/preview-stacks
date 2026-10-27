/**
 * Grafana Explore links over the Loki streams pstack writes. A stream carries one label,
 * `service_name=<stack>-<service>`, so the finest link is per service: swarm replicas share it.
 *
 * The advanced UI keeps its own copy (apps/ui/src/composables/grafana.ts): the spec asks for one
 * builder per UI, and the two apps share no source.
 */

const regex = (s: string): string => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
/** A LogQL double-quoted string: its backslashes and quotes escaped. */
const quote = (s: string): string => `"${s.replace(/[\\"]/g, '\\$&')}"`;

/** Every service of one stack. Loki's `=~` is anchored, so stack `a` never matches `a-b`'s streams. */
export const stackQuery = (stack: string, services: string[]): string =>
  `{service_name=~${quote(`${regex(stack)}-(${[...new Set(services)].map(regex).join('|')})`)}}`;

export const serviceQuery = (stack: string, service: string): string => `{service_name=${quote(`${stack}-${service}`)}}`;

/** Explore on the provisioned Loki datasource (uid `loki`), from `from` to now. */
export function exploreUrl(base: string, query: string, from = 'now-1h'): string {
  const panes = { a: { datasource: 'loki', queries: [{ refId: 'A', expr: query, datasource: { type: 'loki', uid: 'loki' } }], range: { from, to: 'now' } } };
  return `${base.replace(/\/+$/, '')}/explore?${new URLSearchParams({ schemaVersion: '1', panes: JSON.stringify(panes), orgId: '1' })}`;
}

/** The sidebar's gate: Grafana runs, developer and up (it reads unredacted lines), and a session, not a stored token. */
export const showGrafana = (grafana: string | null | undefined, developer: boolean, tokenOnly: boolean): grafana is string =>
  !!grafana && developer && !tokenOnly;
