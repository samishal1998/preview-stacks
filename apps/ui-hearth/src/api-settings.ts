import type { HostSettings, SettingKey, SettingWritten } from '@samyx/preview-stacks-client';
import { call, id } from './api';

export const settingsApi = {
  read: () => call<HostSettings>('GET', '/api/settings'),
  write: (key: SettingKey, value: number | string) => call<SettingWritten>('PUT', `/api/settings/${id(key)}`, { value }),
};
