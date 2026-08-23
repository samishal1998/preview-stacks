/**
 * A visibility-aware interval.
 *
 * The dashboard and the lists poll `/api/deployments`, which shells out to `docker compose ls` on
 * the host on every call. A tab left open in the background for a weekend would otherwise run that
 * ~10 times a minute forever, on the same box that is running everyone's previews. So the timer
 * stops when the document is hidden and fires once immediately on return, which is also what an
 * operator wants: the numbers are current the moment they look at them.
 *
 * Never used for the job log (that is SSE) or for compose logs (expensive, and fetched on demand).
 */

import { onMounted, onUnmounted } from 'vue';

export function usePolling(fn: () => void, intervalMs = 6000): void {
  let timer: ReturnType<typeof setInterval> | undefined;

  const stop = (): void => {
    if (timer !== undefined) clearInterval(timer);
    timer = undefined;
  };

  const start = (): void => {
    stop();
    timer = setInterval(fn, intervalMs);
  };

  const onVisibility = (): void => {
    if (document.visibilityState === 'visible') {
      // Catch up first, then resume: returning to the tab should not show a stale page for a
      // full interval before the truth arrives.
      fn();
      start();
    } else {
      stop();
    }
  };

  onMounted(() => {
    fn();
    if (document.visibilityState === 'visible') start();
    document.addEventListener('visibilitychange', onVisibility);
  });

  onUnmounted(() => {
    stop();
    document.removeEventListener('visibilitychange', onVisibility);
  });
}
