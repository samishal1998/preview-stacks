/**
 * The only real proof this feature works: there is no component harness in this app, so the parsers
 * live in a `.ts` and are tested here, directly.
 *
 * Outside `src/` on purpose — `tsconfig.app.json` includes `src/**​/*.ts`, and a `bun:test` import
 * there would fail `vue-tsc` unless `@types/bun` joined the app's dependencies for a file the app
 * never ships. Run with `cd apps/ui && bun test test/useVarFormats.test.ts`.
 */
import { describe, expect, test } from 'bun:test';
import {
  detectFormat,
  formatVars,
  mergeVars,
  parseVars,
  VAR_FORMATS,
  type VarFormat,
} from '../src/composables/useVarFormats';

/** Every shape a value can take that a naive formatter loses. */
const HOSTILE = [
  { k: 'EMPTY', v: '' },
  { k: 'PLAIN', v: '7' },
  { k: 'SPACES', v: 'hello world' },
  { k: 'LEADING_SPACE', v: '  indented' },
  { k: 'TRAILING_SPACE', v: 'trailing   ' },
  { k: 'ONLY_SPACES', v: '   ' },
  { k: 'HASH', v: '#not-a-comment' },
  { k: 'INLINE_HASH', v: 'value # still value' },
  { k: 'DQUOTE', v: 'say "hi"' },
  { k: 'SQUOTE', v: "it's" },
  { k: 'BACKSLASH', v: 'C:\\path\\n' },
  { k: 'NEWLINE', v: 'line one\nline two' },
  { k: 'EQUALS', v: 'a=b=c' },
  { k: 'COMMA', v: 'a,b,c' },
  { k: 'TAB', v: 'a\tb' },
  { k: 'DOLLAR', v: '${NOT_EXPANDED}' },
  { k: 'UNICODE', v: 'café — ✓' },
  { k: 'name', v: 'value' },
];

describe('round-trip', () => {
  // negative control: drop the `v === v.trim()` clause from delimQuote's `safe` test — LEADING_SPACE
  // and TRAILING_SPACE come back trimmed in csv and tsv. Confirmed failing.
  test('every hostile value survives export → import in all three formats', () => {
    for (const format of VAR_FORMATS) {
      const parsed = parseVars(formatVars(HOSTILE, format), format);
      expect({ format, pairs: parsed.pairs, problems: parsed.problems }).toEqual({
        format,
        pairs: HOSTILE,
        problems: [],
      });
    }
  });

  // negative control: make formatVars omit the `['name', 'value']` header row — the pair named
  // `name` is eaten as a header and csv/tsv come back one pair short. Confirmed failing.
  test('a pair literally named name,value survives csv and tsv', () => {
    for (const format of ['csv', 'tsv'] as VarFormat[]) {
      const pairs = [{ k: 'name', v: 'value' }];
      expect(parseVars(formatVars(pairs, format), format).pairs).toEqual(pairs);
    }
  });

  // negative control: have formatVars re-detect instead of trusting the argument, or delete the
  // `\r` case from envQuote — the CR is lost. Confirmed failing.
  test('a carriage return survives .env and is normalised by csv/tsv', () => {
    const pairs = [{ k: 'CR', v: 'a\rb' }];
    expect(parseVars(formatVars(pairs, 'env'), 'env').pairs).toEqual(pairs);
    // Documented exception: a row terminator cannot also be data without a dialect flag.
    expect(parseVars(formatVars(pairs, 'csv'), 'csv').pairs).toEqual([{ k: 'CR', v: 'a\nb' }]);
  });

  // negative control: delete the header-detection branch in parseDelimited — the header of an empty
  // csv export comes back as a pair named `name`. Confirmed failing.
  test('an empty list exports and re-imports as nothing', () => {
    for (const format of VAR_FORMATS) {
      const out = parseVars(formatVars([], format), format);
      expect({ pairs: out.pairs, problems: out.problems }).toEqual({ pairs: [], problems: [] });
    }
  });

  // negative control: drop the `.filter((e) => e.k)` in formatVars — the editor's blank trailing row
  // is exported as `=` and comes back as a problem. Confirmed failing.
  test('a blank editor row is not exported', () => {
    expect(formatVars([{ k: 'A', v: '1' }, { k: '', v: '' }], 'env')).toBe('A=1\n');
  });
});

