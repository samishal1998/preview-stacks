import { expect, test } from 'bun:test';
import { capProblem, mayWrite, mergePrefs, PREFS_VERSION, PRESSURE, pressureProblem } from '../src/lib/settings';
import { canCreate, configFor, eventsFor, targetOf } from '../src/lib/notifiers';
import { problem } from '../src/lib/settings-problem';

test('cap rule', () => {
  expect(capProblem('')).not.toBe('');
  expect(capProblem(null)).not.toBe('');
  expect(capProblem(0)).not.toBe('');
  expect(capProblem('1.5')).not.toBe('');
  expect(capProblem('12')).toBe('');
});

test('pressure rules: blank is not 0 (0 turns the guard off)', () => {
  const p = (k: string) => PRESSURE.find((x) => x.key === k)!;
  expect(PRESSURE.map((x) => x.key)).toEqual(['pressure_disk_pct', 'pressure_mem_pct', 'pressure_cpu_load', 'pressure_sleep_idle_min', 'image_prune_after_h']);
  for (const x of PRESSURE) {
    expect(pressureProblem(x, '')).not.toBe('');
    expect(pressureProblem(x, '-1')).not.toBe('');
    expect(pressureProblem(x, '0')).toBe('');
  }
  expect(pressureProblem(p('pressure_disk_pct'), '90')).toBe('');
  expect(pressureProblem(p('pressure_disk_pct'), '101')).not.toBe('');
  expect(pressureProblem(p('pressure_mem_pct'), '9.5')).not.toBe('');
  expect(pressureProblem(p('pressure_cpu_load'), '2.5')).toBe('');
  expect(pressureProblem(p('pressure_cpu_load'), '500')).toBe('');
  expect(pressureProblem(p('image_prune_after_h'), '1000')).toBe('');
  expect(pressureProblem(p('pressure_sleep_idle_min'), '1.5')).not.toBe('');
  // The server's bounds (settings.go wholeMax). negative control: max Infinity for either key fails this.
  expect(pressureProblem(p('pressure_sleep_idle_min'), '525600')).toBe('');
  expect(pressureProblem(p('pressure_sleep_idle_min'), '525601')).toBe('0 to 525600.');
  expect(pressureProblem(p('image_prune_after_h'), '87600')).toBe('');
  expect(pressureProblem(p('image_prune_after_h'), '87601')).toBe('0 to 87600.');
  expect(pressureProblem(p('pressure_disk_pct'), '101')).toBe('0 to 100.');
});

test('mayWrite fails closed', () => {
  expect(mayWrite('admin', 'maintainer', false)).toBe(false);
  expect(mayWrite('maintainer', 'admin', false)).toBe(true);
  expect(mayWrite('wizard', 'maintainer', false)).toBe(false);
  expect(mayWrite('maintainer', 'wizard', false)).toBe(false);
  expect(mayWrite('maintainer', undefined, false)).toBe(false);
  expect(mayWrite('admin', undefined, true)).toBe(true);
});

test('notifier form', () => {
  const t = { kind: 'webhook', label: 'Webhook', fields: [{ key: 'url', label: 'URL', required: true }, { key: 'x', label: 'X', required: false }] };
  expect(canCreate({ name: 'a', events: ['*'], config: { url: ' ' } }, t)).toBe(false);
  expect(canCreate({ name: 'a', events: [], config: { url: 'u' } }, t)).toBe(false);
  expect(canCreate({ name: 'a', events: ['*'], config: { url: 'u' } }, t)).toBe(true);
  expect(configFor(t, { url: ' u ', x: '', stray: 'z' })).toEqual({ url: 'u' });
  expect(eventsFor(true, ['a'])).toEqual(['*']);
  expect(eventsFor(false, ['a'])).toEqual(['a']);
  expect(targetOf({ webhookUrl: 'h' })).toBe('h');
});

test('problem wording', () => {
  expect(problem({ status: 403, body: {} }, 'Save')).toContain('not allowed');
  expect(problem({ status: 404, body: {} }, 'Load')).toContain('not supported');
  expect(problem({ status: 409, body: { error: 'dup' } }, 'Add')).toBe('Add: dup');
});

test('prefs: fresh or unreadable prefs start on the custom theme, compact', () => {
  // negative control: defaulting density to 'comfortable' fails this test.
  for (const raw of [{}, null, 'junk', 7]) expect(mergePrefs(raw)).toEqual({ v: PREFS_VERSION, theme: 'custom', mode: 'system', density: 'compact' });
});

test('prefs: old prefs without v reset density to compact once and keep the rest', () => {
  // negative control: spreading the stored object without the version check keeps 'comfortable'.
  expect(mergePrefs({ theme: 'ocean', mode: 'dark', density: 'comfortable' })).toEqual({ v: PREFS_VERSION, theme: 'ocean', mode: 'dark', density: 'compact' });
});

test('prefs: versioned prefs keep the chosen density', () => {
  // negative control: resetting density unconditionally turns this 'comfortable' into 'compact'.
  expect(mergePrefs({ v: PREFS_VERSION, theme: 'rose', mode: 'light', density: 'comfortable' })).toEqual({ v: PREFS_VERSION, theme: 'rose', mode: 'light', density: 'comfortable' });
});

test('prefs: before v2 the old default sunset moves to the custom theme; a picked palette stays', () => {
  // negative control: dropping the `stored.theme === 'sunset'` check turns this 'ocean' into 'custom'.
  expect(mergePrefs({ v: 1, theme: 'sunset', mode: 'dark', density: 'comfortable' })).toEqual({ v: PREFS_VERSION, theme: 'custom', mode: 'dark', density: 'comfortable' });
  expect(mergePrefs({ v: 1, theme: 'ocean', mode: 'dark', density: 'comfortable' })).toEqual({ v: PREFS_VERSION, theme: 'ocean', mode: 'dark', density: 'comfortable' });
  // A sunset picked after v2 is a choice.
  expect(mergePrefs({ v: 2, theme: 'sunset', mode: 'dark', density: 'compact' }).theme).toBe('sunset');
});
