/** SSO provider form rules. Pure: no Vue, no DOM. */
import type { SsoClaimMap, SsoConfig, SsoPreset, SsoProviderEntry } from '@samyx/preview-stacks-client';

export const OIDC_CLAIMS: SsoClaimMap = { subject: 'sub', username: 'preferred_username', email: 'email', name: 'name', avatar: 'picture' };
export const INHERIT = 'inherit';
export const KEY_RE = /^[a-z0-9][a-z0-9-]{0,31}$/;

/** Empty defaultRole means inherit the host default; never admin by omission. */
export const blank = (): SsoConfig => ({
  mode: 'oidc',
  enabled: true,
  label: '',
  clientId: '',
  discoveryUrl: '',
  provider: '',
  authorizeUrl: '',
  tokenUrl: '',
  userInfoUrl: '',
  emailsUrl: '',
  groupsUrl: '',
  scopes: '',
  claimMap: { ...OIDC_CLAIMS },
  allowedEmailDomains: [],
  allowedUsernames: [],
  requiredGroups: [],
  defaultRole: '',
});

export type Editing = {
  /** null while adding; otherwise the stored key being edited. */
  originalKey: string | null;
  key: string;
  preset: SsoPreset | null;
  form: SsoConfig;
  secret: string;
  secretSet: boolean;
  domains: string;
  usernames: string;
  groups: string;
  placeholders: Record<string, string>;
};

export type Tile = { preset: SsoPreset | null; id: string; label: string; mode: 'oidc' | 'oauth2' };

export const pickerTiles = (presets: SsoPreset[]): Tile[] => [
  ...presets.map((p) => ({ preset: p, id: p.key, label: p.label, mode: p.mode })),
  { preset: null, id: 'custom-oauth2', label: 'Custom OAuth 2.0', mode: 'oauth2' },
  { preset: null, id: 'custom-oidc', label: 'Custom OpenID Connect', mode: 'oidc' },
];

/** A free slug for a second provider on the same preset: github, github-2, ... */
export function suggestKey(base: string, taken: string[]): string {
  const set = new Set(taken);
  if (!set.has(base)) return base;
  for (let n = 2; ; n++) if (!set.has(`${base}-${n}`)) return `${base}-${n}`;
}

export function startAdd(tile: Pick<Tile, 'preset' | 'mode'>, taken: string[]): Editing {
  const p = tile.preset;
  const f = blank();
  f.mode = tile.mode;
  f.provider = p ? p.key : tile.mode === 'oauth2' ? 'custom' : '';
  if (p) {
    f.label = p.label;
    f.scopes = p.scopes;
    f.claimMap = { ...OIDC_CLAIMS, ...p.claimMap };
    if (p.mode === 'oauth2') {
      f.authorizeUrl = p.authorizeUrl;
      f.tokenUrl = p.tokenUrl;
      f.userInfoUrl = p.userInfoUrl;
      // emailsUrl/groupsUrl stay blank: the server fills the preset's own back in.
    } else if (!p.discoveryUrl.includes('<')) {
      f.discoveryUrl = p.discoveryUrl;
    }
  }
  return {
    originalKey: null,
    key: suggestKey(p ? p.key : tile.mode === 'oauth2' ? 'custom' : 'oidc', taken),
    preset: p,
    form: f,
    secret: '',
    secretSet: false,
    domains: '',
    usernames: '',
    groups: '',
    placeholders: {},
  };
}

export function startEdit(entry: SsoProviderEntry, presets: SsoPreset[]): Editing {
  return {
    originalKey: entry.key,
    key: entry.key,
    preset: presets.find((x) => x.key === entry.config.provider) ?? null,
    form: { ...blank(), ...entry.config, claimMap: { ...OIDC_CLAIMS, ...entry.config.claimMap } },
    secret: '',
    secretSet: entry.secretSet,
    domains: (entry.config.allowedEmailDomains ?? []).join(', '),
    usernames: (entry.config.allowedUsernames ?? []).join(', '),
    groups: (entry.config.requiredGroups ?? []).join(', '),
    placeholders: {},
  };
}

export type Part = string | { ph: string };

