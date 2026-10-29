import type { Stats } from '@samyx/preview-stacks-client';
import { call } from './api';

export const statsApi = {
  read: () => call<Stats>('GET', '/api/stats'),
};
