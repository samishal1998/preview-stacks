---
name: pstack-ui-verify
description: Look at a pstack UI change in a real headless Chrome, against a locally served pstack with a fake docker, and capture screenshots (1280/320, light/dark) plus DOM facts (heading, sideways overflow, console errors, focus). Use this whenever you change apps/ui (the advanced Vue UI) or packages/pstack/ui/index.html (the basic UI), whenever a review says "manual browser check not performed", "not verified in a browser" or asks for a screenshot, and before you call any UI work done. Also use it to check dark mode, the 320px layout, keyboard focus rings, the signed-out state, or what the Swarm, Control, Logging or Grafana views show, even if nobody asked for a browser check by name.
---

# pstack UI verify

Several defects in this UI's history were invisible in code review and obvious in a screenshot: a
stale "Healthy" beside "Exited", buttons that took a click and did nothing, columns painted over the
panel beside them (AGENTS.md, *UI work*). Review loops have also spun five fix rounds on "manual
browser check not performed". The fix for both is the same: serve the real binary, open the real
page, and look. This skill makes that one start command, one driver, and one stop command.

What it touches: a run directory under
`~/.claude/projects/-Volumes-S1-code-preview-stacks/ui-verify/<label>/` and processes on
`127.0.0.1`/`localhost`. It never writes to the repo, never uses a real docker daemon, and has no
VCS or outward-facing step. The token and admin password it sets are throwaway values for a loopback
server; never point any of this at a real host or put a real host's token in it.

