import { expect, test } from 'bun:test';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

// docs/ui-rules.md: a scroll container takes tabindex="0" (Safari never focuses an overflow scroller).
// negative control: drop tabindex="0" from ErrorNote's <pre class="raw"> and this fails naming it.
test('every scrolling pre and .sso-url is focusable', () => {
  const src = join(import.meta.dir, '../src');
  const files = readdirSync(src, { recursive: true }).map(String).filter((f) => f.endsWith('.vue'));
  const missing: string[] = [];
  for (const f of files) {
    const tags = readFileSync(join(src, f), 'utf8').match(/<pre\b[^>]*\bclass="[^"]*\b(raw|log|code|eqc-code)\b[^"]*"[^>]*>|<code\b[^>]*\bclass="[^"]*\bsso-url\b[^"]*"[^>]*>/g);
    for (const tag of tags ?? []) if (!tag.includes('tabindex="0"')) missing.push(`${f}: ${tag}`);
  }
  expect(missing).toEqual([]);
});
