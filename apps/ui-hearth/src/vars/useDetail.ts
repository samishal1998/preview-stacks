/**
 * The open deployment's resolved detail, shared by the Config, Axes and Requires tabs so a tab
 * switch shows the last answer at once. Request variables are part of the state: they ride on the GET.
 * `openDetail(id)` from a tab always refetches; the same id keeps its data on screen meanwhile.
 */
import { computed, reactive } from 'vue';
import { getDetail, normalizeDetail, type DeploymentDetail } from '../api-vars';
import { writeVars, readVars } from './store';
import { problem } from '../lib/problem';
import type { VarPair } from '../lib/vars';

export const detail = reactive({
  id: '',
  data: null as DeploymentDetail | null,
  error: '',
  loading: false,
  vars: [] as VarPair[],
});

export const unverifiableAxes = computed(() => (detail.data?.axes ?? []).filter((a) => !a.verifiable).map((a) => a.name));

let seq = 0;

export async function reloadDetail(): Promise<void> {
  const want = detail.id;
  const mine = ++seq;
  detail.loading = true;
  const r = await getDetail(want, detail.vars);
  if (mine !== seq) return; // a newer reload owns the state
  detail.loading = false;
  if (!r.ok) {
    detail.data = null;
    detail.error = problem(r, 'Resolve the spec');
    return;
  }
  detail.error = '';
  detail.data = normalizeDetail(r.body);
}

export function openDetail(id: string): void {
  if (detail.id !== id) {
    detail.data = null;
    detail.error = '';
  }
  detail.id = id;
  detail.vars = readVars(id);
  void reloadDetail();
}

export function persistVars(): void {
  writeVars(detail.id, detail.vars);
}

export function applyVars(): void {
  persistVars();
  void reloadDetail();
}
