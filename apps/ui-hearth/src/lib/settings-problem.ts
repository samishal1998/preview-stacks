/** One sentence for a failed call: what the server said, or what the status means. Pure. */
export function problem(r: { status: number; body: { error?: string } }, what: string): string {
  if (r.status === 0) return r.body.error ?? 'The server did not answer.';
  if (r.status === 401) return 'Signed out. Sign in again.';
  if (r.status === 403) return `${what}: ${r.body.error ?? 'not allowed for this account.'}`;
  if (r.status === 404 && (!r.body.error || r.body.error === 'not found')) return `${what}: not supported by this server.`;
  return r.body.error ? `${what}: ${r.body.error}` : `${what}: HTTP ${r.status}`;
}
