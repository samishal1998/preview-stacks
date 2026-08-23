/**
 * Measure the Bun/JS semantics the contract depends on and write them down as fixtures.
 *
 * The Go binary reproduces these by TEST, not by reading about them: every table here is consumed
 * by a `go test` in the foundation packages (yamlx, js, jsonx, auth). Measured once on Bun 1.3.12
 * and checked in; the file is kept so the measurement can be repeated, never regenerated routinely
 * — a newer Bun measuring differently would be a change to the contract, not a correction of it.
 *
 *   bun gen/facts.ts          → golden/facts/{argon2,yaml,yaml-corpus,coerce,json-numbers,esc}.json
 */
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { REPO } from '../harness/impl.ts';

const OUT = join(REPO, 'packages', 'conformance', 'golden', 'facts');
mkdirSync(OUT, { recursive: true });
const write = (name: string, v: unknown) => {
  writeFileSync(join(OUT, name), JSON.stringify(v, null, 2) + '\n');
  console.log(`  wrote golden/facts/${name}`);
};

// ── argon2 ──────────────────────────────────────────────────────────────────────────────────────
// Two call shapes live in auth.ts (createUser passes 'argon2id', setPassword passes nothing), plus
// one with explicit non-default cost: a verifier that hardcodes m/t/p instead of PARSING the PHC
// string passes the first two and fails the third. Salts are random, so the hashes differ per run;
// what a consumer checks is that each verifies its password and none verifies the wrong one.
{
  const password = 'correct horse battery staple';
  const rows = [
    { shape: "hash(pw, 'argon2id')", hash: await Bun.password.hash(password, 'argon2id') },
    { shape: 'hash(pw)', hash: await Bun.password.hash(password) },
    {
      shape: "hash(pw, { algorithm: 'argon2id', memoryCost: 19456, timeCost: 3 })",
      hash: await Bun.password.hash(password, { algorithm: 'argon2id', memoryCost: 19456, timeCost: 3 }),
    },
  ];
  for (const r of rows) {
    if (!(await Bun.password.verify(password, r.hash))) throw new Error(`Bun cannot verify its own hash for ${r.shape}`);
    if (await Bun.password.verify('wrong', r.hash)) throw new Error('Bun verified a wrong password');
  }
  write('argon2.json', {
    bun: Bun.version,
    password,
    wrongPassword: 'wrong',
    rows: rows.map((r) => ({ ...r, params: /^\$(argon2id)\$v=(\d+)\$m=(\d+),t=(\d+),p=(\d+)\$/.exec(r.hash)?.slice(1) })),
    note: 'A conforming verifier reads variant, v, m, t and p from the string. Salt and key are raw (unpadded) standard base64.',
  });
}

