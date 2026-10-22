/** Pure rules for the Swarm page. */
export type JoinFormat = 'token' | 'command' | 'script' | 'cloud-config';
export const DISTROS = ['ubuntu', 'debian', 'fedora', 'suse', 'arch', 'alpine'];

export function joinPath(format: JoinFormat, distro: string): string {
  return `/api/swarm/join?format=${format}${format === 'cloud-config' ? `&distro=${encodeURIComponent(distro)}` : ''}`;
}

/** The server refuses any other removal (409): only a node that is down and drained goes. */
export const removable = (n: { state?: string; status?: string; availability: string; self?: boolean }): boolean =>
  !n.self && (n.state ?? n.status) === 'down' && n.availability === 'drain';

type Placed = { role: string; status: string; availability: string; self?: boolean };

/** The manager this API runs on. */
export const thisManager = <T extends Placed>(nodes: T[]): T | undefined => nodes.find((n) => n.self && n.role === 'manager');

/** Drained manager: it keeps the control stack and every preview lands on a worker. */
export const workersOnly = (nodes: Placed[]): boolean => thisManager(nodes)?.availability === 'drain';

/** A node besides this one that swarm can place a preview on. */
export const hasReadyWorker = (nodes: Placed[]): boolean =>
  nodes.some((n) => !n.self && n.status === 'ready' && n.availability === 'active');

export function joinProblem(status: number, text: string): string {
  if (status === 403) return 'Maintainer only.';
  if (status === 409) return 'Not a swarm manager.';
  return text || `HTTP ${status}`;
}
