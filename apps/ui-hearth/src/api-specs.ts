/** Stored specs and the submit path. Same endpoints as apps/ui. */
import { call, id } from './api';

export type Kind = 'isolated' | 'shared';
export type SpecMeta = { name: string; kind: Kind; description?: string; createdAt: number; updatedAt: number; requiredVars: string[] };
/** `source` is sent only to an authenticated request; otherwise `sourceWithheld`. */
export type SpecDetail = SpecMeta & { source?: string; sourceWithheld?: true };
export type DeploymentSource = { id: string; specName: string | null; spec?: string; compose?: string | null; sourceWithheld?: true };
export type SubmitResponse = { id: string; kind: Kind; stack: string; specName?: string | null; stackSharedWith?: string[]; swarmNotes?: string[] };

/** A 404 here means a build without named specs: not an error. */
export const listSpecs = () => call<{ specs: SpecMeta[] }>('GET', '/api/specs');
export const getSpec = (name: string) => call<SpecDetail>('GET', `/api/specs/${id(name)}`);
/** 201 created, 200 replaced; a bad spec is a 400 carrying the SpecError text. */
export const putSpec = (name: string, body: { spec: string; compose?: string; description?: string }) =>
  call<{ spec: SpecMeta }>('PUT', `/api/specs/${id(name)}`, body);
/** 409 when deployments still reference it; the body lists them in `deployments`. */
export const deleteSpec = (name: string) => call<{ deleted: string; deployments?: string[] }>('DELETE', `/api/specs/${id(name)}`);

export const deploymentSource = (dep: string) => call<DeploymentSource>('GET', `/api/deployments/${id(dep)}/source`);
/** 201 created, 200 replaced. 400 SpecError, 409 a job in flight over the stack. */
export const submit = (dep: string, body: Record<string, unknown>) => call<SubmitResponse>('PUT', `/api/deployments/${id(dep)}`, body);
