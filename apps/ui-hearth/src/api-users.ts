/** Accounts, personal tokens and the signed-in identity. */
import type { User } from '@samyx/preview-stacks-client';
import { call } from './api';
import type { Role } from './lib/users';

export type Token = { id: number; name: string; createdAt: number; lastUsedAt: number | null };

export const usersApi = {
  list: () => call<{ users: User[] }>('GET', '/api/users'),
  create: (b: { username: string; password: string; role: Role }) => call<{ user: User }>('POST', '/api/users', b),
  remove: (uid: number) => call<unknown>('DELETE', `/api/users/${uid}`),
  setRole: (uid: number, role: string) => call<unknown>('PATCH', `/api/users/${uid}`, { role }),
  setPassword: (uid: number, password: string) => call<unknown>('PUT', `/api/users/${uid}/password`, { password }),
  tokens: () => call<{ tokens: Token[] }>('GET', '/api/tokens'),
  createToken: (name: string) => call<{ token: string; id: number }>('POST', '/api/tokens', { name }),
  removeToken: (tid: number) => call<unknown>('DELETE', `/api/tokens/${tid}`),
};