/** Splits `https://<your-domain>.okta.com/` into literals and placeholder names (add mode, oidc only). */
export function templateParts(e: Editing): Part[] | null {
  if (e.originalKey !== null || !e.preset || e.form.mode !== 'oidc') return null;
  const t = e.preset.discoveryUrl;
  if (!t.includes('<')) return null;
  const parts: Part[] = [];
  const re = /<([^>]+)>/g;
  let last = 0;
  for (let m = re.exec(t); m; m = re.exec(t)) {
    if (m.index > last) parts.push(t.slice(last, m.index));
    parts.push({ ph: m[1] ?? '' });
    last = m.index + m[0].length;
  }
  if (last < t.length) parts.push(t.slice(last));
  return parts;
}

export const placeholderNames = (parts: Part[] | null): string[] =>
  (parts ?? []).flatMap((p) => (typeof p === 'string' ? [] : [p.ph]));

/** The issuer as assembled so far; an unfilled placeholder stays visible as `<name>`. */
export const assembled = (parts: Part[], values: Record<string, string>): string =>
  parts.map((p) => (typeof p === 'string' ? p : values[p.ph]?.trim() || `<${p.ph}>`)).join('');

/** Why the save button is disabled; '' when it can save. */
export function cannotSave(e: Editing, existingKeys: string[]): string {
  const key = e.key.trim();
  if (!KEY_RE.test(key)) return 'The key is a lowercase slug: letters, digits, dashes, 32 at most.';
  if (e.originalKey === null && existingKeys.includes(key)) return `"${key}" is taken. Edit it, or pick another key.`;
  if (!e.form.clientId.trim()) return 'The client id is required.';
  if (!e.secretSet && !e.secret.trim()) return 'The client secret is required.';
  if (e.form.mode === 'oidc') {
    const parts = templateParts(e);
    const url = parts ? assembled(parts, e.placeholders) : e.form.discoveryUrl.trim();
    if (!url) return 'The issuer is required.';
    if (url.includes('<')) return 'Fill in the issuer placeholder.';
  } else if (!e.form.authorizeUrl.trim() || !e.form.tokenUrl.trim()) {
    return 'The authorize and token URLs are required.';
  }
  return '';
}

export const list = (s: string): string[] => s.split(',').map((v) => v.trim()).filter(Boolean);

export type SsoBody = SsoConfig & { key: string; clientSecret: string };

/** Typed so a mistyped field fails the build instead of silently restricting nobody. */
export function buildBody(e: Editing): SsoBody {
  const parts = templateParts(e);
  return {
    ...e.form,
    key: e.key.trim(),
    clientSecret: e.secret.trim(),
    discoveryUrl: parts ? assembled(parts, e.placeholders) : e.form.discoveryUrl,
    allowedEmailDomains: list(e.domains),
    allowedUsernames: list(e.usernames),
    requiredGroups: e.form.mode === 'oidc' ? [] : list(e.groups),
  };
}

const cap = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) : s);

export function whereLine(c: SsoConfig, presets: SsoPreset[]): string {
  if (c.mode === 'oidc') {
    try {
      return `OpenID Connect, ${new URL(c.discoveryUrl).hostname}`;
    } catch {
      return 'OpenID Connect';
    }
  }
  const p = presets.find((x) => x.key === c.provider);
  return p ? `OAuth 2.0, ${p.label}` : 'OAuth 2.0, custom endpoints';
}

export function roleLine(c: SsoConfig, hostDefault: string): string {
  if (c.defaultRole) return `New accounts: ${cap(c.defaultRole)}`;
  return `New accounts: host default${hostDefault ? ` (${cap(hostDefault)})` : ''}`;
}

export function rulesLine(c: SsoConfig): string {
  const parts: string[] = [];
  if (c.allowedEmailDomains?.length) parts.push(`domains ${c.allowedEmailDomains.join(', ')}`);
  if (c.allowedUsernames?.length) parts.push(`usernames ${c.allowedUsernames.join(', ')}`);
  if (c.requiredGroups?.length) parts.push(`groups ${c.requiredGroups.join(', ')}`);
  return parts.length ? `Lets in ${parts.join('; ')}` : 'Lets in anyone the provider allows';
}

/** Only same-origin paths survive; anything else goes home. */
export function safeNext(v: unknown): string {
  return typeof v === 'string' && v.startsWith('/') && !v.startsWith('//') && !v.startsWith('/login') ? v : '/';
}