describe('.env parsing', () => {
  // negative control: change `s.indexOf('=')` to `lastIndexOf` — DSN loses everything up to its last
  // `=`. Confirmed failing.
  test('splits on the first = only', () => {
    expect(parseVars('DSN=postgres://u:p@h/db?a=1&b=2', 'env').pairs).toEqual([
      { k: 'DSN', v: 'postgres://u:p@h/db?a=1&b=2' },
    ]);
  });

  // negative control: delete the `^export\s+` strip — the name becomes `export PR` and the line is
  // reported as unusable instead of parsed. Confirmed failing.
  test('export, comments, blank lines and padding', () => {
    const text = [
      '# a comment',
      '',
      'export PR=7',
      '   REGION = eu-central   ',
      '\t# indented comment',
      'EMPTY=',
    ].join('\n');
    const out = parseVars(text, 'env');
    expect(out.pairs).toEqual([
      { k: 'PR', v: '7' },
      { k: 'REGION', v: 'eu-central' },
      { k: 'EMPTY', v: '' },
    ]);
    expect(out.problems).toEqual([]);
  });

  // negative control: run unescape over single-quoted bodies too — LITERAL comes back with a real
  // newline instead of a backslash and an n. Confirmed failing.
  test('single quotes are literal, double quotes carry escapes', () => {
    const text = ['LITERAL=\'no \\n escapes # here\'', 'ESCAPED="line\\nbreak\\ttab"'].join('\n');
    expect(parseVars(text, 'env').pairs).toEqual([
      { k: 'LITERAL', v: 'no \\n escapes # here' },
      { k: 'ESCAPED', v: 'line\nbreak\ttab' },
    ]);
  });

  // negative control: stop consuming following lines when the quote is unclosed (drop the `while`
  // loop) — the value is reported as an unterminated quote and the rest of the block is parsed as
  // its own lines. Confirmed failing.
  test('a double-quoted value spans physical lines, keeping its whitespace', () => {
    const text = 'KEY="line one   \nline two"\nAFTER=1';
    expect(parseVars(text, 'env').pairs).toEqual([
      { k: 'KEY', v: 'line one   \nline two' },
      { k: 'AFTER', v: '1' },
    ]);
  });

  // negative control: strip the `\s+#.*$` replace — the inline comment is kept as part of the value.
  // Confirmed failing.
  test('an unquoted value ends at a spaced # and loses trailing space', () => {
    expect(parseVars('A=eu-central   # the region\nB=a#b', 'env').pairs).toEqual([
      { k: 'A', v: 'eu-central' },
      // No space before it, so it is not a comment — dotenv's rule.
      { k: 'B', v: 'a#b' },
    ]);
  });

  // negative control: `continue` past a malformed line without pushing to `problems` — the paste
  // silently loses three lines and the test's problems array is empty. Confirmed failing.
  test('malformed lines are reported, never dropped silently', () => {
    const text = ['GOOD=1', 'this line has no equals', '9BAD=x', 'UNTERMINATED="oops', '=novalue'].join(
      '\n',
    );
    const out = parseVars(text, 'env');
    expect(out.pairs).toEqual([{ k: 'GOOD', v: '1' }]);
    expect(out.problems.map((p) => [p.line, p.text])).toEqual([
      [2, 'this line has no equals'],
      [3, '9BAD=x'],
      [4, 'UNTERMINATED="oops'],
      [5, '=novalue'],
    ]);
    expect(out.problems.every((p) => p.reason.length > 0)).toBe(true);
  });

  // negative control: add `-` to NAME's character class — `foo-bar` is imported instead of reported.
  // Confirmed failing.
  test('a key the editor accepts but a spec cannot reference is reported, not imported', () => {
    // `VarEditor` only trims a name, so this pair can exist in the editor and be exported. NAME is
    // shared by all three parsers, so pinning it here pins it everywhere.
    const out = parseVars(formatVars([{ k: 'foo-bar', v: '1' }, { k: 'OK', v: '2' }], 'env'), 'env');
    expect(out.pairs).toEqual([{ k: 'OK', v: '2' }]);
    expect(out.problems).toHaveLength(1);
  });

  // negative control: make lastWins keep the first value (`if (!m.has(e.k))`) — PR comes back 1.
  // Confirmed failing.
  test('a repeated name keeps its last value at its first position', () => {
    expect(parseVars('PR=1\nREGION=eu\nPR=2', 'env').pairs).toEqual([
      { k: 'PR', v: '2' },
      { k: 'REGION', v: 'eu' },
    ]);
  });
});

