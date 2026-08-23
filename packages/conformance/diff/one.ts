/** Run ONE scenario in this process (PSTACK_IMPL already pinned by the caller) and print its trace. */
import { Session } from '../harness/diff.ts';
import { bootServer } from '../harness/server.ts';
import { dockerShim } from '../harness/docker-shim.ts';
import { SCENARIOS } from './scenarios/index.ts';

const [name, dataDir] = process.argv.slice(2);
const sc = SCENARIOS.find((x) => x.name === name);
if (!sc || !dataDir) throw new Error(`unknown scenario ${name}`);
const shim = dockerShim(sc.shim ?? '', { record: true });
// The readiness watcher polls docker in the background, and its calls land in the shim's recording
// interleaved with the scenario's own — a nondeterministic argv trace. It has its own tests; here it
// only has to be quiet, so the poll interval is pushed past any run.
const s = await bootServer({
  dataDir,
  token: 'diff-token-0123456789abcdef0123456789abcdef',
  pathPrefix: shim.dir,
  keep: true,
  domain: undefined,
  readiness: { pollMs: 3_600_000 },
});
const session = new Session(s, shim);
try {
  await sc.run(session);
} finally {
  await s.stop();
}
const steps = session.finish();
shim.remove();
process.stdout.write(JSON.stringify(steps));
