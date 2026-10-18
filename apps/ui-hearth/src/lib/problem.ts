/** A failed call as one short sentence. Never a bare status. */
export function problem(r: { status: number; body: { error?: string } }, what: string): string {
  if (r.status === 0) return r.body.error ?? 'the server did not answer';
  if (r.status === 401) return 'Signed out. Sign in again.';
  if (r.status === 403) return r.body.error ?? `${what}: not allowed.`;
  if (r.status === 404 && !r.body.error) return `${what}: not found.`;
  return r.body.error ? `${what}: ${r.body.error}` : `${what}: HTTP ${r.status}`;
}
