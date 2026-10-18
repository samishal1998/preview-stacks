/** Pure rules for the Swarm page. */
export type JoinFormat = 'token' | 'command' | 'script' | 'cloud-config';
export const DISTROS = ['ubuntu', 'debian', 'fedora', 'suse', 'arch', 'alpine'];

export function joinPath(format: JoinFormat, distro: string): string {
  return `/api/swarm/join?format=${format}${format === 'cloud-config' ? `&distro=${encodeURIComponent(distro)}` : ''}`;
}

/** The server refuses any other removal (409): only a node that is down and drained goes. */
export const removable = (n: { state?: string; status?: string; availability: string; self?: boolean }): boolean =>
  !n.self && (n.state ?? n.status) === 'down' && n.availability === 'drain';

export function joinProblem(status: number, text: string): string {
  if (status === 403) return 'Maintainer only.';
  if (status === 409) return 'Not a swarm manager.';
  return text || `HTTP ${status}`;
}
