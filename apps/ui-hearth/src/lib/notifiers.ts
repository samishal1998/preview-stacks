export type NotifierField = { key: string; label: string; placeholder?: string; required: boolean; secret?: boolean };
export type NotifierType = { kind: string; label: string; fields: NotifierField[]; signs?: boolean };

/** The create form is valid: a name, at least one event, every required field of the chosen type. */
export function canCreate(f: { name: string; events: string[]; config: Record<string, string> }, type: NotifierType | null): boolean {
  return (
    !!f.name.trim() &&
    f.events.length > 0 &&
    (type?.fields ?? []).every((x) => !x.required || !!f.config[x.key]?.trim())
  );
}

/** Config for the POST: only the chosen type's own fields, trimmed, blanks dropped. */
export function configFor(type: NotifierType | null, config: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const x of type?.fields ?? []) {
    const v = config[x.key]?.trim();
    if (v) out[x.key] = v;
  }
  return out;
}

/** Events as sent: the wildcard alone when "all" is on. */
export const eventsFor = (all: boolean, picked: string[], wildcard = '*'): string[] => (all ? [wildcard] : picked);

/** The URL shown under a row. Chat types keep it under `webhookUrl`, masked by the server. */
export const targetOf = (config: Record<string, unknown>): string => String(config.url ?? config.webhookUrl ?? '');
