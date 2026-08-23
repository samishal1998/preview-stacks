/**
 * Read the same YAML files with BOTH parsers and report any file they disagree about.
 *
 * pstack reads your `preview.yml` and your compose file. Through 0.28.0 it read them with Bun's
 * YAML parser; from 0.29.0 with its own (`internal/yamlx`), because no Go YAML library resolves
 * scalars the way Bun does — `restart: no` has to stay the string "no", `0755` has to be 755 and
 * not octal 493. Two parsers reading one file can disagree, and when they do NOTHING FAILS: the
 * file still parses, pstack just quietly generates different labels than it used to.
 *
 * The unit tests pin ~70 hand-written snippets and six files from this repo. This runs the same
 * comparison over as many real files as you can point it at.
 *
 *   bun diff/corpus.ts <file|dir> [...]     # directories are walked for *.yml / *.yaml
 *   bun diff/corpus.ts ~/code /var/lib/pstack/deployments
 *
 * Reads only; parses nothing else, starts no server, touches no docker. Safe on a copy of a real
 * host's data directory.
 */
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { REPO } from '../harness/impl.ts';

const args = process.argv.slice(2);
if (args.length === 0) {
  console.error('usage: bun diff/corpus.ts <file|dir> [...]');
  process.exit(2);
}

/** Every *.yml / *.yaml under the given paths. Skips the places that are not anyone's YAML. */
function collect(paths: string[]): string[] {
  const out: string[] = [];
  const skip = new Set(['.git', 'node_modules', 'dist', 'bin', '.turbo', '.diff', '.status']);
  const walk = (p: string, depth: number): void => {
    let st;
    try {
      st = statSync(p);
    } catch {
      return; // a broken symlink, or something we may not read
    }
    if (st.isFile()) {
      if (/\.ya?ml$/.test(p)) out.push(p);
      return;
    }
    if (!st.isDirectory() || depth > 12) return;
    for (const e of readdirSync(p)) {
      if (skip.has(e)) continue;
      walk(join(p, e), depth + 1);
    }
  };
  // An explicitly named file is always included, even if it is inside a skipped directory.
  for (const p of paths) walk(p, 0);
  return [...new Set(out)];
}

const files = collect(args);
if (files.length === 0) {
  console.error('no .yml / .yaml files under those paths');
  process.exit(2);
}

/** The reference: Bun's parser, the one pstack used through 0.28.0. */
function withBun(path: string): { json?: string; error?: string } {
  try {
    return { json: JSON.stringify(Bun.YAML.parse(readFileSync(path, 'utf8'))) };
  } catch (err) {
    return { error: (err as Error).message };
  }
}

/** pstack's own parser, through a tool that prints one JSON line per file. */
async function withPstack(paths: string[]): Promise<Map<string, { json?: string; error?: string }>> {
  const bin = `${process.env.TMPDIR ?? '/tmp'}/pstack-yamlcat-${process.pid}`;
  const build = Bun.spawn(['go', 'build', '-o', bin, './packages/pstack/tools/yamlcat'], { cwd: REPO, stdout: 'pipe', stderr: 'pipe' });
  if ((await build.exited) !== 0) throw new Error(`go build failed:\n${await new Response(build.stderr).text()}`);

  const out = new Map<string, { json?: string; error?: string }>();
  // In batches: a few thousand paths would overflow the argument list.
  for (let i = 0; i < paths.length; i += 500) {
    const proc = Bun.spawn([bin, ...paths.slice(i, i + 500)], { stdout: 'pipe', stderr: 'pipe' });
    const [text, err, code] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
    if (code !== 0) throw new Error(`yamlcat failed:\n${err.slice(-2000)}`);
    for (const line of text.split('\n')) {
      if (!line) continue;
      const row = JSON.parse(line) as { path: string; json?: string; error?: string };
      out.set(row.path, { json: row.json, error: row.error });
    }
  }
  return out;
}

const pstack = await withPstack(files);
let agreed = 0;
let bothRejected = 0;
const differ: string[] = [];

for (const path of files) {
  const a = withBun(path);
  const b = pstack.get(path) ?? { error: 'no output' };
  const short = path.startsWith(REPO) ? path.slice(REPO.length + 1) : path;

  if (a.json !== undefined && b.json !== undefined) {
    if (a.json === b.json) {
      agreed++;
      continue;
    }
    differ.push(short);
    console.log(`✗ ${short}`);
    // The first differing run of characters, with a little context on each side.
    let i = 0;
    while (i < a.json.length && a.json[i] === b.json[i]) i++;
    const from = Math.max(0, i - 60);
    console.log(`    at char ${i}`);
    console.log(`    0.28.0: …${a.json.slice(from, i + 90)}`);
    console.log(`    0.29.0: …${b.json.slice(from, i + 90)}`);
    continue;
  }
  if (a.error !== undefined && b.error !== undefined) {
    // Both refuse it. The MESSAGES differ by design (a documented deviation); refusing agrees.
    bothRejected++;
    continue;
  }
  differ.push(short);
  console.log(`✗ ${short}`);
  console.log(a.error !== undefined ? `    0.28.0 rejects it: ${a.error}` : `    0.28.0 parses it`);
  console.log(b.error !== undefined ? `    0.29.0 rejects it: ${b.error}` : `    0.29.0 parses it`);
}

console.log(
  `\n${files.length} file(s): ${agreed} identical, ${bothRejected} rejected by both, ${differ.length} DIFFER`,
);
process.exit(differ.length ? 1 : 0);
