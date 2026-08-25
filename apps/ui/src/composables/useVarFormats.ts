/**
 * `.env`, CSV and TSV for variable lists — the one implementation both variable surfaces share.
 *
 * WHY PURE FUNCTIONS IN A `.ts`. There is no component test harness in this app, so anything that
 * can be wrong in an interesting way has to live somewhere `bun test` can reach it. Everything here
 * is a string in and a value out; the components own the chrome and the policy, and nothing else.
 *
 * THE ROUND-TRIP IS THE CONTRACT. `parseVars(formatVars(pairs, f), f)` returns `pairs` for every
 * value the editors can hold — newlines, quotes, `#`, leading and trailing spaces, backslashes,
 * `=`, the empty string. That is what makes "copy them out, paste them back" safe, and it is what
 * `test/useVarFormats.test.ts` spends most of its lines on. Two deliberate exceptions, both tested:
 *
 *   - A lone carriage return survives `.env` (escaped as `\r`) but not CSV/TSV, where CRLF and CR
 *     are normalised to LF before parsing — a row terminator cannot also be data without a
 *     dialect flag, and no `<input type="text">` can produce one anyway.
 *   - `parseVars` collapses a repeated name to its last value, as every dotenv reader does. The
 *     import preview shows the result, so the collapse is visible before anything is replaced.
 *
 * WHY CSV AND TSV ALWAYS EXPORT A `name,value` HEADER. A header row is ambiguous with data — a
 * variable really can be called `name` — and guessing wrong either eats a variable or imports a
 * label. Emitting the header makes our own output unambiguous: the header we wrote is the row the
 * detector eats, so `{ k: 'name', v: 'value' }` round-trips. Foreign input whose first row happens
 * to read `name,value` loses that row, which is why the importer previews what it parsed.
 *
 * A NAME MUST BE ONE A SPEC CAN REFERENCE. `VarEditor` only trims what is typed, so `foo-bar` can
 * exist in the editor and be exported — but `${foo-bar}` is not a thing `internal/spec` can
 * interpolate, so importing it would add a row that can never do anything. It comes back as a
 * problem instead, named, where the operator can see it.
 *
 * A MALFORMED LINE IS NEVER DROPPED SILENTLY. Every line that does not become a pair comes back in
 * `problems` with its line number, its text and why — a paste that half-worked without saying so
 * is how a deployment ends up missing the one variable its stack name is built from.
 */

import type { VarPair } from '../api/types';

export type VarFormat = 'env' | 'csv' | 'tsv';

/** A line that did not become a pair. Reported, never swallowed. */
export type VarProblem = { line: number; text: string; reason: string };

export type VarParse = { format: VarFormat; pairs: VarPair[]; problems: VarProblem[] };

export const VAR_FORMATS: VarFormat[] = ['env', 'csv', 'tsv'];
export const FORMAT_LABEL: Record<VarFormat, string> = { env: '.env', csv: 'CSV', tsv: 'TSV' };

/** The charset a spec can reference — `internal/spec`'s `${NAME}` regex, not a UI opinion. */
const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
/** Values needing no quotes: bytes that dotenv, compose and a shell all read back identically. */
const PLAIN = /^[A-Za-z0-9_@%+=:,./-]+$/;
const DELIM: Record<'csv' | 'tsv', string> = { csv: ',', tsv: '\t' };
const HEADER_NAME = new Set(['name', 'key', 'variable', 'var']);
const HEADER_VALUE = new Set(['value', 'val']);

// ── export ────────────────────────────────────────────────────────────────────────────────────────

/**
 * Render pairs. `note` becomes `#` comment lines and is only expressible in `.env` — the surface
 * that passes one (host secrets, whose values are withheld) also says so on the page, because CSV
 * has nowhere to put it.
 */
export function formatVars(pairs: VarPair[], format: VarFormat, note?: string): string {
  const rows = pairs.filter((e) => e.k);
  if (format === 'env') {
    const head = note ? note.split('\n').map((l) => `# ${l}`) : [];
    return [...head, ...rows.map((e) => `${e.k}=${envQuote(e.v)}`)].join('\n') + '\n';
  }
  const d = DELIM[format];
  return [['name', 'value'], ...rows.map((e) => [e.k, e.v])]
    .map((f) => f.map((v) => delimQuote(v ?? '', d)).join(d))
    .join('\n') + '\n';
}

function envQuote(v: string): string {
  // An empty value stays bare: `KEY=` parses back to `''` and reads as "fill this in", which is
  // exactly what a withheld secret is.
  if (v === '' || PLAIN.test(v)) return v;
  return `"${v.replace(/[\\"]/g, '\\$&').replace(/\n/g, '\\n').replace(/\r/g, '\\r')}"`;
}