// ── YAML dialect ─────────────────────────────────────────────────────────────────────────────────
// What Bun.YAML.parse makes of each scalar and structure. The Go parser is tested against this
// table: the VALUE and its TYPE (null/boolean/number/string/array/object).
{
  const typeOf = (v: unknown): string => (v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v);
  const scalars = [
    // 1.1 vs 1.2: these are STRINGS in 1.2 core
    'yes', 'no', 'on', 'off', 'Yes', 'NO', 'y', 'n',
    // booleans
    'true', 'false', 'True', 'FALSE',
    // nulls
    '~', 'null', 'Null', 'NULL', '',
    // ints and the octal/hex/exponent questions
    '0755', '0o755', '0x1f', '0b101', '1e3', '1E3', '+12', '-12', '007', '1_000', '9007199254740993', '123456789012345678901234567890',
    // floats
    '1.0', '.5', '5.', '1.5e2', '.inf', '-.Inf', '.NaN', '.nan', 'inf', 'nan',
    // sexagesimal and timestamps (1.1 would parse these)
    '22:22', '1:20:30', '2001-12-14', '2001-12-14t21:59:43.10-05:00',
    // strings that look like things
    '80:80', '"80:80"', "'yes'", '"1"', 'a b', 'a: b', '#notacomment', 'x#y', 'null-ish', 'true-ish',
  ];
  const scalarRows = scalars.map((input) => {
    let value: unknown;
    let error: string | null = null;
    try {
      value = Bun.YAML.parse(`k: ${input}`);
      value = (value as Record<string, unknown>).k;
    } catch (e) {
      error = String(e);
    }
    // `repr` is String(value): what spec.ts's asString() puts into a stack name, and the only way
    // Infinity/NaN (JSON null) and float precision loss stay visible in the fixture.
    return { input, value: value === undefined ? null : value, type: error ? 'error' : typeOf(value), repr: error ? null : String(value), error, json: error ? null : JSON.stringify(value) };
  });

  const docs: Array<{ name: string; yaml: string }> = [
    { name: 'key order is insertion order', yaml: 'z: 1\na: 2\nm: 3\n"1": 4\n"0": 5\n' },
    { name: 'duplicate keys', yaml: 'a: 1\nb: 2\na: 3\n' },
    { name: 'anchors and aliases', yaml: 'base: &b\n  x: 1\n  y: [1, 2]\nref: *b\n' },
    { name: 'merge keys', yaml: 'base: &b\n  x: 1\n  y: 2\nchild:\n  <<: *b\n  y: 9\n  z: 3\n' },
    { name: 'multi-document', yaml: 'a: 1\n---\nb: 2\n' },
    { name: 'block scalars', yaml: 'lit: |\n  line1\n  line2\nfold: >\n  one\n  two\n\n  three\nkeep: |+\n  x\n\nstrip: |-\n  y\n' },
    { name: 'flow collections', yaml: 'seq: [1, "2", three, null, ~, true]\nmap: {a: 1, b: [x, y], c: {d: e}}\nempty: []\nemptymap: {}\n' },
    { name: 'quoted vs plain', yaml: "a: 'single ''quoted'''\nb: \"double \\\"quoted\\\" \\n newline\"\nc: plain # comment\nd: 'yes'\ne: \"0755\"\n" },
    { name: 'nulls in mappings', yaml: 'a:\nb: ~\nc: null\nd: ""\n' },
    { name: 'nested sequences of mappings', yaml: 'axes:\n  - name: a\n    up: "true"\n  - name: b\n    up: |\n      echo hi\n      echo there\n' },
    { name: 'compose-shaped', yaml: 'services:\n  web:\n    image: nginx\n    ports:\n      - 80:80\n      - "443:443"\n    restart: no\n    labels:\n      - traefik.enable=true\n      - pstack.routing.port=80\n    environment:\n      A: yes\n      B: 0755\n      C: 1e3\n' },
    { name: 'tabs and odd whitespace', yaml: 'a: 1   \nb:    2\n' },
    { name: 'unicode', yaml: 'name: Ünïcødé ✓\nemoji: "🚀"\n' },
    // NO FINAL LINE BREAK. A stream's last break is not content, so clip chomping still keeps one
    // newline at end-of-input — and any trailing spaces on that last line are content. A spec
    // submitted over HTTP routinely arrives without a trailing newline, and axis hooks are block
    // scalars, so this decides the bytes a hook receives. (goccy drops both; found by
    // diff/corpus.ts over real files, never by the cases above.)
    { name: 'block scalar at end of input, no trailing newline', yaml: 'hook: |\n  set -e\n  deploy' },
    { name: 'block scalar at end of input, trailing spaces kept', yaml: 'hook: |\n  a b \n  c d ' },
    { name: 'plain scalar at end of input, no trailing newline', yaml: 'a: 1\nb: two' },
  ];
  const docRows = docs.map((d) => {
    try {
      const value = Bun.YAML.parse(d.yaml);
      return { ...d, value, json: JSON.stringify(value) };
    } catch (e) {
      return { ...d, error: String(e) };
    }
  });
  const invalid = ['a: [1, 2', 'a: b: c', '- a\nb: c', '\t- tab', 'a: "unterminated'];
  const invalidRows = invalid.map((yaml) => {
    try {
      return { yaml, value: Bun.YAML.parse(yaml), threw: false };
    } catch (e) {
      return { yaml, threw: true, error: String(e).split('\n')[0] };
    }
  });
  write('yaml.json', {
    bun: Bun.version,
    notes: [
      'YAML 1.2 core: yes/no/on/off are strings; 0755 is DECIMAL 755; 0o755 and 0x1f are ints; 0b101 and 1_000 are strings; .inf/.nan are numbers (JSON null).',
      'Integers beyond 2^53 lose precision (JS number); 9007199254740993 → 9007199254740992.',
      'JS object key order: integer-like keys are hoisted first in ascending order, then string keys in insertion order — see "key order is insertion order". An ordered map that does not hoist is byte-different from JSON.stringify on such a document.',
      'Duplicate keys: last wins, no error. Merge keys and aliases are expanded. A multi-document stream parses to an array.',
      '`#notacomment` as a plain scalar is a comment, so the value is null.',
    ],
    scalars: scalarRows,
    documents: docRows,
    invalid: invalidRows,
  });

  // The real files the parser meets: specs, compose files, the two templates. Stored as the JSON
  // Bun.YAML produces (key order preserved by JSON.stringify), which is what the Go parser must
  // produce byte-for-byte through the same serialisation.
  const corpus = [
    'packages/pstack/examples/preview.yml',
    'packages/pstack/examples/shared.yml',
    'packages/pstack/examples/docker-compose.preview.yml',
    'packages/pstack/examples/docker-compose.minimal.yml',
    'packages/pstack/templates/control/docker-compose.yml',
    'packages/pstack/templates/cloud-init.tpl.yaml',
  ];
  const corpusRows: Array<{ path: string; json: string } | { path: string; error: string }> = [];
  for (const p of corpus) {
    try {
      const text = readFileSync(join(REPO, p), 'utf8');
      corpusRows.push({ path: p, json: JSON.stringify(Bun.YAML.parse(text)) });
    } catch (e) {
      corpusRows.push({ path: p, error: String(e).split('\n')[0] ?? String(e) });
    }
  }
  write('yaml-corpus.json', { bun: Bun.version, files: corpusRows });
}

