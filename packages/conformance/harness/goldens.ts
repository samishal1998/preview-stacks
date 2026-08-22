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
  /** Source of the regexp whose lines diverge by design between TS and Go. */
  goDivergent?: string;
};

/**
 * Replace what legitimately varies: the data dir, the implementation's version, Bun's version
 * (stamped into cloud-init), a generated token, and the tmp root. Everything else is the contract.
 */
export function mask(text: string, version: string): string {
  const tmp = process.env.TMPDIR ?? '/tmp';
  return text
    .split(DATA_DIR).join('<DATA>')
    .split(`/private${DATA_DIR}`).join('<DATA>')
    .split(TOKEN).join('<TOKEN>')
    .replace(/PSTACK_TOKEN=[0-9a-f]{48}/g, 'PSTACK_TOKEN=<GENERATED_TOKEN>')
    .replace(/bun-v\d+\.\d+\.\d+/g, 'bun-v<BUN_VERSION>')
    .split(version).join('<VERSION>')
    .split(tmp.replace(/\/$/, '')).join('<TMP>');
}

/** Drop the lines that diverge by design, on both sides, before comparing in go mode. */
export function withoutDivergent(text: string, re: RegExp | undefined): string {
  if (!re) return text;
  return text
    .split('\n')
    .filter((l) => !re.test(l))
    .join('\n');
}