Scripts (all in `/Volumes/S1/code/preview-stacks/.claude/skills/pstack-ui-verify/scripts/`; call
them by that absolute path, since a subagent's cwd resets between calls):

| Script | Does |
|---|---|
| `serve.sh start [label] [arms…]` | Builds `pstack` into the run dir, writes the docker shim, starts `pstack serve` (fresh data dir, bootstrap admin), vite for `apps/ui`, then headless Chrome. Waits until each answers, prints `run.env`. |
| `serve.sh stop [label]` | Kills all three as whole process groups; deletes `chrome/`, `data/` and the built binary, keeps logs, `shim/` and `shots/`. |
| `cdp.ts` | A small CDP driver: import it in a driver script, or `bun cdp.ts shots …` for the standard screenshot set. |
| `shim.ts <dir> [arms…]` | Writes the fake `docker` (serve.sh calls it for you). |

## 1. Start

```bash
bash /Volumes/S1/code/preview-stacks/.claude/skills/pstack-ui-verify/scripts/serve.sh start            # label "default", arms swarm loki plugins
bash /Volumes/S1/code/preview-stacks/.claude/skills/pstack-ui-verify/scripts/serve.sh start grafana-nav loki grafana
```

It takes ~10–30 s (a Go build, then three health waits) and returns once everything answers, so no
separate wait is needed. It prints `run.env`, which every later step reads:

```
API=http://127.0.0.1:<port>     pstack itself — also serves the BASIC UI
SPA=http://localhost:<port>     vite serving apps/ui live from source; proxies /api to API
CDP=http://127.0.0.1:<port>     headless Chrome's DevTools endpoint
TOKEN=… ADMIN_USER=admin ADMIN_PASSWORD=…   throwaway, loopback only
SHOTS=<run>/shots               where screenshots land
```

**Arms** pick what the fake docker claims exists (names from `packages/conformance`'s fixtures):

| Arm | The UI then shows |
|---|---|
| `swarm` | This node is a two-node swarm's leader: the Swarm page, join commands |
| `loki` | Logging on (the control stack has a `loki` container with its push-url label); serve.sh also seeds the Loki config so the settings panel has something to read |
| `grafana` | A `grafana` control container: the Grafana nav link |
| `plugins` | `docker node inspect`: node n1 has the loki log plugin, n2 does not (use with `swarm`) |

No arms means `swarm loki plugins` (golden case `swarm-status-loki`). Name only what you want for
other states, e.g. `serve.sh start plain swarm` for a host with logging off. With neither `loki` nor
`grafana`, the control stack has no containers and the dashboard's control card says so; that is the
fake host, not a regression. Everything else docker is asked exits 0 with no output, and every call is
appended to `<run>/shim/calls.log`; read it to see the exact argv the API issued for something you
clicked.

`start` refuses if that label is already running, and needs `bun install` done at the repo root. Ports
are picked free each run; set `API_PORT`, `UI_PORT`, `CDP_PORT` to pin them. Different labels can run
side by side.

The basic UI is embedded in the binary, so an edit to `packages/pstack/ui/index.html` needs a
restart (stop + start rebuilds). The advanced UI is vite from source: an `apps/ui` edit shows up on
the next `nav()` without a restart.

### Seeding state

A fresh server has no deployments. To see a deployment page, register one through the API with the
run's token. Hooks run in real `bash` on this machine, so keep them `true`; docker calls hit the shim.

```bash
source ~/.claude/projects/-Volumes-S1-code-preview-stacks/ui-verify/default/run.env
curl -sf -X PUT -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  "$API/api/deployments/demo" -d '{"spec":"version: 1\nstack: demo\naxes:\n  - name: db\n    up: \"true\"\n    down: \"true\"\n"}'
curl -sf -X POST -H "authorization: Bearer $TOKEN" "$API/api/deployments/demo/up"
```

## 2. Drive

**The standard set** — for "did anyone look at it": log in, then per path a full-page screenshot at
1280 and 320 wide, light and dark, with one JSON line of facts each.

```bash
bun /Volumes/S1/code/preview-stacks/.claude/skills/pstack-ui-verify/scripts/cdp.ts shots default spa / /control /swarm
bun /Volumes/S1/code/preview-stacks/.claude/skills/pstack-ui-verify/scripts/cdp.ts shots default basic /
```

Each line: `file`, `url`, `title`, `heading`, `width`, `overflowX` (true = the page scrolls sideways,
a defect at 320), `focused`, `errors` (uncaught exceptions, `console.error`, failed requests). A 401
on `/api/auth/me` while signed out is expected, not an error. The basic UI has a single dark theme, so
its light and dark shots are the same.

**Anything specific** — write a driver script in the run dir (not the repo, not `/tmp`) and run it
with `bun`:

```ts
// ~/.claude/projects/-Volumes-S1-code-preview-stacks/ui-verify/default/check.ts
import { open } from '/Volumes/S1/code/preview-stacks/.claude/skills/pstack-ui-verify/scripts/cdp.ts';

const p = await open('default');
try {
  await p.login(p.env.SPA);                          // p.env.API to sign in to the basic UI
  await p.nav(`${p.env.SPA}/control`);
  await p.waitFor((t) => document.body.textContent.includes(t), 'Logging');
  const text = await p.ev((h) => [...document.querySelectorAll('section.panel')]
    .find((s) => s.querySelector('h2')?.textContent === h)?.innerText ?? null, 'Logging');
  console.log(text);
  await p.viewport(320);
  await p.scheme('dark');
  console.log(await p.facts(), await p.shot('control-logging-320-dark', { full: true }));
} finally {
  await p.close();
}
```

The page object:

| Member | What it does |
|---|---|
| `env` | `run.env` as an object |
| `nav(url)` | Full navigation; waits for `load` plus 800 ms. Resets `errors`. |
| `waitFor(fn, …args)` | Polls `fn` in the page until truthy (10 s). Use it for SPA content instead of sleeps. |
| `ev(fn, …args)` | Runs `fn(...args)` in the page, returns the value (awaits promises). A string is a raw expression. |
| `login(base, user?, password?)` | Clears that origin's localStorage, `POST /api/auth/login` from the page. Defaults to the bootstrap admin. |
| `signOut()` | Clears every cookie and all storage for both origins. |
| `viewport(width, height = 900)` | Exact CSS width (`mobile: false`, so 320 means 320). |
| `scheme('light' \| 'dark')` | Emulates `prefers-color-scheme`. |
| `shot(name, { full? })` | PNG to `$SHOTS/<name>.png`, returns the path. `full` = the whole scrollable page. |
| `tab(shift?)`, `press(key, keyCode, modifiers?)` | Real key events (`Input.dispatchKeyEvent`). |
| `facts()` | url, title, heading, width, overflowX, focused element, errors. |
| `send(method, params)` | Any other CDP command. |
| `close()` | Closes the tab (Chrome keeps running until `serve.sh stop`). |

Useful checks:

```ts
// Keyboard focus ring: a real Tab, then ask the element.
await p.tab();
console.log(await p.ev(() => {
  const e = document.activeElement as HTMLElement;
  const s = getComputedStyle(e);
  return { el: e.outerHTML.slice(0, 80), focusVisible: e.matches(':focus-visible'), outline: s.outlineStyle, ring: s.boxShadow };
}));
// Then shot() it: a ring can be a box-shadow rather than an outline, so look rather than trust `outline`.

// Signed-out state: the SPA should land on /login.
await p.signOut();
await p.nav(`${p.env.SPA}/control`);
console.log(await p.waitFor(() => location.pathname === '/login' && location.href));
```

To exercise the login form itself (rather than signing in around it), fill it: the SPA's fields are
`#u` and `#p` inside a `form` (submit with `form.requestSubmit()`); the basic UI's are
`input[placeholder=username]` and `input[type=password]` with a `sign in` button. Set `.value` and
dispatch an `input` event so Vue's `v-model` sees it.

## 3. Look

Open every screenshot you will cite with the Read tool and look at it. A screenshot nobody looked at
proves only that Chrome can write a PNG. Check against `docs/ui-rules.md` (one control height,
sentence case, full-width pages, container-query tables) and the copy rule there: UI text states, it
does not explain.

## 4. Stop — always

```bash
bash /Volumes/S1/code/preview-stacks/.claude/skills/pstack-ui-verify/scripts/serve.sh stop default
pgrep -fl 'ui-verify/default|apps/ui/node_modules/.bin/vite' || echo clean
```

The path matches this run's `pstack` and Chrome; vite's command line carries only its `--port`, so a
vite listed there may belong to another run (compare the port with the `SPA` you had). Stop even
when a step failed; a leftover Chrome or vite holds its port and its CPU. `start` cleans up after
itself if it fails partway.

## 5. Report

Say what you looked at and what you did not. Give the screenshot paths (they are durable), the facts
lines that matter (any `overflowX: true` at 320, any `errors`), and anything the fake docker could not
show — real containers, real logs, real Loki queries, a real Grafana behind its sign-in — as
**not verified**. A review finding like "manual browser check not performed" is answered with those
paths and facts, not with "it looked fine".

## Gotchas (each one cost time before)

- **Leave the SPA's API base empty.** `apps/ui` stores an override in localStorage
  (`pstack.ui.apiBase`); a non-empty value sends requests past the vite proxy and login fails.
  `login()` clears storage first. Never set it to the API URL "to help".
- **`element.focus()` does not trigger `:focus-visible`.** Chrome shows focus rings for keyboard
  focus only; use `p.tab()`, which sends a real Tab key.
- **Never hand-assemble JavaScript in nested template literals.** It broke parsing before. Pass a
  function to `ev(fn, ...args)`: it must be self-contained (it sees its arguments and the page's
  globals, never your script's variables). If you do write a raw string, build it by concatenation.
- **Match text with `textContent`, not `innerText`.** `innerText` applies CSS `text-transform`, and
  this UI's panel headings are uppercased in CSS: `innerText` reads `LOGGING` where the source says
  `Logging`, and a `waitFor` on it times out.
- **The obscura browser MCP blocks localhost**, and the Claude-in-Chrome extension may be
  disconnected. Headless Chrome over CDP (this skill) is the reliable path; do not burn time on either.
- **Cookies ignore ports.** That is why the SPA is on `localhost` and the basic UI on `127.0.0.1`:
  two separate sessions. Signing in to one does not sign in to the other.
- **Fresh data every start.** The admin bootstrap only runs while no users exist, so a reused data dir
  would silently keep old credentials; `start` wipes `data/` for that reason. Seed state per run.
- **Never write run output to `/tmp` or the session scratchpad** — both are wiped on session restart,
  and a lost screenshot is a check that has to be redone. Everything here goes under
  `~/.claude/projects/-Volumes-S1-code-preview-stacks/ui-verify/`.
- **`prefers-color-scheme` only reaches the SPA while its theme setting is "system"**
  (`pstack.ui.theme` in localStorage), the default once storage is cleared. If you picked a theme in
  Settings, `scheme()` does nothing.
- **A dead end on a fake docker is not a UI bug.** If a view needs a docker answer the shim does not
  give (it exits 0 with no output for anything unlisted), `shim/calls.log` names the call. Add an arm
  to `<run>/shim/docker` above `*)`, written with `printf '%s\n'` the way
  `packages/conformance/harness/docker-shim.ts` documents; the shim is read on every call, so no
  restart. Or report the view as not verifiable here.
