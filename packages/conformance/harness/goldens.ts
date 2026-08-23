/**
 * Shared between the golden generator and its consumer: where goldens live, and the masks that
 * make a transcript reproducible across machines and implementations.
 */
import { join } from 'node:path';
import { REPO } from './impl.ts';
import { DATA_DIR, TOKEN } from '../gen/goldens.table.ts';

export const GOLDEN = join(REPO, 'packages', 'conformance', 'golden');

export type CliGolden = {
  name: string;
  argv: string[];
  env: Record<string, string>;
  cwd: string;
  code: number;
  stdout: string;
  stderr: string;
};

/**
 * Replace what legitimately varies: the data dir, the implementation's version, a generated token,
 * and the tmp root. Everything else is the contract.
 */
export function mask(text: string, version: string): string {
  const tmp = process.env.TMPDIR ?? '/tmp';
  // An EMPTY version would make `.split('')` shred the text into characters and rejoin them around
  // `<VERSION>`. That is what a filtered run (`bun test -t <case>`) used to produce, because the
  // version is captured by a test the filter removed — a diff of every character against itself.
  const withVersion = (t: string) => (version ? t.split(version).join('<VERSION>') : t);
  return withVersion(
    text
      .split(DATA_DIR).join('<DATA>')
      .split(`/private${DATA_DIR}`).join('<DATA>')
      .split(TOKEN).join('<TOKEN>')
      .replace(/PSTACK_TOKEN=[0-9a-f]{48}/g, 'PSTACK_TOKEN=<GENERATED_TOKEN>'),
  ).split(tmp.replace(/\/$/, '')).join('<TMP>');
}
