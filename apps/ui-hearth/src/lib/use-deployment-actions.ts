/**
 * Lifecycle actions for the open deployment. One instance is shared by the page header (Deploy,
 * Verify, Wake) and the Danger section (Sleep, Tear down, Forget), so both see the same pending
 * state: two copies is how a button stays enabled while the other knows a job is running.
 */
import { computed, ref, type Ref } from 'vue';
import { useRouter } from 'vue-router';
import { ACTION_LABEL } from '../format';
import { auth, data, refresh, toast } from '../state';
import { can } from './users';
import { deploymentApi, type DeploymentDetail } from '../api-deployment';
import type { Lifecycle } from '../api';
import { classifyConflict, problemText, whyDisabled, type Conflict, type ConflictBody } from './deployment';

export type Pending = '' | Lifecycle | 'down-forget' | 'forget';

export function useDeploymentActions(id: () => string, dep: Ref<DeploymentDetail | null>) {
  const router = useRouter();
  const pending = ref<Pending>('');
  const error = ref('');
  const conflict = ref<Conflict | null>(null);
  const conflictJobId = ref('');
  const busy = computed(() => dep.value?.busy === true);

  // Below developer every one of these is a 403: say so up front. The server still decides.
  const why = (a: Lifecycle, forceArmed = false): string => (can('developer', auth) ? whyDisabled(a, dep.value, forceArmed) : 'Needs the developer role.');

  function reset(): void {
    pending.value = '';
    error.value = '';
    conflict.value = null;
    conflictJobId.value = '';
  }

  function onConflict(b: ConflictBody): void {
    const c = classifyConflict(b);
    conflict.value = c;
    // A running job is only offered as a link; "a job is in flight" and "docker did not answer" look alike.
    const stack = c.stack || dep.value?.stack;
    conflictJobId.value = c.kind === 'other' && stack ? (data.jobs.find((j) => j.state === 'running' && j.stack === stack)?.id ?? '') : '';
  }

  async function act(action: Lifecycle, opts: { verify?: boolean; force?: boolean; forget?: boolean } = {}): Promise<void> {
    if (!dep.value) return;
    const forget = action === 'down' && !!opts.forget;
    const label = forget ? 'Tear down and forget' : ACTION_LABEL[action]!;
    pending.value = forget ? 'down-forget' : action;
    error.value = '';
    conflict.value = null;
    const r = await deploymentApi.act(id(), action, opts);
    pending.value = '';
    if (r.status === 202) {
      const job = r.body.job;
      if (!job?.id) {
        error.value = 'Started, but there is no job to follow. Check Jobs.';
        return;
      }
      toast(`${label} ${job.state === 'queued' ? 'queued' : 'started'}`, 'success', job.stack);
      void refresh();
      void router.push(`/jobs/${encodeURIComponent(job.id)}`);
      return;
    }
    if (r.status === 409) return onConflict(r.body);
    error.value = problemText(r.status, r.body.error, label);
    toast(`${label} refused`, 'danger', error.value);
  }

  /** Removes the record only. The server refuses with 409 while any container exists. */
  async function forgetRecord(): Promise<void> {
    pending.value = 'forget';
    error.value = '';
    conflict.value = null;
    const r = await deploymentApi.forget(id());
    pending.value = '';
    if (r.ok) {
      toast('Forgotten', 'success', id());
      void refresh();
      void router.push('/deployments');
      return;
    }
    if (r.status === 409) return onConflict(r.body);
    error.value = problemText(r.status, r.body.error, 'Forget');
  }

  return { pending, error, conflict, conflictJobId, busy, why, reset, act, forgetRecord };
}

export type DeploymentActions = ReturnType<typeof useDeploymentActions>;