function delimQuote(v: string, d: string): string {
  const safe =
    v === '' || (!v.includes(d) && !v.includes('"') && !/[\n\r]/.test(v) && v === v.trim());
  return safe ? v : `"${v.replace(/"/g, '""')}"`;
}

// ── import ────────────────────────────────────────────────────────────────────────────────────────

/** Parse in `format`, or in whatever `detectFormat` reads the text as. */
export function parseVars(text: string, format?: VarFormat): VarParse {
  const f = format ?? detectFormat(text);
  return f === 'env' ? parseEnv(text) : parseDelimited(text, f);
}

/**
 * Which format is this? The first meaningful line decides `.env`; otherwise both delimiters are
 * tried with the real splitter and scored, because `indexOf('\t')` calls a CSV whose value contains
 * a tab a TSV. A field that still holds a `"` after splitting is the tell that the row was cut on
 * the wrong delimiter, so it counts against that reading.
 */
export function detectFormat(text: string): VarFormat {
  const first = text.split(/\r\n|\n|\r/).find((l) => l.trim() && !l.trim().startsWith('#')) ?? '';
  if (/^(export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=/.test(first.trim())) return 'env';
  const tsv = score(text, '\t');
  const csv = score(text, ',');
  if (tsv <= 0 && csv <= 0) return 'env';
  return tsv > csv ? 'tsv' : 'csv';
}

function score(text: string, d: string): number {
  let s = 0;
  for (const r of splitRows(text, d)) {
    const f = padOrTrim(r.fields);
    if (!f.length) continue;
    s += f.length === 2 ? 1 : -1;
    for (const v of f) if (v.includes('"')) s -= 1;
  }
  return s;
}

function parseEnv(text: string): VarParse {
  const lines = text.split(/\r\n|\n|\r/);
  const pairs: VarPair[] = [];
  const problems: VarProblem[] = [];

  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i] ?? '';
    const at = i + 1;
    if (!raw.trim() || raw.trim().startsWith('#')) continue;

    // Only the LEFT is trimmed: a quoted value that spans lines keeps the spaces before its first
    // newline, which `raw.trim()` would have eaten.
    const s = raw.trimStart().replace(/^export\s+/, '');
    const eq = s.indexOf('='); // the FIRST `=`; the rest of the line is value, `=` signs and all
    if (eq < 0) {
      problems.push({ line: at, text: raw, reason: 'no "=" — a line reads NAME=value' });
      continue;
    }
    const k = s.slice(0, eq).trim();
    if (!NAME.test(k)) {
      problems.push({ line: at, text: raw, reason: nameReason(k) });
      continue;
    }

    const rest = s.slice(eq + 1).replace(/^[ \t]+/, '');
    const q = rest[0];
    if (q === '"' || q === "'") {
      const start = i;
      let body = rest.slice(1);
      let end = closingQuote(body, q);
      while (end < 0 && i + 1 < lines.length) {
        i++;
        body += '\n' + (lines[i] ?? '');
        end = closingQuote(body, q);
      }
      if (end < 0) {
        // Rewind. The lines swallowed hunting for a closing quote are other people's lines, and
        // eating the rest of the file behind one stray `"` is the silent drop this reports instead.
        i = start;
        problems.push({ line: at, text: raw, reason: 'the quote is never closed' });
        continue;
      }
      const inner = body.slice(0, end);
      // Single quotes are literal, double quotes carry escapes — dotenv's rule, and the one our
      // own exporter writes against. Anything after the closing quote is a trailing comment.
      pairs.push({ k, v: q === '"' ? unescape(inner) : inner });
      continue;
    }
    // Unquoted: a ` #` starts a comment, trailing whitespace is not value.
    pairs.push({ k, v: rest.replace(/\s+#.*$/, '').trimEnd() });
  }
  return { format: 'env', pairs: lastWins(pairs), problems };
}

const ESCAPES: Record<string, string> = {
  n: '\n',
  r: '\r',
  t: '\t',
  '\\': '\\',
  '"': '"',
  "'": "'",
  $: '$',
  '`': '`',
};

function unescape(s: string): string {
  let out = '';
  for (let i = 0; i < s.length; i++) {
    const c = s[i] ?? '';
    if (c !== '\\' || i === s.length - 1) {
      out += c;
      continue;
    }
    const n = s[++i] ?? '';
    // An unknown escape keeps both characters: `C:\path` typed inside quotes stays `C:\path`
    // rather than losing the backslash to a rule nobody wrote down.
    out += ESCAPES[n] ?? '\\' + n;
  }
  return out;
}

function closingQuote(s: string, q: string): number {
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (q === '"' && c === '\\') {
      i++;
      continue;
    }
    if (c === q) return i;
  }
  return -1;
}

function parseDelimited(text: string, format: 'csv' | 'tsv'): VarParse {
  const rows = splitRows(text, DELIM[format]);
  const pairs: VarPair[] = [];
  const problems: VarProblem[] = [];
  let first = true;

  for (const r of rows) {
    const fields = padOrTrim(r.fields);
    if (!fields.length) continue; // a blank line is not malformed
    if (first) {
      first = false;
      const a = (fields[0] ?? '').toLowerCase();
      const b = (fields[1] ?? '').toLowerCase();
      if (fields.length === 2 && HEADER_NAME.has(a) && HEADER_VALUE.has(b)) continue;
    }
    if (fields.length !== 2) {
      const n = fields.length;
      problems.push({
        line: r.line,
        text: r.raw,
        reason: `${n} column${n === 1 ? '' : 's'} — expected two, a name and a value`,
      });
      continue;
    }
    const k = (fields[0] ?? '').trim();
    if (!NAME.test(k)) {
      problems.push({ line: r.line, text: r.raw, reason: nameReason(k) });
      continue;
    }
    pairs.push({ k, v: fields[1] ?? '' });
  }
  return { format, pairs: lastWins(pairs), problems };
}

type Row = { fields: string[]; line: number; raw: string };

/**
 * RFC 4180 with one delimiter parameter: `""` is a literal quote, a quoted field may hold the
 * delimiter and newlines, an unquoted field is trimmed. CRLF and lone CR are normalised to LF
 * first — a row terminator cannot also be data.
 */
function splitRows(text: string, d: string): Row[] {
  const s = text.replace(/\r\n?/g, '\n');
  const rows: Row[] = [];
  let fields: string[] = [];
  let field = '';
  let quoted = false;
  let inQuote = false;
  let line = 1;
  let rowLine = 1;
  let rowStart = 0;

  const endField = (): void => {
    fields.push(quoted ? field : field.trim());
    field = '';
    quoted = false;
  };

  for (let i = 0; i < s.length; i++) {
    const c = s[i] ?? '';
    if (inQuote) {
      if (c === '"') {
        if (s[i + 1] === '"') {
          field += '"';
          i++;
        } else inQuote = false;
      } else {
        if (c === '\n') line++;
        field += c;
      }
      continue;
    }
    // A quote opens a field only at its start — spaces before it are formatting, a quote after
    // real characters is data.
    if (c === '"' && !quoted && field.trim() === '') {
      field = '';
      inQuote = true;
      quoted = true;
      continue;
    }
    if (c === d) {
      endField();
      continue;
    }
    if (c === '\n') {
      endField();
      rows.push({ fields, line: rowLine, raw: s.slice(rowStart, i) });
      fields = [];
      line++;
      rowLine = line;
      rowStart = i + 1;
      continue;
    }
    field += c;
  }
  if (field !== '' || quoted || fields.length) {
    endField();
    rows.push({ fields, line: rowLine, raw: s.slice(rowStart) });
  }
  return rows;
}

/**
 * Drop trailing empty columns a spreadsheet padded the row with — but never below two, or
 * `SECRET,` (a name whose value was withheld) would read as a one-column row instead of an empty
 * value. An all-empty row is a blank line.
 */
function padOrTrim(fields: string[]): string[] {
  let n = fields.length;
  while (n > 0 && (fields[n - 1] ?? '') === '') n--;
  return n === 0 ? [] : fields.slice(0, Math.max(n, Math.min(2, fields.length)));
}

function nameReason(k: string): string {
  return k
    ? `"${k}" is not a usable variable name — letters, digits and _, not starting with a digit`
    : 'no name before the value';
}

/** dotenv semantics: a repeated name keeps its last value, at the position it first appeared. */
function lastWins(pairs: VarPair[]): VarPair[] {
  const m = new Map<string, string>();
  for (const e of pairs) m.set(e.k, e.v);
  return [...m].map(([k, v]) => ({ k, v }));
}

/** Incoming overrides by name, everything else keeps its place. Blank editor rows are dropped. */
export function mergeVars(existing: VarPair[], incoming: VarPair[]): VarPair[] {
  const next = existing.filter((e) => e.k).map((e) => ({ ...e }));
  for (const e of incoming) {
    const hit = next.find((n) => n.k === e.k);
    if (hit) hit.v = e.v;
    else next.push({ ...e });
  }
  return next;
}
