import type { DeliveryRow, NotifierRow } from '@samyx/preview-stacks-client';
import { call } from './api';
import type { NotifierType } from './lib/notifiers';

export type NotifierMeta = { events: string[]; wildcard: string; types: NotifierType[] };
export type TestResult = { ok: boolean; status?: number; error?: string };

export const notifiersApi = {
  list: () => call<{ notifiers: NotifierRow[] }>('GET', '/api/notifiers'),
  meta: () => call<NotifierMeta>('GET', '/api/notifiers/meta'),
  create: (b: { name: string; type: string; events: string[]; config: Record<string, string> }) =>
    call<{ notifier: NotifierRow; secret: string | null }>('POST', '/api/notifiers', b),
  setEnabled: (n: number, enabled: boolean) => call<{ notifier: NotifierRow }>('PATCH', `/api/notifiers/${n}`, { enabled }),
  remove: (n: number) => call<unknown>('DELETE', `/api/notifiers/${n}`),
  test: (n: number) => call<{ result: TestResult }>('POST', `/api/notifiers/${n}/test`, {}),
  deliveries: (n: number) => call<{ deliveries: DeliveryRow[]; queued?: number }>('GET', `/api/notifiers/${n}/deliveries`),
  redeliver: (n: number, d: number) => call<{ note?: string }>('POST', `/api/notifiers/${n}/deliveries/${d}/redeliver`, {}),
};
