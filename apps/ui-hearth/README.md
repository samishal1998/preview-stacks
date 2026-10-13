# @samyx/preview-stacks-ui-hearth

An alternative web UI for pstack, built on [Hearth UI](https://hearth-ui.samyx.net/llms.txt) rather
than ported from `apps/ui`: it uses the components Hearth ships for each job instead of rebuilding
the advanced UI's screens. Private and unreleased; `apps/ui` stays the advanced UI until this covers
what it does.

| Job | Hearth component |
|---|---|
| Frame, navigation, account | `HDashboardShell`, `HCommandPalette` (Ctrl/Cmd+K), `HTheme`, `HThemeSwitcher`, `HPopover` |
| Sign in | `HAuthPage` |
| Overview | `HPageHeader` (hero), `HStatCard`, `HSparkline`, `HTimeline` |
| Deployments list | `HDataTable` (sort, select, page, row actions), `HFilterBar`, `HChipGroup`, `HToolbar`, `HAlertDialog` |
| One deployment | `HResourceDetail`, `HDropdownMenu`, `HLogViewer`, `HCopyField`, `HSwitch`, `HSelect` |
| One job | `HResourceDetail`, `HStepper`, `HLogViewer`, `HProgress` |
| Feedback | `HToaster`, `HConnectionState`, `HAlert`, `HEmptyState`, `HSkeleton`, `HErrorPage` |

Everything the advanced UI does beyond this — editing specs, routing, registries, settings, users,
the control stack — is still there and not yet here.

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

Submit and edit specs (`HFirstRunSetup` / `HSettingsPage` / `HFileUpload`), routing and domains,
registries, users and single sign-on, the control stack and Swarm. Take component names from the
Hearth index, not from memory.
