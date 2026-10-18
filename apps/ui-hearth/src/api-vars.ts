/** Deployment detail (`GET /api/deployments/:id`): the spec's env, axes and requires. */
import { call, id } from './api';
import { varsQuery, type VarPair } from './lib/vars';

export type HookName = 'up' | 'assert_live' | 'down' | 'assert_gone';
export type Axis = { name: string; hooks: HookName[]; verifiable: boolean };
export type Requirement = { name: string; hint: string | null };
export type DisplayVar = { key: string; value: string; visibility: string; length: number };
export type DeploymentDetail = {
  id: string;
  kind: string;
  stack: string;
  requires: Requirement[];
  axes: Axis[];
  env: DisplayVar[];
};

/** Request variables ride as the query string; a 400 names the variable that is missing. */
export const getDetail = (dep: string, vars: VarPair[]) =>
  call<DeploymentDetail>('GET', `/api/deployments/${id(dep)}${varsQuery(vars)}`);

/** Older builds omit fields; derive instead of defaulting to a false claim. */
export function normalizeDetail(b: DeploymentDetail): DeploymentDetail {
  return {
    ...b,
    requires: Array.isArray(b.requires) ? b.requires : [],
    env: Array.isArray(b.env) ? b.env : [],
    axes: (Array.isArray(b.axes) ? b.axes : []).map((a) => {
      const hooks = Array.isArray(a.hooks) ? a.hooks : [];
      return { name: a.name, hooks, verifiable: a.verifiable ?? hooks.includes('assert_gone') };
    }),
  };
}
