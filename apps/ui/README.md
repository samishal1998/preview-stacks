# @samyx/preview-stacks-ui

The **optional** advanced web UI for pstack. A Vue 3 SPA that talks to the same HTTP API as the
basic UI, served from its own container.

## Opt-in, and why there are two UIs

The API bundle already embeds a basic UI, so every install has a working interface with **no extra
container, no second image to build, and nothing that can drift out of step with the API it talks
to**. That is the right default for a host you touch once a month.

This one is richer — dashboards, tabs, live-following logs, keyboard navigation, light/dark — at the
cost of being a second artefact. So it is opt-in:

```bash
pstack build-image --ui   # nginx around this package, fetched from npm inside the build
pstack init --ui advanced
```

Nothing to install on the host: the package ships `dist/` already compiled, and the generated
Dockerfile `bun add`s it *inside* the build — the same trick `pstack build-image` uses for the
control image. This package being a BUILD-time input is the whole point; requiring it on the host
too once turned an optional UI into a boot failure. (`apps/ui/Dockerfile` still exists and builds
from source; that is what CI uses, and what you want when developing the UI itself.)

Docker images are not published yet. When they are, this becomes a `docker pull` and
`PSTACK_UI_IMAGE=<registry>/…`.

`init` then renders an `advanced-ui` service into the control stack and repoints
`control.<domain>` at it. **The API keeps serving the basic UI on `api.<domain>` either way**, so a
broken or missing advanced image degrades to a working interface on another hostname rather than
leaving the host with none.

## Development

```bash
# terminal 1 — the API, with the registry somewhere disposable
cd packages/pstack
PSTACK_DATA=/tmp/pstack-dev PSTACK_TOKEN=dev bun src/cli.ts serve

# terminal 2 — the SPA, proxied at /api to the above (see vite.config.ts)
cd apps/ui && bun run dev
```

Set the bearer token in **Settings** to match `PSTACK_TOKEN`; reads work without it, so the
dashboard renders before you do.

```bash
bun run build       # vue-tsc --build && vite build  → dist/
bun run typecheck
```

## Safety rules this UI encodes

These are not styling choices. Each mirrors a guarantee the API makes, and changing the UI to
contradict one would make the interface lie.

- **The control stack has no actions.** `GET /api/control` returns `actionable: false` and a note
  naming `pstack init` as its owner. The API runs *inside* that stack, so acting on it would kill
  the process doing the work. The note renders where an operator would otherwise hunt for buttons.
- **Masked values have no reveal control.** The server redacts secret-looking variables *before*
  responding — the plaintext was never sent to the page, so a reveal affordance would be a lie. The
  length is shown, because "is it set at all" is a real question.
- **`down` on a `kind: shared` deployment demands a typed confirmation** before it will send
  `force: true`, and states the blast radius: `compose down -v` destroys volumes every tenant
  depends on. The API independently refuses with 409 without `force`.
- **Variables persist per deployment.** The same variables must accompany a later `down` as the
  earlier `up`, or teardown resolves a *different stack* and orphans the first. The UI stores them
  and says so.
- **`leaked` is never styled like `failed`, and `unverifiable` is never styled like a pass.** A
  leak means a resource survived teardown and nothing else will clean it up; `unverifiable` means
  the axis has no `assert_gone`, so nothing was checked. Blurring either into "red" or "green" is
  precisely the false confidence pstack exists to remove.
- **`prefers-reduced-motion` is honoured**, with an explicit override in Settings for both
  directions.

## Layout

```
src/
├── api/          typed client + response types. One fetch wrapper; the token goes on
│                 POST/PUT/DELETE only, and 409 is handled distinctly everywhere it can occur.
├── composables/  control-plane state, polling (paused when the tab is hidden), settings,
│                 toasts, shortcuts, step classification
├── components/   small presentational pieces (badges, step list, var editor, toasts)
├── views/        one per route, with the deployment detail split into tabs/
└── styles/       tokens.css (the design system) + app.css
```

`src/composables/useSteps.ts` is the most important file: it is where a step becomes
`ok` / `unverifiable` / `leaked` / `failed`, in the same order the CLI's own report uses.

## The design system (0.13.0)

`styles/tokens.css` is the source of truth. Four rules worth knowing before editing anything:

- **A raised surface gets a catch-light AND a shadow, never one of them.** `--hairline` is an inset
  highlight one pixel tall along the top edge; the elevation shadows are tinted toward the page hue
  rather than neutral black. Drop either half and a panel flattens into a filled rectangle. Buttons
  use the same pair, and swap it for an inset shadow while pressed — pressed things do not catch
  light. Inputs are inset from the start: an input is a hole, a button is a thing you press, and
  giving them the same box makes a form read as a row of identical rectangles.
- **Tracking is part of the type scale.** `--track-display` through `--track-caps`: type spaced for
  15px looks slack at 36px, and small ALL-CAPS labels lose their word-shape and need the opposite.
  `font-variant-numeric: tabular-nums` is global — this page is mostly numbers in columns, and
  proportional figures make a column shimmer as it updates.
- **One focus ring, from `--ring`.** Two rings, the inner one page-coloured, so the accent stays
  legible on the page, on a panel, and on a coloured button alike. A ring that differs per control
  reads as an accident to the person least able to tolerate one.
- **Reduced motion also stops View Transitions.** They are generated outside the document tree, so
  the `*` selector never reaches them — the `::view-transition-*` rules are stopped explicitly.

Route changes use the **View Transitions API** where it exists (`data-vt` on `<html>`, set by the
router), and Vue's CSS transition everywhere else. Exactly one of them runs: Vue unmounting a node
the browser had already swapped for a snapshot throws on every navigation, so where the browser
animates, Vue does not manage the swap at all.

`components/CommandPalette.vue` is ⌘K. It scores by fuzzy **subsequence**, not substring, so `sdb`
finds `shared-db`; hover moves the cursor rather than painting a second highlight, because two lit
rows is the classic palette bug where Enter does something other than what the eye is on.
