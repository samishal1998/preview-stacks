/**
 * Custom routes for one page: the list and every write. The Routing page and the deployment page each
 * draw their own table over one of these, so both ask the same move question the same way.
 */
import { computed, ref } from 'vue';
import { customRoutesApi, type CustomRoute } from '../api-routing';
import { auth, data, toast } from '../state';
import { moveQuestion, needsMove, type Holder } from './customRoutes';
import { problem } from './problem';
import { can } from './users';

export function useCustomRoutes() {
  const routes = ref<CustomRoute[]>([]);
  const loaded = ref(false);
  const error = ref('');
  const busy = ref(false);
  /** Courtesy: writes are developer's. The server still decides. */
  const mayWrite = computed(() => can('developer', auth));
  /** A move waiting for its confirm. */
  const move = ref<{ name: string; deployment: string; question: string } | null>(null);
  /** An unassign or delete waiting for its confirm. */
  const drop = ref<{ name: string; kind: 'unassign' | 'delete' } | null>(null);

  const stackOf = (dep: string): string => data.deployments.find((d) => d.id === dep)?.stack ?? dep;

  async function load(): Promise<void> {
    const r = await customRoutesApi.list();
    loaded.value = true;
    if (!r.ok) {
      error.value = problem(r, 'Custom routes');
      return;
    }
    error.value = '';
    routes.value = Array.isArray(r.body) ? r.body : [];
  }

  function askMove(name: string, deployment: string, from: Holder): void {
    move.value = { name, deployment, question: moveQuestion(name, from, stackOf(deployment)) };
  }

  function done(msg: string): void {
    toast(msg, 'success');
    void load();
  }

  /** Points `name` at `deployment`, creating the route when it is new. A route another stack holds asks first. */
  async function assign(name: string, deployment: string): Promise<void> {
    const row = routes.value.find((r) => r.name === name);
    const held = row && needsMove(row, deployment);
    if (held) return askMove(name, deployment, held);
    busy.value = true;
    const r = row ? await customRoutesApi.assign(name, deployment) : await customRoutesApi.create(name, deployment);
    busy.value = false;
    if (r.ok) return done(`Assigned ${name} to ${stackOf(deployment)}.`);
    // The list was stale: someone else holds it now. That is the move question, not an error.
    const stale = r.status === 409 ? needsMove(r.body, deployment) : null;
    if (stale) return askMove(name, deployment, stale);
    toast(problem(r, `Not assigned`), 'danger');
    void load();
  }

  async function confirmMove(): Promise<void> {
    const m = move.value;
    if (!m) return;
    busy.value = true;
    const r = await customRoutesApi.assign(m.name, m.deployment, true);
    busy.value = false;
    move.value = null;
    if (r.ok) return done(`Moved ${m.name} to ${stackOf(m.deployment)}.`);
    toast(problem(r, 'Not moved'), 'danger');
    void load();
  }

  /** '' when created; else why not, for the form that asked. */
  async function create(name: string, deployment: string): Promise<string> {
    busy.value = true;
    const r = await customRoutesApi.create(name, deployment || undefined);
    busy.value = false;
    if (!r.ok) return problem(r, 'Not created');
    done(`Created ${name}.`);
    return '';
  }

  async function confirmDrop(): Promise<void> {
    const d = drop.value;
    if (!d) return;
    busy.value = true;
    const r = d.kind === 'delete' ? await customRoutesApi.remove(d.name) : await customRoutesApi.unassign(d.name);
    busy.value = false;
    drop.value = null;
    if (r.ok) return done(d.kind === 'delete' ? `Deleted ${d.name}.` : `Unassigned ${d.name}.`);
    toast(problem(r, d.kind === 'delete' ? 'Not deleted' : 'Not unassigned'), 'danger');
    void load();
  }

  const moveOpen = computed({ get: () => !!move.value, set: (v) => !v && (move.value = null) });
  const dropOpen = computed({ get: () => !!drop.value, set: (v) => !v && (drop.value = null) });

  return { routes, loaded, error, busy, mayWrite, move, drop, moveOpen, dropOpen, load, assign, confirmMove, create, confirmDrop };
}
