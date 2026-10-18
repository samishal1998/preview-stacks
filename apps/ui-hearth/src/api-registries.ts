import { call, id } from './api';

export type RegistryEntry = { registry: string; username: string | null; viaHelper: boolean };
export type RegistriesResponse = { dir: string; writable: boolean; entries: RegistryEntry[]; helpers: string[] };

export const registriesApi = {
  list: () => call<RegistriesResponse>('GET', '/api/registries'),
  put: (host: string, username: string, password: string) =>
    call<{ registry: string }>('PUT', `/api/registries/${id(host)}`, { username, password }),
  forget: (host: string) => call<unknown>('DELETE', `/api/registries/${id(host)}`),
};
