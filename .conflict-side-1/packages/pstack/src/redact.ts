/**
 * Redaction for anything a human is shown.
 *
 * The UI wants to display a deployment's configuration, and a spec's `env:` block is where
 * connection strings and tokens legitimately live. So values are masked by DEFAULT and revealed
 * only when the name looks unambiguously harmless — deny-by-default, because the cost of the two
 * mistakes is wildly asymmetric: wrongly masking `PREVIEW_DOMAIN` is a mild annoyance, wrongly
 * revealing `DATABASE_URL` in a browser tab (or a screenshot, or a support ticket) is a breach.
 *
 * This is defence in depth, NOT the primary control. The primary control is that responses are
 * built field-by-field and `Stack.env` is never serialised — see src/api.ts. If you find yourself
 * relying on this module to keep a secret out of a response, the response is wrong.
 */

/** Names that are safe in the clear: describe topology or scale, never authorise anything. */
const SAFE = [
  /^PR(_NUMBER)?$/i,
  /^STACK$/i,
  /_?DOMAIN$/i,
  /_?HOST(NAME)?$/i,
  /_?PORT$/i,
  /_?REGION$/i,
  /_?ENV(IRONMENT)?$/i,
  /_?PROFILE$/i,
  /_?TAG$/i,
  /_?VERSION$/i,
  /_?REPLICAS$/i,
  /_?TIMEOUT$/i,
  /_?LOG_LEVEL$/i,
  /^GIT_(SHA|REF|BRANCH)$/i,
  /_?IMAGE$/i,
];

/**
 * Names that are secret even though they might read as safe. Checked FIRST, because the safe list
 * is pattern-based and a name like `REGISTRY_IMAGE_PULL_SECRET` would otherwise match `_IMAGE`.
 */
const SECRET = [
  /TOKEN/i,
  /SECRET/i,
  /PASSWORD/i,
  /PASSWD/i,
  /CREDENTIAL/i,
  /PRIVATE/i,
  /_KEY$/i,
  /^KEY$/i,
  /API_?KEY/i,
  /AUTH/i,
  /SESSION/i,
  /COOKIE/i,
  /SALT/i,
  /SIGNATURE/i,
  // A URL with credentials in it — postgres://user:pass@host — is the single most common leak.
  /(DATABASE|DB|REDIS|AMQP|MONGO|POSTGRES|MYSQL)_?(URL|URI|DSN|CONN)/i,
  /^DSN$/i,
];

export type Visibility = 'shown' | 'masked';

export type DisplayVar = {
  key: string;
  /** The value, or a mask. Never the real value when `visibility` is 'masked'. */
  value: string;
  visibility: Visibility;
  /** Character count of the real value — useful for "did it get set at all?" without revealing it. */
  length: number;
};

export function isSecretName(key: string): boolean {
  if (SECRET.some((re) => re.test(key))) return true;
  return !SAFE.some((re) => re.test(key));
}

/**
 * Mask preserving only shape. Not a prefix/suffix reveal: a 4-character prefix of a 20-character
 * token is a meaningful head start for an attacker and buys the operator almost nothing.
 */
export function mask(value: string): string {
  if (value.length === 0) return '(empty)';
  return '•'.repeat(Math.min(value.length, 12));
}

/** One variable, ready to render. */
export function displayVar(key: string, value: string): DisplayVar {
  const secret = isSecretName(key);
  return {
    key,
    value: secret ? mask(value) : value,
    visibility: secret ? 'masked' : 'shown',
    length: value.length,
  };
}

/**
 * Render a spec's DECLARED variables only.
 *
 * `Stack.env` also contains the entire ambient environment (see its doc comment), so iterating it
 * would dump every secret the process holds. `declaredEnv` is the authored key list.
 */
export function displayDeclared(
  declaredEnv: string[],
  env: Record<string, string>,
): DisplayVar[] {
  return declaredEnv.map((k) => displayVar(k, env[k] ?? ''));
}

/**
 * Strip anything that looks like a secret out of free text — command output, a log line, an error.
 * Values are matched by content, since a log line has no key to inspect.
 *
 * Deliberately conservative: it catches the shapes that actually leak (a URL with a password, a
 * long opaque token assigned to a secret-looking name) and does not attempt to be exhaustive. Text
 * shown to a human is a weaker channel than a JSON field, but hook output is written by the spec
 * author and can echo anything.
 */
export function redactText(text: string, extraSecrets: string[] = []): string {
  let out = text;
  // Longest first, so a secret containing another secret is not partially replaced.
  for (const s of [...extraSecrets].filter((s) => s.length >= 8).sort((a, b) => b.length - a.length)) {
    out = out.split(s).join('••••••');
  }
  // A swarm join token. Whoever holds one can add a node that runs any task on the cluster, and
  // `docker swarm join-token` prints it in a line a hook could easily echo.
  out = out.replace(/SWMTKN-[A-Za-z0-9-]+/g, 'SWMTKN-••••••');
  // scheme://user:password@host  →  keep the shape, drop the password
  out = out.replace(/(\b[a-z][a-z0-9+.-]*:\/\/[^\s:/@]+):[^\s@]+@/gi, '$1:••••@');
  // NAME=value where NAME reads as a secret
  out = out.replace(
    /\b([A-Z][A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|API_?KEY|CREDENTIAL)[A-Z0-9_]*)=(\S+)/g,
    '$1=••••••',
  );
  return out;
}
