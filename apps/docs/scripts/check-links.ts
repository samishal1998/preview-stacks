/**
 * Runs after `astro build`: every link inside the site must reach a page that exists and, when it
 * names an anchor, a heading that page actually rendered.
 *
 * sync-docs.ts works out where each anchor lands when a document is split or moved; this is the
 * proof that it agrees with what Astro rendered. A disagreement — a heading slugged differently, a
 * page renamed — fails the build here instead of shipping a link that scrolls nowhere.
 */
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const DIST = join(import.meta.dir, '../dist/');
const BASE = '/preview-stacks/';

function pages(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) return pages(p);
    return name.endsWith('.html') ? [p] : [];
  });
}

const ids = new Map<string, Set<string>>();
const html = new Map<string, string>();
for (const file of pages(DIST)) {
  const route = BASE + relative(DIST, file).replace(/index\.html$/, '').replace(/\.html$/, '/');
  const text = readFileSync(file, 'utf8');
  html.set(route, text);
  ids.set(route, new Set([...text.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1])));
}

const broken: string[] = [];
for (const [route, text] of html) {
  // Only the page's own content: Starlight's chrome (sidebar, search) is generated, not written.
  const main = text.slice(text.indexOf('<main'), text.lastIndexOf('</main>'));
  const refs = [...main.matchAll(/<a\s[^>]*href="([^"]+)"/g), ...main.matchAll(/<img\s[^>]*src="([^"]+)"/g)];
  for (const [, href] of refs) {
    const raw = href.replace(/&amp;/g, '&');
    if (/^[a-z]+:/i.test(raw) || raw.startsWith('//')) continue;
    const [path, anchor] = raw.split('#');
    const target = path === '' ? route : path;
    if (!target.startsWith(BASE)) {
      broken.push(`${route} → ${raw}: outside the site's base`);
      continue;
    }
    const page = target.endsWith('/') ? target : target + '/';
    if (!ids.has(page)) {
      // Not a page: a file the site ships (an image, the favicon) must exist in dist.
      if (!existsSync(join(DIST, decodeURIComponent(target.slice(BASE.length))))) broken.push(`${route} → ${raw}: no such page or file`);
      continue;
    }
    if (anchor && anchor !== '_top' && !ids.get(page)!.has(decodeURIComponent(anchor))) {
      broken.push(`${route} → ${raw}: no heading #${anchor} on ${page}`);
    }
  }
}

if (broken.length) {
  console.error(`check-links: ${broken.length} broken link(s):\n  ` + broken.join('\n  '));
  process.exit(1);
}
console.log(`check-links: every link on ${html.size} pages resolves`);
