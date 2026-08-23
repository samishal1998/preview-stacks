/**
 * A shell inside a preview container, over a WebSocket.
 *
 * ── THIS IS THE MOST DANGEROUS ROUTE IN THE PRODUCT ──────────────────────────────────────────────
 *
 * `docker exec` accepts ANY container name the daemon knows — Traefik, another PR's stack, and the
 * pstack control container itself, whose filesystem holds `pstack.db`: every password hash, every
 * session, every notifier signing secret. A caller-supplied container name that reaches `docker exec`
 * is therefore not "a terminal into a preview", it is root on the host's control plane.
 *
 * So the name is never trusted. The handler asks the deployment what containers it actually owns
 * (`deploymentRuntime` → `com.docker.compose.project=<stack>`) and matches the request against THAT
 * list; anything not in it is a 404. Quoting is not the defence here and never was — the argv form
 * below means there is no shell to quote for, and it would still be the wrong container.
 *
 * ── NO PTY, DELIBERATELY ─────────────────────────────────────────────────────────────────────────
 *
 * `Bun.spawn` has no pty, so a real terminal needs `docker exec -it` wrapped in util-linux `script`
 * (or a native pty binding). That wrapper's flags differ between util-linux and busybox and across
 * versions, and there is no Docker on the machine this was written on — shipping it would mean
 * shipping a command string nobody has ever run.
 *
 * `docker exec -i` covers what an operator actually opens a terminal for: `ls`, `cat`, `env`,
 * `psql`, `redis-cli`, a migration command. What it does NOT give is job control, curses UIs
 * (`top`, `vim`), readline editing, or ^C — the shell has no controlling terminal, so it prints no
 * prompt either. The UI says so out loud rather than leaving the user to conclude it is broken.
 *
 * ponytail: no pty. Upgrade path is `script -qec` (or a pty binding) VERIFIED ON A REAL HOST, at
 * which point the client's existing resize messages become `stty cols/rows` and nothing else moves.
 */

import type { Store } from './store.ts';
import type { Principal } from './auth.ts';

/** Shells worth offering. An argv element, never a shell string — but an allowlist is cheap and it
 *  keeps a typo from becoming a confusing "exec format error" from the daemon. */
export const SHELLS = ['sh', 'bash', 'ash', 'zsh', 'fish'] as const;
export type Shell = (typeof SHELLS)[number];

export function isShell(v: unknown): v is Shell {
  return typeof v === 'string' && (SHELLS as readonly string[]).includes(v);
}

/**
 * The command that opens the shell.
 *
 * An argv ARRAY, not a string: `Bun.spawn(['docker','exec',…])` execs directly with no shell in
 * between, so a container id or shell name cannot be re-split or expanded no matter what it holds.
 * `shq` exists for the `bash -c` path in `exec.ts`; here there is nothing to quote for.
 */
export function execArgv(containerId: string, shell: Shell): string[] {
  return ['docker', 'exec', '-i', containerId, shell];
}

/** Who did it, in one string, for the audit row. */
export function actorOf(who: Principal): string {
  if (who.kind === 'root') return 'root (PSTACK_TOKEN)';
  if (who.kind === 'share') return `share-link (${who.deployment})`;
  return who.user.username;
}

/**
 * May this principal open a terminal?
 *
 * Everyone is `role: 'admin'` today, so this is one line that does nothing yet — which is exactly
 * why it goes in now. When roles become real, the check that decides who gets a shell on the host
 * must already be in the code path, not a thing someone remembers to add.
 */
export function mayOpenTerminal(who: Principal): boolean {
  // A share link is read-only by construction; it never reaches a shell.
  if (who.kind === 'share') return false;
  return who.kind === 'root' || who.user.role === 'admin';
}

export class TerminalAudit {
  #store: Store;

  constructor(store: Store) {
    this.#store = store;
  }

  /**
   * Written at OPEN, not at close. A session that ends because the process died, the container was
   * removed, or pstack itself was killed must still have left a trace — an audit log that only
   * records tidy endings is an audit log that misses exactly the sessions worth auditing.
   */
  open(args: {
    actor: string;
    deployment: string;
    container: string;
    containerId: string;
    shell: string;
  }): number {
    const r = this.#store.db
      .query(
        `INSERT INTO terminal_sessions (actor, deployment, container, container_id, shell, started_at)
         VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
      )
      .get(args.actor, args.deployment, args.container, args.containerId, args.shell, Date.now()) as {
      id: number;
    };
    return r.id;
  }

  close(id: number): void {
    this.#store.db
      .query('UPDATE terminal_sessions SET ended_at = ? WHERE id = ? AND ended_at IS NULL')
      .run(Date.now(), id);
  }

  recent(limit = 100): Array<{
    id: number;
    actor: string;
    deployment: string;
    container: string;
    shell: string;
    startedAt: number;
    endedAt: number | null;
  }> {
    return (
      this.#store.db
        .query(
          `SELECT id, actor, deployment, container, shell, started_at, ended_at
           FROM terminal_sessions ORDER BY started_at DESC LIMIT ?`,
        )
        .all(Math.min(Math.max(limit, 1), 500)) as Array<{
        id: number;
        actor: string;
        deployment: string;
        container: string;
        shell: string;
        started_at: number;
        ended_at: number | null;
      }>
    ).map((r) => ({
      id: r.id,
      actor: r.actor,
      deployment: r.deployment,
      container: r.container,
      shell: r.shell,
      startedAt: r.started_at,
      endedAt: r.ended_at,
    }));
  }
}

/** What the upgrade handler attaches to the socket. Mutable: the process is spawned in `open`. */
export type TerminalData = {
  actor: string;
  deployment: string;
  containerId: string;
  containerName: string;
  shell: Shell;
  sessionId: number;
  argv: string[];
  proc?: {
    kill: () => void;
    stdinWrite: (data: string | Uint8Array) => void;
  };
};
