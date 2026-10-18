/** The bearer token (PSTACK_TOKEN or a personal token), kept in this browser. Storage can throw; every access is guarded. */
export const TOKEN_KEY = 'pstack.hearth.token';

type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;
const def = (): Store | undefined => {
  try {
    return localStorage;
  } catch {
    return undefined;
  }
};

export function getToken(s: Store | undefined = def()): string {
  try {
    return s?.getItem(TOKEN_KEY) ?? '';
  } catch {
    return '';
  }
}
export function setToken(t: string, s: Store | undefined = def()): void {
  try {
    s?.setItem(TOKEN_KEY, t);
  } catch {
    /* blocked storage: the token lasts until reload */
  }
}
export function clearToken(s: Store | undefined = def()): void {
  try {
    s?.removeItem(TOKEN_KEY);
  } catch {
    /* nothing stored */
  }
}

/** The token described without printing it. */
export function tokenShape(t: string): string {
  if (t.startsWith('pstack_pat_')) return 'Personal token.';
  if (/^[0-9a-f]{40,}$/i.test(t)) return 'Machine token.';
  return `${t.length} characters, not a pstack token.`;
}
