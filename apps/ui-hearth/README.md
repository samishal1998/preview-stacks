# @samyx/preview-stacks-ui-hearth

An alternative web UI for pstack, built on [Hearth UI](https://hearth-ui.samyx.net/llms.txt) rather
than ported from `apps/ui`: it uses the components Hearth ships for each job instead of rebuilding
the advanced UI's screens. It covers what `apps/ui` does and is selectable as a control-plane UI:
`pstack init --ui hearth`.

| Job | Hearth component |
|---|---|
| Frame, navigation, account | `HDashboardShell`, `HCommandPalette` (Ctrl/Cmd+K), `HTheme`, `HThemeSwitcher`, `HPopover` |
| Sign in | `HAuthPage` |
| Overview | `HPageHeader` (hero), `HStatCard`, `HSparkline`, `HTimeline` |
| Deployments list | `HDataTable` (sort, select, page, row actions), `HFilterBar`, `HChipGroup`, `HToolbar`, `HAlertDialog` |
| One deployment | `HResourceDetail`, `HDropdownMenu`, `HLogViewer`, `HCopyField`, `HSwitch`, `HSelect` |
| One job | `HResourceDetail`, `HStepper`, `HLogViewer`, `HProgress` |
| Feedback | `HToaster`, `HConnectionState`, `HAlert`, `HEmptyState`, `HSkeleton`, `HErrorPage` |

## Why a separate app, not a switch inside `apps/ui`

- A runtime switch would put two implementations of every view behind one router and ship both
  component libraries in one bundle. Neither UI could change without the other.
- The two already share what matters: the API, through `@samyx/preview-stacks-client`'s types. This
  app has its own thin `src/api.ts` and no code from `apps/ui`.
- Choosing one is a deployment choice: `pstack init --ui basic|advanced|hearth`, or `pstack ui hearth`
  on a running host. `hearth` runs this app's image as the same `advanced-ui` compose service.

## Run

```bash
PSTACK_TOKEN=… pstack serve        # 127.0.0.1:7878
cd apps/ui-hearth && bun run dev   # 127.0.0.1:5274, /api → 7878
```

`bun run build` type-checks and builds `dist/`.

## Deploy

```bash
# from a checkout, at the repo root
docker build -f apps/ui-hearth/Dockerfile -t pstack-ui-hearth:local .
# or, with a built dist/ (nginx.conf beside it)
pstack build-image --ui hearth --ui-dist apps/ui-hearth/dist

pstack init --ui hearth --domain <preview.example.com> --acme-email <you@example.com>
pstack ui hearth                   # switch an existing host
```

The image is `pstack-ui-hearth:local`; `PSTACK_UI_HEARTH_IMAGE` overrides it. A bare `--ui` on
`build-image` still builds the advanced image. Without `--ui-dist`, `build-image --ui hearth` fetches
`@samyx/preview-stacks-ui-hearth` from npm (published with each release).
