# cm-centipede UI — Design System

This app reuses cm-beetle's token names verbatim so a component can be ported
between the two dashboards without a rewrite. Only the values change.

## Palette

cm-beetle is emerald/teal (beetle carapace). cm-centipede is indigo/violet, so
the two dashboards are never mistaken for one another at a glance.

| Token | Light (default) | Dark |
|---|---|---|
| `--color-brand-primary` | `#6366f1` (indigo-500) | same |
| `--color-brand-secondary` | `#8b5cf6` (violet-500) | same |
| `--bg-main` | `#eef0f7` | `#09071a` |
| `--bg-panel` | `#ffffff` | `#1a1739` |
| `--bg-input` | `#f5f6fc` | `#110e28` |
| `--text-main` | `#15132b` | `#f4f5fb` |
| `--text-muted` | `#545a76` | `#a8aec9` |
| `--text-placeholder` | `#7d83a3` | `#6e7399` |
| `--border-main` | `#d5d8e8` | `#393471` |
| `--border-input` | `#8b91b0` | `#5b57a6` |

**Light is the default.** An unclassed `:root` is light, so the page is legible
before hydration and if the theme script never runs; the toggle adds `.dark`.

### Surfaces are a three-step ramp, and every step is opaque

`--bg-main` (page) → `--bg-panel` (card) → `--bg-input` (inset: inputs, badges,
progress tracks). Every screen is a single vertical column of stacked
full-width panels, so a panel has no neighbour beside it to mark its edge — the
surface step and its border are the only separation there is.

Both must therefore be stated as opaque values. Alpha over a near-black page
cannot produce a readable step: `--bg-panel: rgba(99,102,241,0.05)` and
`--border-main: rgba(99,102,241,0.12)` composite to a 1.04:1 surface step and a
1.07:1 border, so the cards and their outlines vanish and the whole screen reads
as one dark field.

The dark theme's steps, measured against shipped dark UIs as the reference
(GitHub dark is 1.09:1 surface / 1.42:1 border):

| pair | ratio |
|---|---|
| panel vs page | **1.16:1** |
| border vs panel | **1.55:1** |
| input border vs input | **3.01:1** (meets WCAG 1.4.11) |

Contrast ratio is a weak instrument for near-black surface steps — the +0.05
flare term dominates — so treat those first two numbers as parity checks, not
as WCAG results. Text contrast is separate and comfortable in both themes:
`--text-main` ≥ 15:1 and `--text-muted` ≥ 6.5:1 on every surface.

### A card inside a panel takes the third step

A card nested in a panel — the repeated connection forms in "New source", the
per-unit cards in the plan view, the target shortlist — would get the same
background as its parent from plain `.panel`, leaving a 1px border (1.55:1 on
dark, with the shadow off) to carry the whole separation. `.panel-inset` gives it
`--bg-input` instead, so the card is set apart by an area rather than a line.

It is a class in `globals.css`, not a `bg-*` utility, on purpose: utilities live
in a Tailwind layer, and the unlayered `.panel` `background` beats any layered
rule whatever the order.

### `dark:` follows the toggle, not the OS

`globals.css` declares `@custom-variant dark (&:where(.dark, .dark *))`. Without
it Tailwind v4 compiles `dark:` to `@media (prefers-color-scheme: dark)`, which
follows the operating system while the palette follows a class — so on a
light-preference OS the dark theme kept every light-mode accent
(`text-emerald-600`, `text-amber-600`, …) on a near-black panel.

Accent text is therefore always written as a pair, `text-indigo-600
dark:text-indigo-400`. A bare 400 shade fails on white (`indigo-400` is 2.98:1,
`rose-400` 2.69:1) and a bare 600 shade is muddy on the dark panel.

The pair is the rule; the light-side shade is a preference. `600` is the
default, and `500` is accepted where the text is bold, inside a tinted badge, or
not the only carrier of its meaning — the category accents, the danger button,
KPI deltas, the amber warning lists. Know what it costs: `sky-500` and
`amber-500` are under 3:1 on white, so a `500` shade is never the only thing
telling a reader something.

## Category accents

The three domain screens are near-identical by design, so colour is the only
immediate signal telling them apart. The same accent is used for the nav item,
the badge, the progress bar and the category chart bar.

| Category | Colour |
|---|---|
| File System | `sky-500` |
| Object Storage | `amber-500` |
| Database | `violet-500` |

