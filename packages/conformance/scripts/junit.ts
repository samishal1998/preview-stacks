/** Run the suite in a mode with bun's junit reporter and parse the per-test verdicts. */
import { mkdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { REPO } from '../harness/impl.ts';

export const PKG = join(REPO, 'packages', 'conformance');
export type Verdict = 'pass' | 'fail' | 'skip';
export type Case = { file: string; classname: string; name: string; verdict: Verdict };

export async function runJunit(impl: 'go' | 'null', files: string[] = []): Promise<{ cases: Case[]; exitCode: number }> {
  mkdirSync(join(PKG, '.status'), { recursive: true });
  const out = join(PKG, '.status', `${impl}.xml`);
  const proc = Bun.spawn(['bun', 'test', '--reporter=junit', `--reporter-outfile=${out}`, ...files], {
    cwd: PKG,
    env: { ...process.env, PSTACK_IMPL: impl },
    stdout: 'ignore',
    stderr: 'pipe',
  });
  const [stderr, exitCode] = await Promise.all([new Response(proc.stderr).text(), proc.exited]);
  let xml: string;
  try {
    xml = readFileSync(out, 'utf8');
  } catch {
    throw new Error(`bun test produced no junit file:\n${stderr.slice(-2000)}`);
  }
  return { cases: parseJunit(xml), exitCode };
}

export function parseJunit(xml: string): Case[] {
  const cases: Case[] = [];
  const re = /<testcase name="([^"]*)" classname="([^"]*)"[^>]*? file="([^"]*)"[^>]*?(\/>|>([\s\S]*?)<\/testcase>)/g;
  const unescape = (s: string) => s.replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
  for (const m of xml.matchAll(re)) {
    const inner = m[5] ?? '';
    const verdict: Verdict = /<skipped/.test(inner) ? 'skip' : /<failure|<error/.test(inner) ? 'fail' : 'pass';
    cases.push({ file: m[3]!, classname: unescape(m[2]!), name: unescape(m[1]!), verdict });
  }
  return cases;
}