// ── JS coercions the contract depends on ─────────────────────────────────────────────────────────
{
  const numberInputs = ['', ' ', '12', ' 12 ', '1.5', '0x10', '0o17', '0b11', '1e3', '-0', 'abc', 'Infinity', '-Infinity', 'NaN', '1,000', '1_000', '+5', '.5', '5.', '0x', '12px'];
  const numbers = numberInputs.map((input) => {
    const n = Number(input);
    return { input, number: Number.isNaN(n) ? 'NaN' : n === Infinity ? 'Infinity' : n === -Infinity ? '-Infinity' : Object.is(n, -0) ? '-0' : n, isFinite: Number.isFinite(n), isInteger: Number.isInteger(n) };
  });
  // String(number) — what coerceEnv and asString produce, and what lands in a STACK NAME.
  const strings = [1, 1.0, 1.5, 1e21, 1e-7, 0.1 + 0.2, -0, 123456789012345680000, 2 ** 53, 2 ** 53 + 2, 100, 1e100, 0.000001].map((n) => ({
    input: JSON.stringify(n),
    string: String(n),
  }));
  // URLSearchParams, the query grammar every route reads through (last-wins, ';' literal, '+' is space, bad escapes literal).
  const queries = ['a=1&a=2', 'a=1;b=2', 'a=%zz', 'a=b+c', 'a=%20', 'a', 'a=', '=b', 'a=1&&b=2', 'a=x%2Fy', 'PR=7&token=t', 'a=%E2%9C%93'].map((raw) => {
    const p = new URLSearchParams(raw);
    return { raw, entries: [...p.entries()], lastWins: Object.fromEntries([...p.entries()]) };
  });
  // encodeURIComponent vs what Go's url.QueryEscape would do — the set that differs is what a
  // client_secret_basic credential or a ?next= value is built from.
  const encode = ['a b', "!'()*", '~', '/', '?', '&', '=', 'ü', '+', '%', 'se%cr+et/=', 'a;b'].map((s) => ({ input: s, encodeURIComponent: encodeURIComponent(s) }));
  // decodeURIComponent on path segments: what throws, what passes through.
  const decode = ['a%2Fb', 'a%zz', 'a%', '%E2%9C%93', 'a+b', '%C3'].map((s) => {
    try {
      return { input: s, decoded: decodeURIComponent(s), throws: false };
    } catch {
      return { input: s, decoded: null, throws: true };
    }
  });
  // new URL(): hostname normalisation the SSRF guard and the SSO loopback exception rely on.
  const urls = ['http://[::1]:8080/x', 'http://[::ffff:127.0.0.1]/', 'http://127.1/', 'http://0x7f.0.0.1/', 'http://2130706433/', 'http://LOCALHOST/', 'https://accounts.google.com', 'https://x.com/a/../b', 'http://user:pw@h.com:80/p?q#f', 'http://[fe80::1%25eth0]/'].map((u) => {
    try {
      const x = new URL(u);
      return { input: u, hostname: x.hostname, host: x.host, href: x.href, pathname: x.pathname, throws: false };
    } catch {
      return { input: u, throws: true };
    }
  });
  // UTF-16 .length vs bytes: the 300-char truncation and the bearer length compare.
  const lengths = ['abc', 'ü', '✓', '🚀', 'a🚀b'].map((s) => ({ input: s, jsLength: s.length, utf8Bytes: Buffer.byteLength(s), slice0_2: s.slice(0, 2) }));
  // Array.sort stability and localeCompare.
  const sort = {
    localeCompare: ['b', 'B', 'a', 'A', '_x', 'x-y', 'x_y', 'x.y', 'Foo.yml', 'bar.yml', 'é', 'e', 'z'].sort((x, y) => x.localeCompare(y)),
    byteOrder: ['b', 'B', 'a', 'A', '_x', 'x-y', 'x_y', 'x.y', 'Foo.yml', 'bar.yml', 'é', 'e', 'z'].sort(),
  };
  // Date.now().toString(36) shape for event ids; Math.random().toString(36).slice(2, 8) length range.
  const base36 = { sample: (1755000000000).toString(36), seq7: (7).toString(36), seq40: (40).toString(36) };
  write('coerce.json', { bun: Bun.version, numbers, strings, queries, encode, decode, urls, lengths, sort, base36 });
}