`CATEGORY_ACCENT` in `src/components/ui/primitives.tsx` is the canonical map to
class names (text, tint, border, bar). Two shapes it does not cover keep their
own class strings: `CategoryBadge` in `status.tsx` and the active nav item's
`ACCENT_ACTIVE` in `AppLayout.tsx`. They must stay in the same hue family per
category; the exact shade may differ to suit the shape (the object storage badge
uses `amber-600` text where the accent map uses `amber-500`).

`globals.css` also declares the hexes as `--color-cat-filesystem`,
`--color-cat-objectstorage` and `--color-cat-database`. Nothing references them
yet — every call site uses Tailwind palette classes — so they are reserved, kept
in step with the table above.

## Type scale

`html { font-size: 112.5% }` sets the root, and `body` is `1rem` / `line-height:
1.6` on the system UI stack. Every `--text-*` token is in `rem`, so that one
percentage moves the whole ladder together.

It is a percentage and not `18px` on purpose: an absolute value here would
override a reader who has raised their own browser default, which is the very
person a larger scale is for. `body` is `1rem` for the same reason — an absolute
`16px` there would opt body out of the root scale and strand every unclassed
element behind it.

The `--text-*` tokens override Tailwind's own, one step up from its defaults and
below cm-beetle's enlarged scale. These screens pack paths, filter rules and ids
into a single column; at cm-beetle's 20px body most of that wraps or truncates.

| Class | Value | px @112.5% | Used for |
|---|---|---|---|
| `text-2xs` | 0.7rem | 12.6 | Table headers, captions above a bar, `required` marker, in-panel `UpdatedStamp` |
| `text-xs` | 0.85rem | 15.3 | Body text, hints, supporting copy, badges, `STEP n`, section band titles |
| `text-sm` | 0.95rem | 17.1 | Item names, form inputs |
| `text-base` | 1.05rem | 18.9 | Form panel headings |
| `text-lg` | 1.2rem | 21.6 | Modal headings |
| `text-xl` | 1.4rem | 25.2 | App header, `PageHeader` title |
| `text-2xl` | 1.7rem | 30.6 | — (reserved) |
| `text-3xl` | 2.05rem | 36.9 | Overview KPI figures |

### Keep the baseline real

The scale was originally sound but the *assignment* was not: 127 of 150 sized
elements sat at `text-xs` or `text-2xs`, only 6 above the body size, and 93 of
those small elements were monospace as well. The baseline never rendered
anywhere, so the whole UI read as fine print. When adding a screen, the default
step for anything a person reads is `text-xs`; `text-2xs` is the meta level —
it labels data (a column header, the caption above a progress bar) and is never
the data itself.

**`text-2xs` also has to stand on its own line.** Two steps below `text-sm` is
fine print when it sits beside it: the wizard's step header read `STEP 1` (12.6)
· `Source` (17.1) · `current` (12.6), and the badge and the number looked like
footnotes to the title. Anything inline with a `text-sm` name or title —
badges above all — is `text-xs`, one step down rather than two.

Four rules keep this honest:

- **No arbitrary sizes.** `text-[11px]` and friends do not appear in `src/`;
  every size is a named step. Check with
  `grep -rn 'text-\[' src/` — it must return nothing.
- **Define every step, even unused ones.** `@theme` extends Tailwind's default
  theme rather than replacing it, so deleting `--text-2xl` would not remove
  `text-2xl`; it would silently fall back to Tailwind's 24px and leave a hole in
  the ratio. A new step also needs its `--text-*--line-height` pair, or it emits
  `font-size` alone and inherits body's looser 1.6.
- **Monospace is for machine text, not for sentences.** Identifiers, paths,
  patterns, counts and API field names are `font-mono`; explanations, hints and
  status sentences are not. Mono prose reads slower and, stacked with a small
  step and `text-muted`, is what made the contract notes on the forms the least
  legible text on the screen.

Likewise `text-muted` marks a label, not a value. Where a row mixes the two —
`last scan …`, `conn 3/3 agent 3/3`, `267/412 items` — the label is muted and
the figure keeps full contrast.

## Layout rules

Everything except the nav flows top to bottom.

- One vertical column. No left rail, no master–detail split.
- List items expand **downward** (accordion); a collapsed item keeps a one-line
  summary.
- **One panel open at a time, and an open form is one of them.** Every accordion
  holds a single open id — the wizard's `openStep`, `RunsPage`'s `openId`,
  `SourceSection`'s `expandedId` — so opening one closes the last. Source groups
  used to allow three, which also meant three groups polling their connections
  at once; exclusivity bounds that at one. `SourceSection`'s add form takes part
  in the same rule rather than sitting outside it: it wears `.panel-open` like an
  expanded group does, so leaving both open would show two panels claiming to be
  the one being worked in. Opening the form collapses the list, and expanding a
  group closes the form.