describe('csv / tsv parsing', () => {
  // negative control: treat `"` as data everywhere (delete the quote-open branch in splitRows) —
  // the delimiter inside the quoted field splits the row into three columns. Confirmed failing.
  test('a quoted field holds the delimiter, newlines and doubled quotes', () => {
    const csv = 'A,"a,b"\nB,"line\none"\nC,"say ""hi"""';
    expect(parseVars(csv, 'csv').pairs).toEqual([
      { k: 'A', v: 'a,b' },
      { k: 'B', v: 'line\none' },
      { k: 'C', v: 'say "hi"' },
    ]);
  });

  // negative control: drop the `\r\n?` normalisation in splitRows — a QUOTED value keeps the row's
  // carriage return (`"7"` becomes `7\r`), because only unquoted fields get trimmed. Confirmed failing.
  test('CRLF input parses as cleanly as LF', () => {
    expect(parseVars('name,value\r\nPR,7\r\nQUOTED,"7"\r\nWRAPPED,"a\r\nb"\r\n', 'csv').pairs).toEqual([
      { k: 'PR', v: '7' },
      { k: 'QUOTED', v: '7' },
      { k: 'WRAPPED', v: 'a\nb' },
    ]);
  });

  // negative control: always skip the first row — the headerless paste loses PR. Confirmed failing.
  test('a header row is detected, not assumed', () => {
    expect(parseVars('PR,7\nREGION,eu', 'csv').pairs).toEqual([
      { k: 'PR', v: '7' },
      { k: 'REGION', v: 'eu' },
    ]);
    expect(parseVars('Name\tValue\nPR\t7', 'tsv').pairs).toEqual([{ k: 'PR', v: '7' }]);
    // Only the first row: a later `name,value` is data.
    expect(parseVars('PR,7\nname,value', 'csv').pairs).toEqual([
      { k: 'PR', v: '7' },
      { k: 'name', v: 'value' },
    ]);
  });

  // negative control: return early from padOrTrim without the `Math.min(2, …)` floor — `SECRET,`
  // reads as a one-column row and becomes a problem instead of an empty value. Confirmed failing.
  test('an empty value column stays a pair; padding columns are dropped', () => {
    const out = parseVars('SECRET,\nPADDED,1,,\n', 'csv');
    expect(out.pairs).toEqual([
      { k: 'SECRET', v: '' },
      { k: 'PADDED', v: '1' },
    ]);
    expect(out.problems).toEqual([]);
  });

  // negative control: take `fields.slice(0, 2)` instead of reporting a wrong column count — the
  // three-column row is imported with its note silently discarded. Confirmed failing.
  test('wrong column counts and bad names are reported with their row', () => {
    const out = parseVars('PR,7\nONLYONE\nA,1,note\nbad-name,2', 'csv');
    expect(out.pairs).toEqual([{ k: 'PR', v: '7' }]);
    expect(out.problems.map((p) => [p.line, p.text])).toEqual([
      [2, 'ONLYONE'],
      [3, 'A,1,note'],
      [4, 'bad-name,2'],
    ]);
  });

  // negative control: stop trimming unquoted fields in splitRows' endField — the hand-spaced paste
  // keeps its alignment spaces. Confirmed failing.
  test('unquoted fields are trimmed, quoted fields are not', () => {
    expect(parseVars('PR , 7\nPAD, "  kept  "', 'csv').pairs).toEqual([
      { k: 'PR', v: '7' },
      { k: 'PAD', v: '  kept  ' },
    ]);
  });
});

