/**
 * Pure rules for custom routes: when an assign is a move, its question, and the host list.
 *
 * Hearth keeps its own copy (apps/ui-hearth/src/lib/customRoutes.ts): the spec asks for one builder
 * per UI, and the two apps share no source.
 */

/** Who holds a route. `stack` is null when that deployment does not resolve. */
export type Holder = { deployment: string; stack: string | null };

/**
 * The holder to ask about before assigning to `target`, or null when a plain assign will do. Takes
 * the list row, or the body of a 409 from a non-force PUT (the list was stale). Only a 409 carrying
 * `assignedTo` is a move; a host or router collision is a 409 too, and is an error.
 */
export function needsMove(
  src: { deployment?: string | null; stack?: string | null } | { assignedTo?: Holder | null },
  target: string,
): Holder | null {
  const h =
    'assignedTo' in src
      ? src.assignedTo
      : 'deployment' in src && src.deployment
        ? { deployment: src.deployment, stack: src.stack ?? null }
        : null;
  return h && h.deployment !== target ? h : null;
}

export const moveQuestion = (name: string, from: Holder, toStack: string): string =>
  `Move ${name} from ${from.stack ?? from.deployment} to ${toStack}?`;

/** A `*.` wildcard host does not resolve on its own: text, not a link. */
export const hostItems = (hosts: string[]): Array<{ host: string; href: string | null }> =>
  hosts.map((host) => ({ host, href: host.startsWith('*.') ? null : `https://${host}` }));

/**
 * Focus a write dropped: on <body>, on a control it disabled, or on an element with no box: removed,
 * or a closed dialog's button, where Firefox leaves focus for a frame before it falls to <body>.
 */
export const focusLost = (a: Element | null, body: Element | null): boolean =>
  !a || a === body || a.matches(':disabled') || !a.getClientRects().length;
