import { describe, expect, test } from 'bun:test';
import GithubSlugger from 'github-slugger';
import { HEADING, broken, lines, plain, rewriteLine, type Page } from './sync-docs.ts';

const slug = (s: string) => new GithubSlugger().slug(s);

describe('heading text and anchors', () => {
  // negative control: drop the `site` dash replacement — the site slug keeps four dashes.
  test('smart punctuation: `--` is an en dash on the site, two hyphens on GitHub', () => {
    expect(slug(plain('Why -- force'))).toBe('why----force');
    expect(slug(plain('Why -- force', true))).toBe('why--force');
  });

  // negative control: strip tags before pulling code spans out — `<domain>` vanishes.
  test('code spans keep their content literally', () => {
    expect(plain('`api.<domain>` is public')).toBe('api.<domain> is public');
    expect(slug(plain('`api.<domain>` is public'))).toBe('apidomain-is-public');
    expect(plain('the `__init__` hook')).toBe('the __init__ hook');
  });

  // negative control: remove the `_x_` rule — the underscores stay in the slug.
  test('markup outside code is undone', () => {
    expect(plain('**Bold** and _em_ and [a link](x.md) &amp; more')).toBe('Bold and em and a link & more');
  });

  // negative control: go back to `\s*#*\s*$` — the C# heading loses its #.
  test('a closing # run needs a space before it', () => {
    expect(HEADING.exec('### Writing hooks in C#')![2]).toBe('Writing hooks in C#');
    expect(HEADING.exec('## Closed ##')![2]).toBe('Closed');
  });
});

describe('fences', () => {
  // negative control: close a fence on any fence line — the ~~~ inside the ``` block ends it early.
  test('a fence closes only on its own kind and length', () => {
    const l = lines('```\n~~~\n# not a heading\n```\n# heading');
    expect(l.map((x) => x.fenced)).toEqual([true, true, true, true, false]);
  });
});

describe('links', () => {
  const page: Page = { slug: 'reference/x', title: 'x', order: 0, source: 'docs/x.md', body: [], shift: 0 };

  // negative control: mask code with /`+[^`]*`+/ again — the lone backtick swallows the link after it.
  test('a code span holding one backtick does not hide the link after it', () => {
    // The span after the link is what the old pattern paired the stray backtick with.
    const out = rewriteLine('a `` ` `` then [AGENTS](../AGENTS.md) and `code`', page);
    expect(out).toContain('(https://github.com/samishal1998/preview-stacks/blob/main/AGENTS.md)');
  });

  // negative control: split on code spans before matching — the link with a code label is missed.
  test('a link whose label is code is still rewritten', () => {
    expect(rewriteLine('[`AGENTS.md`](../AGENTS.md)', page)).toBe(
      '[`AGENTS.md`](https://github.com/samishal1998/preview-stacks/blob/main/AGENTS.md)',
    );
  });

  // negative control: rewrite images to /blob/ — they point at GitHub's viewer page, not the file.
  test('an image points at the raw file', () => {
    expect(rewriteLine('![logo](../README.md)', page)).toBe('![logo](https://github.com/samishal1998/preview-stacks/raw/main/README.md)');
  });

  // negative control: return the url untouched without recording it — a broken link ships.
  test('a link to nothing is recorded, not passed through quietly', () => {
    const before = broken.length;
    rewriteLine('[gone](no-such-file.md)', page);
    expect(broken.length).toBe(before + 1);
  });
});
