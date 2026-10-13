# @samyx/preview-stacks-ui-hearth

An alternative web UI for pstack, built on [Hearth UI](https://hearth-ui.samyx.net/llms.txt). A first
slice: sign in, Deployments and Jobs, read-only. Private and unreleased; `apps/ui` stays the
advanced UI until this one covers what it does.

## Why a separate app, not a switch inside `apps/ui`

- A runtime switch would put two implementations of every view behind one router and ship both
  component libraries in one bundle. Neither UI could change without the other.
- The two already share what matters: the API, through `@samyx/preview-stacks-client`'s types. This
  app has its own thin `src/api.ts` and no code from `apps/ui`.
- Choosing one is then a deployment choice, like `pstack init --ui basic|advanced`: a third value
  that points the control stack at this app's image. Not wired yet; the Dockerfile and the init flag
  come with the first slice that is worth running instead of `apps/ui`.

## Run

```bash
PSTACK_TOKEN=… pstack serve        # 127.0.0.1:7878
cd apps/ui-hearth && bun run dev   # 127.0.0.1:5274, /api → 7878
```

`bun run build` type-checks and builds `dist/`.

## Next

Deployment page (overview, logs, danger), lifecycle actions, jobs with a live log, routing. Take the
component names from the Hearth index, not from memory.