describe('format detection', () => {
  // negative control: detect by `text.includes('\t')` before scoring — the csv row whose value holds
  // a tab is read as tsv and comes back as one mangled pair. Confirmed failing.
  test('a tab inside a csv value does not make it a tsv', () => {
    expect(detectFormat('name,value\nA,"a\tb"')).toBe('csv');
    expect(detectFormat('name\tvalue\nA\t"a,b"')).toBe('tsv');
  });

  // negative control: drop the env-shape regex and rely on scoring alone — the URL's `,` scores the
  // line as a two-column csv. Confirmed failing.
  test('an env line wins on shape, whatever punctuation the value holds', () => {
    expect(detectFormat('URL=https://h/p?a=1,b=2')).toBe('env');
    expect(detectFormat('# comment first\nexport PR=7')).toBe('env');
  });

  // negative control: return `'csv'` as the fallback instead of `'env'` — garbage is parsed as
  // one-column rows and reports a column-count problem rather than the missing `=`. Confirmed failing.
  test('unrecognisable text falls back to .env so the problem names the missing =', () => {
    const out = parseVars('just some prose');
    expect(out.format).toBe('env');
    expect(out.problems[0]?.reason).toContain('=');
  });

  // negative control: pass an explicit format that disagrees (`parseVars(csv, 'env')`) — proves the
  // override is honoured rather than re-detected. Confirmed failing.
  test('an explicit format overrides detection', () => {
    expect(parseVars('name,value\nPR,7', 'env').problems.length).toBe(2);
    expect(parseVars('name,value\nPR,7').pairs).toEqual([{ k: 'PR', v: '7' }]);
  });

  // negative control: have every export re-detect as env — the round-trip of our own output through
  // auto-detection stops matching. Confirmed failing.
  test('our own exports detect as what we wrote', () => {
    for (const format of VAR_FORMATS) {
      expect(detectFormat(formatVars(HOSTILE, format))).toBe(format);
      expect(parseVars(formatVars(HOSTILE, format)).pairs).toEqual(HOSTILE);
    }
  });
});

describe('withheld secrets', () => {
  // negative control: make envQuote wrap an empty value in quotes — the export reads
  // `DB_PASSWORD=""`, which is a value someone can paste, not a blank to fill in. Confirmed failing.
  test('a secret exports as its name, an empty value and a comment', () => {
    const text = formatVars([{ k: 'DB_PASSWORD', v: '' }], 'env', 'Values withheld.');
    expect(text).toBe('# Values withheld.\nDB_PASSWORD=\n');
    // Empty is what the importing surface skips, and what pstack treats as undefined.
    expect(parseVars(text, 'env').pairs).toEqual([{ k: 'DB_PASSWORD', v: '' }]);
  });

  // negative control: emit the note in csv as a `#` row — it parses as a one-column problem instead
  // of being left out. Confirmed failing.
  test('csv has nowhere to put a comment, so it carries none', () => {
    const out = parseVars(formatVars([{ k: 'DB_PASSWORD', v: '' }], 'csv', 'Values withheld.'), 'csv');
    expect(out.pairs).toEqual([{ k: 'DB_PASSWORD', v: '' }]);
    expect(out.problems).toEqual([]);
  });
});

describe('merge', () => {
  // negative control: push incoming without checking for an existing name — PR appears twice and
  // the editor shows a duplicate row. Confirmed failing.
  test('incoming overrides by name, order is preserved, blank rows are dropped', () => {
    const existing = [
      { k: 'PR', v: '1' },
      { k: 'REGION', v: 'eu' },
      { k: '', v: '' },
    ];
    expect(mergeVars(existing, [{ k: 'PR', v: '2' }, { k: 'NEW', v: 'x' }])).toEqual([
      { k: 'PR', v: '2' },
      { k: 'REGION', v: 'eu' },
      { k: 'NEW', v: 'x' },
    ]);
    // The source list is not mutated — the caller decides whether to keep the result.
    expect(existing[0]).toEqual({ k: 'PR', v: '1' });
  });
});
