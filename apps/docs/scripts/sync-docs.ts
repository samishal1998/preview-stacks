/**
 * Generates the site's guide and reference pages from the documents that already live in the
 * repository, so the site and the repo cannot disagree. Runs before every `astro dev` and
 * `astro build`; everything it writes is gitignored. To change a generated page, edit its source
 * — the page's "Edit page" link goes there.
 *
 *   docs/usage.md                  → guide/*         one page per `## ` section
 *   docs/*.md, the two READMEs,    → operate/*, reference/*, start/design
 *   skills/pstack/SKILL.md
 *   `pstack --help` (its golden)   → reference/cli   a test pins the golden to the real binary
 *   the `pstack api` lock file     → reference/api   generated from openapi.yaml; CI keeps it fresh
 *
 * Links are rewritten so they keep working once a document is split or moved: an anchor in
 * usage.md resolves to whichever page its heading landed on, a link to another synced document
 * becomes a site link, and a link to any other file in the repo becomes a GitHub link. A link
 * that resolves to nothing fails the sync — a broken link in the docs is a bug on GitHub too, and
 * this is the one place that checks them all.
 */
import { existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, posix, resolve } from 'node:path';
import GithubSlugger from 'github-slugger';

const ROOT = resolve(import.meta.dir, '../../..');
const OUT = resolve(import.meta.dir, '../src/content/docs');
const BASE = '/preview-stacks';
const REPO = 'https://github.com/samishal1998/preview-stacks';

/** Where generated pages go. Everything here is deleted and rewritten on every run. */
const GENERATED = ['guide', 'operate', 'reference', 'start/design.md'];

/** `label` is the sidebar's short name; the page keeps the document's own title. */
type Standalone = { src: string; slug: string; title?: string; label?: string; order: number };

const STANDALONE: Standalone[] = [
  { src: 'packages/pstack/README.md', slug: 'start/design', title: 'Design', order: 3 },
  { src: 'docs/bootstrap.md', slug: 'operate/bootstrap', label: 'Bootstrap a host', order: 1 },
  { src: 'docs/tls-challenge.md', slug: 'operate/tls-challenge', label: 'Switch the TLS challenge', order: 2 },
  { src: 'docs/preview-ingress-address-leak.md', slug: 'operate/stuck-in-new', label: 'Tasks stuck in New', order: 3 },
  { src: 'docs/swarm-local-testing.md', slug: 'operate/local-swarm', label: 'Test a swarm locally', order: 4 },
  { src: 'docs/webhook-events.md', slug: 'reference/webhook-events', order: 3 },
  { src: 'docs/node-signals-design.md', slug: 'reference/node-signals', label: 'Node signals', order: 4 },
  { src: 'packages/client/README.md', slug: 'reference/client', title: 'Client SDK', order: 5 },
  { src: 'docs/control-plane.md', slug: 'reference/architecture', label: 'Architecture', order: 6 },
  { src: 'skills/pstack/SKILL.md', slug: 'reference/agent-skill', title: 'Agent skill', order: 7 },
];

const USAGE = 'docs/usage.md';

// ── reading markdown ─────────────────────────────────────────────────────────────────────────────

type Line = { text: string; fenced: boolean };