- **Opening is not editing.** Expanding a finished panel to re-read it must cost
  nothing. The wizard used to invalidate from any completed step the reader
  clicked, so opening Step 1 to check which connection was scanned discarded the
  plan and the reference to a run that was already going — reading and editing
  were one gesture, and only one of them was ever meant. Invalidation is keyed to
  the inputs instead: `wizardStore`'s `edit` applies a change and discards what
  was derived from it, `patch` is for a write that derives nothing (navigation,
  and values mirrored back out of a query — cascading from those would let a
  background refetch throw away a plan). Because the emptying is then invisible,
  `edit` records what it took and the wizard says so above the steps.
- **The clicked row holds still.** Exclusivity has a cost: opening a card below
  the open one collapses that one, the page loses its height, and the card the
  reader just clicked slides up out from under the cursor.
  `useAnchoredAccordion` measures the clicked element's distance from the top of
  the viewport before the state change and restores it in `useLayoutEffect`, so
  the correction lands in the same frame and nothing flickers. All three
  accordions use it; a new one must too. (Native `overflow-anchor` does not
  cover this — it stands down around explicit user interaction.)
- **A box is centred on its label, and a hint never moves it.** `Radio` and
  `Checkbox` put the control and the label on one line of their own and drop the
  hint onto the line below, outside the `<label>`, indented to start where the
  label does. The obvious alternatives are both wrong: aligning the box to the
  top of a label-plus-hint block puts it above the centre of the very word it
  belongs to, and centring it against the whole block floats it between the two
  lines. Separating the lines lets each be aligned on its own terms.
- **A control in a horizontal row keeps the row one line tall.** A hint below the
  label makes that column taller than its neighbours, and everything in the row
  stops lining up. `inlineHint` puts the hint on the label's line for that case —
  `os_access_type`'s `ssh-tunnel`, and the rename rows in the DBMS mapping step.
  Only for a hint short enough not to wrap; a long one belongs on the line below,
  which costs nothing now that the box is centred either way.
- **Where the second line is conditional, so is the alignment.**
  `StepSection`'s summary renders only on a collapsed step, so its chevron picks
  `items-start` or `items-center` from that. Anything else with a line that comes
  and goes must do the same.
- **A tree inside a panel is not an accordion.** `UnitTree` draws a scan's units
  as the hierarchy they are; any number of its nodes may be open at once, and it
  opens the first two levels on its own (a node with more than 20 children stays
  closed). The one-open rule is about panels — what the reader is working in —
  not about branches of data inside one.
- Major regions are separated by a full-width `SectionBand`.
- Tabs are used in exactly one situation: lists that are alternatives to one
  another (VM nodes / object storage / RDBMS in the target picker).

### The panel you are working in

`.panel-open` marks an expanded accordion or an open form — one treatment for
both, so they read as the same thing: the brand colour on the border plus a 3px
ring.

- **Keyed to `open`, never to a status.** `StepSection` used to highlight
  `state === 'current'`, so a current step the reader had collapsed kept its
  highlight. The border says "this is open"; the `done` / `current` / `locked`
  badge says where the wizard stands. Two different questions.
- **Full-strength colour.** `border-indigo-500/30` composites to 1.48:1 on the
  light panel and 1.44:1 on the dark one, against 1.42 / 1.55 for an ordinary
  border — the colour was applied but never arrived. Opaque
  `--color-brand-primary` is 4.47:1 and 3.84:1, both past the 3:1 a UI boundary
  needs.
- **A ring, not a thicker border or a left bar,** so opening a panel moves none
  of its contents.
- The dark theme needs its own `:root.dark .panel-open` rule: `:root.dark
  .panel` zeroes the shadow at a higher specificity, and would otherwise erase
  the ring.

Weaker alpha rules survive where they are deliberately subordinate — the
`border-l-2 border-indigo-500/30` that marks a nested jump-host field group sits
*inside* an open form and should not compete with it.

## Components

`src/components/ui/primitives.tsx` holds the shared shapes: `Panel`,
`SectionBand`, `PageHeader`, `AccordionRow`, `Button`, `Badge`, `ProgressBar`,
`Note` and `ErrorNote`, `EmptyState`, `Loading`, and the freshness cluster
`LivePill`, `UpdatedStamp`, `RefreshButton`. `form.tsx` holds `Field`, the inputs
(`TextInput`, `Select`, `TextArea`, and `inputClass` for a control that is none
of those), `Radio` and `Checkbox` — split off when primitives went over the
400-line cap, and re-exported from `primitives.tsx` so every call site still
imports from one place. `status.tsx` holds the status vocabulary —
`StatusBadge`, `ValidationBadge`, `CategoryBadge`, `HealthBadge`, and
`categoryOfSummary`, which judges a run's category from its `planSummary`.
`JsonTree.tsx` renders a raw API payload as a collapsible tree.

