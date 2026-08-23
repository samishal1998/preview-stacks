/**
 * Lifecycle actions for the open deployment — shared by the detail HEADER (Deploy, Verify) and the
 * Danger tab (Tear down, Forget).
 *
 * WHY THIS MOVED OUT OF THE DANGER TAB. Deploy is the primary action of the entire product, and it
 * lived behind a tab named "Danger" — three clicks deep, labelled as something to be afraid of.
 * Deploying and verifying a preview are routine and reversible; they belong in the header, visible
 * from every tab. What stays in Danger is what the name promises: teardown and forgetting.
 *
 * Module-level state, same pattern as `useDeployment`: exactly one deployment detail is open at a
 * time, and the header and the tab must see the SAME pending/conflict state — two components each
 * holding their own copy is how a button stays enabled while the other copy knows a job is running.
 */
import { computed, ref } from 'vue';
import { api, classifyConflict, problem, type Conflict } from '../api/client';
import type { ActionResponse, ConflictBody, JobsResponse } from '../api/types';
import { dep, isShared, varsQuery } from './useDeployment';
import { loadDeployments, state } from './useControlPlane';
import { settings } from './useSettings';
import { authState } from './useAuth';
import { toast } from './useToasts';
import { actionLabel } from './useFormat';
import { router } from '../router';

export type LifecycleAction = 'up' | 'verify' | 'down' | 'sleep' | 'wake';

export const pending = ref<'' | LifecycleAction | 'forget'>('');
export const actionError = ref('');
export const conflict = ref<Conflict | null>(null);
export const conflictJobId = ref('');

export const busy = computed(() => dep.detail?.busy === true);
/**
 * True only when NOTHING can authenticate the caller. This predates accounts and used to check the
 * token alone — so a signed-in operator with no token pasted was told "every action below will be
 * refused" while their actions were succeeding. A session is a credential; the warning must know so.
 */
export const tokenMissing = computed(
  () => state.health?.authEnforced === true && !settings.token && !authState.authed,
);

/**
 * The variables an action will send, as prose. Which variables are set decides which stack is
 * touched, so the NAMES are worth stating — the URL-encoded query string they become is not.
 */
export const varsSummary = computed(() => {
  const names = dep.vars.filter((v) => v.k).map((v) => v.k);
  // NOT "no variables": with none set here the server resolves the ones stored with the deployment,
  // and claiming otherwise would have an operator hunting for variables that are already in place.
  return names.length
    ? `Sending ${names.join(', ')} with every action.`
    : 'Using the variables saved with this deployment.';
});

/** A disabled control must say why. This is the text that answers it. */
export function whyDisabled(action: LifecycleAction, forceArmed = false): string | undefined {
  if (!dep.detail) return 'The spec has not resolved — the server cannot compute the stack name.';
  if (busy.value) return `A job is already in flight for ${dep.detail.stack}.`;
  if (action === 'down' && isShared.value && !forceArmed) {
    return `Type the stack name "${dep.detail.stack}" below to confirm a shared teardown.`;
  }
  if (action === 'sleep' && isShared.value) {
    return 'A shared deployment cannot sleep — every preview that depends on it would go with it.';
  }
  if (action === 'sleep' && !dep.detail.compose) return 'This spec has no compose section — there is nothing to put to sleep.';
  if (action === 'sleep' && dep.detail.asleep) return 'Already asleep.';
  return undefined;
}

export async function act(
  action: LifecycleAction,
  opts: { verify?: boolean; force?: boolean } = {},
): Promise<void> {
  if (!dep.detail) return;
  pending.value = action;
  actionError.value = '';
  conflict.value = null;

  const body =
    action === 'down'
      ? // `force` is only ever true for a shared deployment whose stack name was typed out in full.
        { verify: opts.verify ?? true, force: opts.force ?? false }
      : {};

  const r = await api.post<ActionResponse & ConflictBody>(
    `/api/deployments/${encodeURIComponent(dep.id)}/${action}${varsQuery.value}`,
    body,
  );
  pending.value = '';

  if (r.status === 202) {
    const job = r.body.job;
    if (job?.id) {
      toast('info', `${actionLabel(action)} started on ${job.stack}`, {
        to: `/jobs/${encodeURIComponent(job.id)}`,
        toLabel: 'Follow',
      });
      void loadDeployments();
      void router.push(`/jobs/${encodeURIComponent(job.id)}`);
      return;
    }
    actionError.value = 'The action was accepted, but the response carried nothing to follow.';
    return;
  }
  if (r.status === 409) return onConflict(r.body);

  actionError.value = problem(r, actionLabel(action).toLowerCase());
  toast('error', `${actionLabel(action)} was refused.`);
}

/**
 * Classify on payload SHAPE (in `client.ts`), never on message text — then look for a running job
 * ONLY to offer a link. "A job is in flight" and "docker did not answer" are shape-identical, so
 * neither is claimed; the server's message is printed verbatim either way. Never retry.
 */
export async function onConflict(body: ConflictBody): Promise<void> {
  const c = classifyConflict(body);
  conflict.value = c;
  conflictJobId.value = '';

  const stack = c.stack || dep.detail?.stack;
  if (c.kind !== 'other' || !stack) return;
  const jl = await api.get<JobsResponse>('/api/jobs');
  const live = (jl.body.jobs ?? []).find((j) => j.state === 'running' && j.stack === stack);
  if (live) conflictJobId.value = live.id;
}

/** Leaving the deployment must not carry a stale error onto the next one. */
export function resetActions(): void {
  pending.value = '';
  actionError.value = '';
  conflict.value = null;
  conflictJobId.value = '';
}