/** Lines, each marked with whether it sits inside a fenced code block (fence lines count as fenced). */
export function lines(src: string): Line[] {
  const out: Line[] = [];
  let fence: string | null = null;
  for (const text of src.split('\n')) {
    const m = /^\s{0,3}(`{3,}|~{3,})/.exec(text);
    if (fence === null && m) {
      fence = m[1];
      out.push({ text, fenced: true });
    } else if (fence !== null) {
      out.push({ text, fenced: true });
      if (m && m[1][0] === fence[0] && m[1].length >= fence.length && text.trim() === m[1]) fence = null;
    } else {
      out.push({ text, fenced: false });
    }
  }
  return out;
}

// A closing run of #s counts only after a space (CommonMark): `Writing hooks in C#` keeps its #.
export const HEADING = /^(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$/;

/** A code span: a run of backticks, its content, and a closing run of exactly the same length. */
const CODE = /(?<!`)(`+)(?!`)[\s\S]*?(?<!`)\1(?!`)/g;

const ENTITIES: Record<string, string> = { '&amp;': '&', '&lt;': '<', '&gt;': '>', '&quot;': '"', '&#39;': "'" };

/**
 * A heading's text as it renders — what GitHub and Astro slug. Code spans keep their content
 * literally (`<domain>`, `__init__`); links, images, tags, emphasis and entities are undone only in
 * the prose between them.
 *
 * `site` applies what GitHub does not and Astro does: smart punctuation. `--` renders as an en dash
 * and `---` as an em dash, both of which the slugger drops, so `Why -- force` is `#why----force` on
 * GitHub and `#why--force` on the site.
 */
export function plain(md: string, site = false): string {
  let out = '';
  let at = 0;
  const prose = (t: string) => {
    let x = t
      .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
      .replace(/<[^>]+>/g, '')
      .replace(/\*\*|__|\*/g, '')
      .replace(/(^|[^\w])_([^_]+)_(?=[^\w]|$)/g, '$1$2')
      .replace(/&(amp|lt|gt|quot|#39);/g, (e) => ENTITIES[e]);
    if (site) x = x.replace(/---/g, '\u2014').replace(/--/g, '\u2013');
    return x;
  };
  for (const m of md.matchAll(CODE)) {
    out += prose(md.slice(at, m.index)) + m[0].slice(m[1].length, m[0].length - m[1].length);
    at = m.index! + m[0].length;
  }
  return (out + prose(md.slice(at))).trim();
}

/** Drop a leading YAML frontmatter block (SKILL.md has one); the site writes its own. */
function stripFrontmatter(src: string): string {
  if (!src.startsWith('---\n')) return src;
  const end = src.indexOf('\n---\n', 4);
  return end < 0 ? src : src.slice(end + 5);
}

// ── pages ────────────────────────────────────────────────────────────────────────────────────────

export type Page = {
  slug: string;
  title: string;
  label?: string;
  order: number;
  source: string;
  body: Line[];
  /** Shift every heading up this many levels (a usage.md section's `###` becomes the page's `##`). */
  shift: number;
};

/** Where an anchor of a source document now lives. */
type Target = { slug: string; anchor: string };
const anchors = new Map<string, Map<string, Target>>();
/** The page a link to a whole source document lands on. */
const landing = new Map<string, string>();

/** A standalone document: its h1 (already lifted into the title) points at the top of the page. */
function register(source: string, pages: Page[], h1?: string) {
  const doc = new GithubSlugger();
  const map = new Map<string, Target>();
  anchors.set(source, map);
  if (h1 !== undefined) map.set(doc.slug(h1), { slug: pages[0].slug, anchor: '' });
  for (const page of pages) {
    const local = new GithubSlugger();
    for (const l of page.body) {
      if (l.fenced) continue;
      const h = HEADING.exec(l.text);
      if (!h) continue;
      map.set(doc.slug(plain(h[2])), { slug: page.slug, anchor: local.slug(plain(h[2], true)) });
    }
  }
  landing.set(source, pages[0].slug);
}

/** A usage.md section title without its number or version: `7b. Scale out (0.26.0)` → scale-out. */
function sectionSlug(title: string): string {
  const bare = plain(title).replace(/^\d+[a-z]?\.\s*/, '').replace(/\s*\([\d.]+\)\s*$/, '');
  return new GithubSlugger().slug(bare).replace(/-{2,}/g, '-');
}

/**
 * usage.md, one page per `## ` section; the part before the first one is the overview. Anchors are
 * worked out in the same pass: one slugger over the whole document reproduces GitHub's anchors
 * (duplicates numbered document-wide), and one per page reproduces what the site renders. The h1
 * and each h2 become a page's title, so their anchors point at the top of that page.
 */
function splitUsage(): Page[] {
  const doc = new GithubSlugger();
  const map = new Map<string, Target>();
  anchors.set(USAGE, map);
  let current: Page = { slug: 'guide/overview', title: 'Overview', order: 0, source: USAGE, body: [], shift: 1 };
  const pages = [current];
  let local = new GithubSlugger();
  for (const l of lines(readFileSync(join(ROOT, USAGE), 'utf8'))) {
    const h = l.fenced ? null : HEADING.exec(l.text);
    if (!h) {
      current.body.push(l);
      continue;
    }
    const text = plain(h[2]);
    const original = doc.slug(text);
    if (h[1].length === 1 && pages.length > 1) {
      // Only the document's title may be an h1. A later one would have nowhere to go: it is not a
      // page boundary, and folding it into the previous section would misfile everything under it.
      broken.push(`${USAGE}: h1 "${text}" after the first section — use ## to start a page`);
      continue;
    }
    if (h[1].length === 2) {
      current = { slug: 'guide/' + sectionSlug(text), title: text, order: pages.length, source: USAGE, body: [], shift: 1 };
      pages.push(current);
      local = new GithubSlugger();
    }
    if (h[1].length <= 2) {
      map.set(original, { slug: current.slug, anchor: '' });
      continue;
    }
    map.set(original, { slug: current.slug, anchor: local.slug(plain(h[2], true)) });
    current.body.push(l);
  }
  landing.set(USAGE, pages[0].slug);
  return pages;
}

function standalone(s: Standalone): Page {
  const body = lines(stripFrontmatter(readFileSync(join(ROOT, s.src), 'utf8')));
  let title = s.title;
  const first = body.findIndex((l) => !l.fenced && /^#\s/.test(l.text));
  let h1: string | undefined;
  if (first >= 0) {
    h1 = plain(HEADING.exec(body[first].text)![2]);
    title ??= h1;
    body.splice(first, 1);
  }
  const page: Page = { slug: s.slug, title: title ?? s.slug, label: s.label, order: s.order, source: s.src, body, shift: 0 };
  register(s.src, [page], h1);
  return page;
}

// ── links ────────────────────────────────────────────────────────────────────────────────────────

export const broken: string[] = [];

function siteLink(t: Target, from: string): string {
  if (t.slug === from) return t.anchor ? `#${t.anchor}` : '#_top';
  return `${BASE}/${t.slug}/${t.anchor ? `#${t.anchor}` : ''}`;
}

function resolveLink(url: string, page: Page, image = false): string {
  if (/^[a-z][a-z0-9+.-]*:/i.test(url)) return url; // http:, https:, mailto:
  const hash = url.indexOf('#');
  const path = hash < 0 ? url : url.slice(0, hash);
  const anchor = hash < 0 ? '' : decodeURIComponent(url.slice(hash + 1));
  const target = path === '' ? page.source : posix.normalize(posix.join(posix.dirname(page.source), path));
  const where = `${page.source} → ${url}`;

  if (anchors.has(target)) {
    if (!anchor) return siteLink({ slug: landing.get(target)!, anchor: '' }, page.slug);
    const t = anchors.get(target)!.get(anchor.toLowerCase());
    if (!t) {
      broken.push(`${where}: no heading "#${anchor}" in ${target}`);
      return url;
    }
    return siteLink(t, page.slug);
  }
  const abs = join(ROOT, target);
  if (target.startsWith('..') || !existsSync(abs)) {
    broken.push(`${where}: ${target} does not exist`);
    return url;
  }
  // An image needs the file itself; /blob/ serves GitHub's viewer page around it.
  if (image) return `${REPO}/raw/main/${target}`;
  const kind = statSync(abs).isDirectory() ? 'tree' : 'blob';
  return `${REPO}/${kind}/main/${target}${anchor ? `#${anchor}` : ''}`;
}

const LINK = /(!?)\[((?:[^\[\]]|\[[^\]]*\])*)\]\(\s*<?([^)\s>]+)>?(\s+"[^"]*")?\s*\)/g;

/**
 * Rewrites the links in a run of prose. Code spans are MASKED for the match, not split out: a
 * link whose label is code — [`openapi.yaml`](../…), the commonest kind in these docs — must still
 * be found, while markdown shown inside a code span must not be. Masking keeps every offset, so the
 * match positions apply to the original line.
 */
export function rewriteLine(text: string, page: Page): string {
  const masked = text.replace(CODE, (m) => m.replace(/[^`\n]/g, 'x'));
  let out = '';
  let at = 0;
  for (const m of masked.matchAll(LINK)) {
    const [whole, bang, label, url, titlePart = ''] = m;
    const start = m.index!;
    const original = text.slice(start + bang.length + 1, start + bang.length + 1 + label.length);
    out += text.slice(at, start) + `${bang}[${original}](${resolveLink(url, page, bang === '!')}${titlePart})`;
    at = start + whole.length;
  }
  return out + text.slice(at);
}

// ── writing ──────────────────────────────────────────────────────────────────────────────────────

function frontmatter(page: { title: string; label?: string; order: number; source?: string; description?: string; extra?: string[] }): string {
  const out = ['---', `title: ${JSON.stringify(page.title)}`];
  if (page.description) out.push(`description: ${JSON.stringify(page.description)}`);
  out.push(page.source ? `editUrl: ${JSON.stringify(`${REPO}/edit/main/${page.source}`)}` : 'editUrl: false');
  if (page.extra) out.push(...page.extra);
  out.push('sidebar:', `  order: ${page.order}`);
  if (page.label) out.push(`  label: ${JSON.stringify(page.label)}`);
  out.push('---', '');
  return out.join('\n');
}

function render(page: Page): string {
  // Links are rewritten a run of prose at a time, not a line at a time: a link's label may wrap onto
  // the next line (one in tls-challenge.md does, inside a blockquote).
  const body: string[] = [];
  let prose: string[] = [];
  const flush = () => {
    if (prose.length) body.push(...rewriteLine(prose.join('\n'), page).split('\n'));
    prose = [];
  };
  for (const l of page.body) {
    if (l.fenced) {
      flush();
      body.push(l.text);
      continue;
    }
    const h = HEADING.exec(l.text);
    prose.push(h && page.shift ? '#'.repeat(Math.max(2, h[1].length - page.shift)) + ' ' + h[2] : l.text);
  }
  flush();
  // Section dividers and blank lines at either end belong to the original document, not the page.
  while (body.length && /^(\s*|---)$/.test(body[0])) body.shift();
  while (body.length && /^(\s*|---)$/.test(body[body.length - 1])) body.pop();
  return (
    frontmatter(page) +
    `<!-- Generated from ${page.source} by apps/docs/scripts/sync-docs.ts. Edit the source, not this file. -->\n\n` +
    body.join('\n') +
    '\n'
  );
}

const written = new Set<string>();

function write(slug: string, content: string) {
  // Two sources mapping to one page would silently overwrite the first — its content gone from the
  // site, and links to it landing on the other.
  if (written.has(slug)) broken.push(`two pages map to ${slug}`);
  written.add(slug);
  const file = join(OUT, `${slug}.md`);
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, content);
}

// ── the two pages generated from build artefacts rather than prose ───────────────────────────────

function cliPage() {
  const src = 'packages/conformance/golden/cli/help.json';
  const version = JSON.parse(readFileSync(join(ROOT, 'packages/pstack/package.json'), 'utf8')).version;
  const help: string = JSON.parse(readFileSync(join(ROOT, src), 'utf8')).stdout.replaceAll('<VERSION>', version);
  write(
    'reference/cli',
    frontmatter({ title: 'CLI', order: 1, source: src, description: 'Every pstack command and flag, exactly as pstack --help prints them.' }) +
      [
        `This is \`pstack --help\` for ${version}, word for word. A test compares it with what the binary`,
        'prints, so it cannot drift. The [HTTP API reference](' + BASE + '/reference/api/) lists the `pstack api` commands,',
        'one per route.',
        '',
        '```text',
        help.trimEnd(),
        '```',
        '',
      ].join('\n'),
  );
}

type Flag = { name: string; source: string; type: string; required?: boolean; enum?: string[]; description?: string };
type Op = { operationId: string; method: string; path: string; command: string; summary?: string; flags?: Flag[] };

function apiPage() {
  const lockSrc = 'packages/pstack/internal/apicli/oascmd.lock.json';
  const specSrc = 'packages/pstack/api/openapi.yaml';
  const ops: Op[] = Object.values(JSON.parse(readFileSync(join(ROOT, lockSrc), 'utf8')).operations);
  const spec = Bun.YAML.parse(readFileSync(join(ROOT, specSrc), 'utf8')) as { tags?: { name: string; description?: string }[] };
  const cell = (s = '') => s.replace(/\|/g, '\\|').replace(/\n+/g, ' ').trim();
  const groups = new Map<string, Op[]>();
  for (const op of ops) {
    const group = op.command.split(' ')[0];
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group)!.push(op);
  }
  const order = (spec.tags ?? []).map((t) => t.name).filter((t) => groups.has(t));
  for (const g of groups.keys()) if (!order.includes(g)) order.push(g);
  const out: string[] = [
    `Every route the control plane serves, generated from [\`openapi.yaml\`](${REPO}/blob/main/${specSrc}) — the`,
    'route list of record. A test fails when the code serves a route the document does not describe, and',
    'every host also serves the document itself at `/api/openapi.yaml`.',
    '',
    'Call a route with a bearer token, or run it through the CLI: every route is a `pstack api` command.',
    '',
    '```bash',
    'curl -H "Authorization: Bearer $PSTACK_TOKEN" https://api.<domain>/api/deployments',
    'pstack api deployments list',
    '```',
    '',
    `${ops.length} routes in ${order.length} groups.`,
    '',
  ];
  for (const g of order) {
    const tag = spec.tags?.find((t) => t.name === g);
    out.push(`## ${g}`, '');
    if (tag?.description) out.push(cell(tag.description), '');
    // One small heading per route, not a table: the long paths took a table's width and squeezed the
    // description into a sliver. A heading also gives every route its own link.
    for (const op of groups.get(g)!.sort((a, b) => a.path.localeCompare(b.path) || a.method.localeCompare(b.method))) {
      const params = (op.flags ?? [])
        .map((f) => `\`--${f.name}\`${f.required ? '' : '?'}${f.enum ? ` (${f.enum.join(' \\| ')})` : ''}`)
        .join(' ');
      out.push(`### \`${op.method} ${op.path}\``, '', cell(op.summary), '', `\`pstack api ${op.command}\`${params ? ` ${params}` : ''}`, '');
    }
    out.push('');
  }
  write(
    'reference/api',
    frontmatter({
      title: 'HTTP API',
      order: 2,
      source: specSrc,
      description: 'Every route the pstack control plane serves, with its pstack api command.',
      // The groups, not all of the routes, in the page's contents list.
      extra: ['tableOfContents:', '  maxHeadingLevel: 2'],
    }) +
      out.join('\n'),
  );
}

// ── run ──────────────────────────────────────────────────────────────────────────────────────────

if (import.meta.main) {
for (const g of GENERATED) rmSync(join(OUT, g), { recursive: true, force: true });

const pages = [...splitUsage(), ...STANDALONE.map(standalone)];
for (const page of pages) write(page.slug, render(page));
cliPage();
apiPage();

if (broken.length) {
  console.error(`sync-docs: ${broken.length} link(s) in the repo's docs resolve to nothing:\n  ` + broken.join('\n  '));
  process.exit(1);
}
console.log(`sync-docs: ${pages.length + 2} pages generated from the repository's docs`);
}