## Placeholders

Placeholders are drawn in the same monospace face as the value that replaces
them, so left alone they read as something already typed. Three cues separate
them, and a placeholder must not rely on any one of them alone:

| cue | where |
| --- | --- |
| colour | `--text-placeholder`, ~3.4:1 on the inset surface against ~17:1 for entered text |
| `font-style: italic` | survives a washed-out panel and colour blindness |
| an `ex)` prefix | says in words that this is not a value |

A placeholder is one of exactly four things, and the wording follows from which:

- **An example value** — prefix it `ex)`: `ex) 127.0.0.1`, `ex) ubuntu`,
  `ex) public, sales`.
- **A statement about leaving the field empty** — a phrase, never a bare value:
  `(same name)`, `columns (empty = whole table)`.
- **The field's own name**, wherever boxes sit with no `Field` label at all —
  the rule editors (`table`, `object name`) and the vault key bar (`secret key`,
  `repeat`). Here the placeholder is the label. Where the format is the hard part
  rather than the identity — a glob, a SQL predicate — it shows an example
  instead, and takes the `ex)` prefix.
- **A prompt on a picker with nothing chosen** — a verb phrase ending in `…`:
  `select target…`. It asks for an action; it is not a value either.

Never prefix the second, third or fourth kind with `ex)`; `ex) (same name)`
claims the field accepts a literal that it does not.

Two things a placeholder is never:

- **A default stated as a bare value.** `placeholder="admin"` on `db_auth_source`
  read as a filled-in field, and the `Field` hint already said
  `empty defaults to "admin"` — so it was deleted, not relabelled.
- **Unreachable.** An input whose value falls back to a number (`rule.value ?? 0`)
  is never empty, so its placeholder can never render. Put the unit beside the
  box instead.

### One address, everywhere

Every example address is `127.0.0.1` — jump host, tunnel host, database host,
object storage endpoint alike. A placeholder's job is to be recognised as a
placeholder, and loopback cannot be mistaken for a host someone typed, the way
`10.0.1.30` and `172.24.78.163` could. It also means a reader who has seen one
example address has seen them all.

## Lint

`npm run lint` runs oxlint with the `@shadcn/lint` plugin
(`.oxlintrc.json`). Run it after any UI change; errors are design-system
violations and must be fixed, while React correctness findings are warnings
only.

- **Colour families.** Only the families this document gives a job are allowed:
  indigo/violet (brand), sky/amber/violet (category accents), emerald/amber/rose
  (status). Every other Tailwind family, neutrals included, is denied; neutral
  text, surfaces and borders go through the `text-*`, `bg-*` and `border-*`
  theme tokens. Giving a new family a job here means adding it to the deny list's
  complement in `.oxlintrc.json` in the same change.
- **No arbitrary values**, except widths and grid/flex tracks (`min-w-[..]`,
  `max-w-[..]`, `grid-cols-[..]`, `flex-[..]`). Anything else becomes a token in
  `@theme`: the uppercase meta labels use `tracking-label` (0.15em, `text-2xs`
  headings) and `tracking-section` (0.18em, the `SectionHeader` band).
- **No unknown classes.** A class Tailwind cannot generate is an error; declare
  it in `globals.css` (`@theme` or `@utility`) rather than ignoring it.
- `cx` is registered as the class-merge function, so classes inside `cx(...)`
  are checked too.

## Conventions

- Identifiers, paths, counts and API field names are rendered in `font-mono`;
  sentences about them are not (see the type scale above).
- Field labels use the backend's own spelling (`os_access_key_id`, `sshTunnel`)
  so what is on screen matches what goes on the wire.
- A screen never hides how fresh its data is: a page header carries the
  cluster `LivePill` (polling cadence) · `UpdatedStamp` (relative age, re-rendered
  every second from the query's `dataUpdatedAt`) · `RefreshButton`. Inside a
  panel, `UpdatedStamp small` drops to `text-2xs`.
- 400 lines per file is a target, not a gate. A file past it is split when a
  change already has it open and a natural seam exists — the way `form.tsx` came
  out of `primitives.tsx` — not in a refactor of its own. Wizard steps and
  stores, which hold one flow's state and handlers together, run past it most
  often.