// ── JSON formatting ──────────────────────────────────────────────────────────────────────────────
{
  const values: Record<string, unknown> = {
    msEpoch: 1755000000000,
    float: 1.5,
    one: 1.0,
    big: 1e21,
    tiny: 1e-7,
    negZero: -0,
    html: '<a href="x">&\u2028\u2029</a>',
    unicode: 'ü✓🚀',
    nested: { z: 1, a: [1, 'two', null, true], m: { q: undefined, r: null } },
    undef: undefined,
    nan: NaN,
    inf: Infinity,
  };
  write('json-numbers.json', {
    bun: Bun.version,
    compact: JSON.stringify(values),
    pretty: JSON.stringify(values, null, 2),
    perKey: Object.fromEntries(Object.entries(values).map(([k, v]) => [k, JSON.stringify(v) ?? '<omitted>'])),
    note: 'undefined-valued keys are omitted; NaN/Infinity become null; no HTML escaping; key order is insertion order; ms epochs are integers.',
  });
}

// ── the hand-rolled HTML escaper, formatDuration and friends ───────────────────────────────────
{
  const esc = (s: string) => s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
  const escapeHostRegexp = (host: string) => host.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  write('esc.json', {
    esc: ['<a href="x">&\'</a>', 'plain', '&amp;'].map((s) => ({ input: s, output: esc(s) })),
    escapeHostRegexp: ['app-pr-7.example.com', 'a.b*c+d?e^f$g{h}i(j)k|l[m]n\\o', 'x-y_z'].map((s) => ({ input: s, output: escapeHostRegexp(s) })),
    shq: ["a'b", 'a b$c', '', 'plain', '`x`'].map((v) => ({ input: v, output: `'${v.split("'").join(`'\\''`)}'` })),
  });
}

console.log('facts written');

export {};
