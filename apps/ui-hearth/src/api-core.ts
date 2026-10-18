/** Overview and job calls beyond the shared wrappers in api.ts. */
import { call, id } from './api';

export type ControlService = { name: string; state: string; health: string; image: string };
export type Control = {
  project: string;
  reachable: boolean;
  parseError: boolean;
  services: ControlService[];
  managedBy: string;
};

export const coreApi = {
  /** Read-only on purpose: pstack runs inside this stack, so an action here could kill the API. */
  control: () => call<Partial<Control>>('GET', '/api/control'),
  /** Stops one job. A queued job is dropped. 409: it ended on its own. */
  cancelJob: (job: string) => call<{ warning?: string }>('POST', `/api/jobs/${id(job)}/cancel`, {}),
  streamUrl: (job: string) => `/api/jobs/${id(job)}/stream`,
};

/** Defaults for a field an older server leaves out. `reachable !== false`, not `!!reachable`. */
export function normalizeControl(b: Partial<Control>): Control {
  return {
    project: b.project ?? '',
    reachable: b.reachable !== false,
    parseError: !!b.parseError,
    services: Array.isArray(b.services) ? b.services : [],
    managedBy: b.managedBy ?? '',
  };
}
