/**
 * Row ids for HDataTable. The table announces its row menu as "Actions for <row id>", so the id has
 * to be a human name. A name that repeats gets its key appended, which keeps ids unique.
 */
export function byLabel<T>(items: T[], label: (t: T) => string, key: (t: T) => string): Map<string, T> {
  const name = (t: T) => label(t) || key(t);
  const seen = new Map<string, number>();
  for (const t of items) seen.set(name(t), (seen.get(name(t)) ?? 0) + 1);
  return new Map(items.map((t) => [(seen.get(name(t)) ?? 0) > 1 ? `${name(t)} (${key(t)})` : name(t), t]));
}
