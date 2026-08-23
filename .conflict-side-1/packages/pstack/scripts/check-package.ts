#!/usr/bin/env bun
/**
 * Assert the published package is not broken — without needing registry credentials.
 *
 * WHY THIS EXISTS, concretely: `@samyx/publish-kit@0.0.0-pre.1` shipped with
 * `files: ["dist"]` but an `exports` map still pointing at `./src/config/define.ts`. `src/` was
 * never in the tarball, so every consumer importing the documented subpath got
 * "Cannot find module". Packing succeeded; the package was simply unusable. That class of bug is
 * invisible to `bun test` and to `tsc`, and a `bun publish --dry-run` cannot run in CI because it
 * demands auth even in dry-run mode.
 *
 * So: pack for real (no auth required), then assert every entry point a consumer can reach —
 * `exports`, `bin`, `main`, `module`, `types` — is actually inside the tarball.
 *
 *   bun scripts/check-package.ts
 */

type Manifest = {
  name?: string;
  files?: string[];
  bin?: string | Record<string, string>;
  exports?: unknown;
  main?: string;
  module?: string;
  types?: string;
};

const manifest: Manifest = await Bun.file('package.json').json();

/** Every file path a consumer could resolve, flattened out of the manifest. */
function entryPoints(m: Manifest): { field: string; path: string }[] {
  const out: { field: string; path: string }[] = [];
  const walk = (node: unknown, field: string): void => {
    if (typeof node === 'string') {
      // Ignore bare specifiers and URLs — only relative paths are files in the tarball.
      if (node.startsWith('.')) out.push({ field, path: node });
      return;
    }
    if (node && typeof node === 'object') {
      for (const [k, v] of Object.entries(node)) walk(v, `${field}${k.startsWith('.') ? k : `.${k}`}`);
    }
  };
  walk(manifest.exports, 'exports');
  if (typeof m.bin === 'string') out.push({ field: 'bin', path: m.bin });
  else for (const [k, v] of Object.entries(m.bin ?? {})) out.push({ field: `bin.${k}`, path: v });
  for (const f of ['main', 'module', 'types'] as const) {
    if (m[f]) out.push({ field: f, path: m[f]! });
  }
  return out;
}

const proc = Bun.spawn(['bun', 'pm', 'pack', '--dry-run'], { stdout: 'pipe', stderr: 'pipe' });
const [stdout, stderr, code] = await Promise.all([
  new Response(proc.stdout).text(),
  new Response(proc.stderr).text(),
  proc.exited,
]);
if (code !== 0) {
  console.error(`pack failed:\n${stderr || stdout}`);
  process.exit(1);
}

// `bun pm pack --dry-run` prints one "packed <size> <path>" line per file.
const packed = new Set(
  stdout
    .split('\n')
    .map((l) => /^\s*packed\s+\S+\s+(.+)$/.exec(l.trim())?.[1])
    .filter((p): p is string => !!p),
);

if (packed.size === 0) {
  console.error('could not parse any packed files from `bun pm pack --dry-run` output:\n' + stdout);
  process.exit(1);
}

const eps = entryPoints(manifest);
let bad = 0;
for (const { field, path } of eps) {
  const rel = path.replace(/^\.\//, '');
  const ok = packed.has(rel);
  console.log(`  ${ok ? '✓' : '✗'} ${field.padEnd(22)} ${path}`);
  if (!ok) bad++;
}

console.log(`\n${packed.size} files packed, ${eps.length} entry point(s) checked`);
if (bad > 0) {
  console.error(
    `\n${bad} entry point(s) are NOT in the tarball. A consumer importing them gets ` +
      `"Cannot find module". Fix \`files\` or the path, then re-run.`,
  );
  process.exit(1);
}
console.log('every entry point is shipped');
