/**
 * Log source for a deployment: a one-off read, or a follow over SSE.
 *
 * Never polled: a chatty stack would hammer the host through `docker compose logs`. Follow is one
 * server-side process whose lines arrive once each. Lines are batched so a burst costs one render,
 * and the buffer is capped so a tab left open overnight stays usable.
 */
import { onBeforeUnmount, ref, shallowRef, watch, type Ref } from 'vue';
import { deploymentApi } from '../api-deployment';
import { capLines, problemText } from './deployment';

export const MAX_FOLLOW_LINES = 5000;

export function useLogs(o: {
  id: () => string;
  token?: () => string;
  service: Ref<string>;
  timestamps: Ref<boolean>;
  tail: Ref<number>;
  since: Ref<string>;
}) {
  const lines = shallowRef<string[]>([]);
  const following = ref(false);
  const loading = ref(false);
  const error = ref('');
  const note = ref('');
  /** Set once a read or a follow has produced something to show, so an untouched pane can say so. */
  const touched = ref(false);
  const lastOk = ref(true);
  const lastMeta = ref('');

  let source: EventSource | null = null;
  let buffer: string[] = [];
  let timer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;

  const opts = () => ({ service: o.service.value || undefined, tail: o.tail.value, since: o.since.value || undefined, timestamps: o.timestamps.value });

  function flush(): void {
    timer = undefined;
    if (!buffer.length) return;
    lines.value = capLines([...lines.value, ...buffer], MAX_FOLLOW_LINES);
    buffer = [];
  }

  function stop(): void {
    source?.close();
    source = null;
    following.value = false;
    if (timer) clearTimeout(timer);
    flush();
  }

  async function fetchOnce(): Promise<void> {
    stop();
    const mine = ++seq;
    loading.value = true;
    error.value = '';
    note.value = '';
    const r = await deploymentApi.logs(o.id(), opts(), o.token?.());
    if (mine !== seq) return; // a newer request or a follow took over
    loading.value = false;
    touched.value = true;
    if (!r.ok) {
      lines.value = [];
      const shared = !!o.token?.();
      error.value =
        r.status === 401 && shared
          ? 'This link has expired or is not valid.'
          : r.status === 403 && shared
            ? 'This link does not allow logs.'
            : problemText(r.status, r.body.error, 'Logs');
      return;
    }
    lines.value = (r.body.text ?? '').split('\n').filter((l) => l.trim());
    lastOk.value = r.body.ok !== false;
    lastMeta.value = `${r.body.lines ?? lines.value.length} lines, tail ${r.body.tail}${r.body.since ? `, last ${r.body.since}` : ''}`;
  }

  function start(): void {
    stop();
    seq++;
    lines.value = [];
    buffer = [];
    error.value = '';
    note.value = '';
    touched.value = true;
    following.value = true;
    source = new EventSource(deploymentApi.streamUrl(o.id(), opts(), o.token?.()));
    source.onmessage = (ev) => {
      let p: { line?: string; done?: boolean; reason?: string };
      try {
        p = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (p.done) {
        note.value = p.reason ?? 'Stream ended.';
        stop();
        return;
      }
      if (p.line === undefined) return;
      buffer.push(p.line);
      if (!timer) timer = setTimeout(flush, 200);
    };
    // EventSource gives no status. A refused stream (expired link, signed out) lands here too.
    source.onerror = () => {
      note.value = 'The stream disconnected.';
      stop();
    };
  }

  function toggle(): void {
    if (following.value) stop();
    else start();
  }

  function reset(): void {
    stop();
    seq++;
    lines.value = [];
    buffer = [];
    error.value = '';
    note.value = '';
    touched.value = false;
    loading.value = false;
  }

  // Changing an option mid-follow must reopen the stream, or the pane keeps the old selection's lines.
  watch([o.service, o.timestamps, o.tail, o.since], () => {
    if (following.value) start();
  });
  onBeforeUnmount(stop);

  return { lines, following, loading, error, note, touched, lastOk, lastMeta, fetchOnce, start, stop, toggle, reset };
}
