# UI rules

The conventions the advanced UI is held to. They exist because the drift they prevent is invisible
one component at a time and obvious across a page: four button heights in one toolbar, three
spellings of "fully round", a table painted over the panel beside it.

Everything here is enforced in `apps/ui/src/styles/tokens.css` and `app.css`. If a rule needs a
magic number at a call site, the rule is missing a token.

## Casing

| What | Case | Example |
|---|---|---|
| Page and section headings | Sentence case | `Compose logs`, `Stored on the server` |
| Table column headers | Sentence case (acronyms keep their case) | `Service`, `URL`, `TLS` |
| Buttons, menu items, tabs | Sentence case | `Tear down`, `Show equivalent command` |
| **Values from the API** — kind, state, health, type, visibility | Sentence case **at render only** | `isolated` → `Isolated`, `start-failed` → `Start failed` |
| Identifiers — stack, container, deployment id, file, env var, host, path | **Never touched** | `shopfront-pr-7`, `DATABASE_URL` |

Render an API enum through `sentence()` in `composables/useFormat.ts`. Never capitalise the value
itself: it is compared, stored, sent and used in class names, and `state === 'Running'` is how a
badge starts looking right while a comparison silently stops matching.

Sentence case, not Title Case — `Start failed`, not `Start Failed`. Title Case on a status reads as
a proper noun, and these are descriptions.

## Alignment

- **One control height.** `--control-h: 34px` for everything interactive in a row — buttons, inputs,
  selects, select triggers, search boxes, checkboxes in a toolbar. `--control-h-sm: 28px` is the only
  other step. A toolbar mixing 27/32/34 reads as assembled from spare parts.
- **Toolbars centre their items** (`align-items: center`). A 22px pill and a 34px button sharing a
  top edge have their optical centres five pixels apart — that is what "these buttons are different
  sizes" usually is.
- **Never `align-self: end` on one control** to patch a height mismatch. Fix the height.
- **Label beside the control in a toolbar** (`.field.inline`), label above it in a form (`.field`).
  The stacking margin between consecutive `.field`s is reset inside `.row` and `.phead`; if you add a
  third kind of toolbar container, reset it there too or its second field drops 16px.

## Padding & spacing

- Spacing comes from `--s1`…`--s7` (4/8/12/16/24/32/48). No raw pixels, and nothing off the 4px
  grid — a stray `10px` is drift, not a decision.
- Gaps come from `gap`, not from margins on children.
- Panels own their padding (`.panel`); a component inside one does not add its own outer margin.

## Roundness

One scale, one spelling each:

| Token | Value | For |
|---|---|---|
| `--r0` | 6px | inline `code`, `kbd`, a checkbox's own box |
| `--r1` | 9px | controls: buttons, inputs, selects |
| `--r2` | 13px | grouped boxes inside a panel |
| `--r3` | 16px | panels |
| `--r4` | 20px | the shell's own floating surfaces |
| `--r-full` | 999px | pills and circles |

Never `50%` — it looks identical to `--r-full` on a square element and drifts the moment the element
stops being square.

## Width

The page is **full width**. `.view` sets no maximum: tables, log panes and routing grids are the
reason anyone opens this on a wide monitor, and capping the page made all of them scroll sideways
inside a column with a thousand pixels of empty space beside it.

What caps itself instead:

- **Prose and forms** — `--measure`, and `.settings-form` for input fields. Text past ~90 characters
  stops being readable.
- **Wide tables** scroll inside their own panel (`.panel > table`), never over the panel beside them.

## Tables

- `table.cards` collapses to cards under a **container** query, not a media query. The panel is what
  constrains the table — `.grid-2` can hand it a 320px track on a 1400px monitor — so a viewport
  query left a narrow panel with the full desktop layout and its columns painted over its neighbour.
- Every `td` carries `data-label` (used as the card-mode label). An actions cell carries
  `data-label=""`, which suppresses the label rather than emitting an empty one.
- Row actions go in a `.row-actions` cell: end-aligned, non-wrapping.

## Buttons

- `variant="primary"` for the one action a screen is about; `danger` for destructive; default for
  everything else; `ghost` inside dense rows and tables.
- Sizes are the button's own (`sm` or not) — **never** a `sm` button beside a full-size one in the
  same group.
- A destructive row action uses `ActionButton`'s `confirm`: the first click arms it, the label
  becomes the question, the second click acts. Not `window.confirm` (a native panel this app avoids
  everywhere else) and not a modal (page-blocking for a decision already on screen).
- A blocked control says **why**, in text a keyboard or screen-reader user can reach. `title` alone
  does not: a natively `disabled` button leaves the tab order, so its tooltip is mouse-only. Use
  `ActionButton`'s handling — `aria-disabled` plus a described reason — or put the reason beside the
  control. A `title` is a supplement, never the only copy of the reason.

## Accessibility

These are the rules the drift kept finding a way around. Each one is here because it broke.

- **One focus ring, and nothing may delete it.** `:focus-visible` sets `box-shadow: var(--ring)`
  globally. A class selector with a bare `box-shadow: none` has EQUAL specificity and, declared
  later, silently wins — that is exactly how every ghost button, container-list row and command
  palette row lost its ring. If a control must drop its shadow, scope it: `.x:not(:focus-visible)`.
- **Dialogs use reka's `DialogRoot`/`DialogPortal`/`DialogOverlay`/`DialogContent`.** Never a
  hand-rolled scrim with `role="dialog" aria-modal="true"` — that attribute promises a focus trap,
  focus restore on close, a scroll lock and Escape, and hand-rolled versions delivered none of them.
- **Error text reaches assistive tech or it does not exist.** Use `ErrorNote` (it carries
  `role="alert"`), or put `role="alert"` on the element yourself. Field-tied validation instead gets
  an `id`, `aria-describedby` on the input, and `aria-invalid`.
- **No ARIA is better than bad ARIA.** `role="tab"` promises an APG keyboard contract; two toggle
  buttons want `aria-pressed`. A `role="tooltip"` nothing references via `aria-describedby` does
  nothing at all.
- **Targets are at least 24×24.** A smaller visual box is fine — grow the target with a
  `::after { inset: -Npx }` overlay rather than the box.
- **A scroll container takes `tabindex="0"`.** Safari does not make overflow scrollers focusable, so
  without it a mouse-free user cannot scroll a log.
- **`display: block` on a table strips its roles.** `table.cards` card mode does exactly that, so
  those tables assert `role="table"/"rowgroup"/"row"/"cell"` in markup — CSS cannot restore what
  `display` removed.
- **Contrast is measured, not judged.** Body text 4.5:1, large text and control boundaries 3:1, in
  BOTH themes, against the surface the thing actually renders on. `--fg-mute` shipped for months at
  3.2:1 in light because nobody computed it.

## Chrome

Nothing decorative that does not solve a nameable problem — no divider between regions already
separated by space, no background on an element nothing scrolls behind, no shadow where nothing
floats. See the workspace `ux-minimal-chrome` rule; this UI follows it.
