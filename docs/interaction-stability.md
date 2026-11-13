# Interaction stability

Interaction stability is a property of the console as a whole, not a
styling preference. It is what lets someone supervising an agent act
quickly without double-checking every click.

## Definition

The console is interaction-stable when, from the user's point of view:

> the only things that change are the things they just caused, where they
> caused them, in the way they expected; and nothing they have built up —
> position, focus, input, view state, work — is lost or put at risk by
> that change or by anything the system does on its own.

It serves three outcomes. Every invariant below exists for at least one.

| Outcome | The user can say |
|---|---|
| **Safety** (安全感) | "I cannot do harm by accident, and harm I do was confirmed or can be undone." |
| **Continuity** (连续感) | "After any action I carry on from exactly where I was." |
| **Predictability** (可预测感) | "I know what will happen before I act, and I can see that it did." |

## Invariants

Each invariant is stated so it can be checked. IDs are referenced from code
and tests.

### S — the spatial frame holds

- **S1 Targets keep their box.** Hover, press, focus, open and selected
  states are paint-only: colour, background, shadow, outline. Never
  transform, size, spacing, border width, font metrics or `content`. An
  element never moves under a stationary pointer because of its own state.
- **S2 Floating layers stay on their anchors.** A menu or popover opens
  once at its anchor and does not follow it afterwards; together with S1
  the anchor never moves while the layer is open.
- **S3 Effects are local.** An action changes only the region it targets.
  Content before it and beside it does not move. New content appears after
  the control or in space reserved for it; a removed item leaves a
  same-size placeholder (e.g. an undo row) rather than collapsing under the
  pointer.
- **S4 Labels and counters keep their width.** A label that changes with
  state reserves the width of its longest option (`SwapLabel`); ticking
  numbers use tabular figures.
- **S5 The system never moves content under the user.** Polling and
  background refreshes do not insert above what the user is looking at or
  pointing at, do not reorder rows and do not collapse expanded items. New
  items arrive where the user is not, or behind an explicit "N new"
  affordance.
- **S6 The viewport moves only when asked.** Programmatic scrolling only
  reveals what the user just asked for and respects reduced motion. Opening
  an overlay does not shift the page.

### C — context survives

- **C1 Focus is never dropped.** Overlays return focus to their opener.
  After a removal, focus moves to the placeholder or a neighbour, never to
  `<body>`. Async completion does not steal focus.
- **C2 Input is never discarded implicitly.** Drafts survive view switches,
  refetches and re-renders. A dialog with unsaved input does not close on
  an outside click or Escape without confirmation. A refetch never
  overwrites a field being edited. Autosaved editors keep a persistent
  status (Saved / Saving… / Unsaved / Couldn't save · Retry) that is never
  blank and never claims Saved for a write that did not land (also relates
  to F3).
- **C3 View state survives data.** Expansion, selection, filters, tabs and
  scroll position persist across refetches; a refetch keeps showing the
  previous data instead of flashing back to a loading state.
- **C4 Places are addressable.** View state worth returning to (tab, path,
  focused run) lives in the URL; Back returns to it.

### P — cause and effect are predictable

- **P1 Acknowledge at once, at the control.** Within ~100 ms the control
  itself shows the press or the pending state. A pending action cannot be
  fired twice.
- **P2 Same pattern, same primitive, same keys.** Buttons, menus, dialogs,
  tabs and swapping labels each have one implementation in
  `web/src/components/ui.tsx`. Escape closes the topmost layer, arrows move
  within menus and tabs, Enter submits.
- **P3 No hidden side effects.** An action does what its label says and
  nothing else; it navigates only if the label says so. The system never
  acts on the user's behalf (no auto-grants, no auto-removal).
- **P4 Motion only explains.** Only entrances and exits of what the user
  opened animate, briefly (≤ 420 ms), never on hover, and not at all under
  `prefers-reduced-motion`.
- **P5 Outcomes appear where the user looks.** Success and failure show next
  to the control that caused them. A failure keeps the inputs and leaves the
  control where it was.
- **P6 Hierarchy is carried by structure, not prose.** A section is a bounded
  block (card with a title bar), not a line of small text. A block's primary
  action sits in its title bar, styled primary; destructive actions use the
  danger style and are spatially separated from primary ones. Instructional
  text is a last resort: if a control needs a sentence to be used correctly,
  change the control.

### F — mistakes are hard and recoverable

- **F1 No accidental activation.** Nothing arrives under a stationary
  pointer (S1, S3, S5). In particular, removing a row must not slide the
  next row's destructive control under the pointer, so a double click can
  never act twice. The gesture that opens a menu cannot select an item.
- **F2 Destructive actions are confirmed in the product or undoable.** The
  confirmation names the object. Prefer undo for frequent actions, an
  in-product confirmation for rare irreversible ones.
- **F3 State is honest.** Pending, failed and stale states are visible; no
  optimistic success without rollback.

## How it is guaranteed

Every invariant has an owner mechanism, in order of preference:

1. **Primitive** — the only sanctioned way to build the pattern makes the
   invariant hold by construction (`Button`, `Menu`, `Modal`, `SwapLabel`
   in `ui.tsx`; surfaces in `material.css`).
2. **Static check** — `web/src/interactionStability.test.ts`, run by
   `pnpm -C web lint` and `pnpm -C web test`. Fails on code that cannot be
   stable.
3. **Browser property test** — `web/e2e/*.spec.ts` measures the rendered
   result (boxes per frame, focus, preserved input) rather than asserting
   markup.
4. **Review** — what cannot be automated is checked against this list in
   review; a gap found twice graduates to 1–3.

| Invariant | Mechanism | Status |
|---|---|---|
| S1 | `material.css` paint-only states; static CSS/Tailwind check; e2e "controls keep their box under hover and press"; notebook directory active chip (`aria-current` tint + name colour only, whether set by the scroll-spy or an explicit pick), the Code|Graph segmented control's active segment and the State/Files tab active state reuse paint-only styles — e2e "S1: scroll-spy highlight is paint-only", "S1/S3: a chip click highlights immediately; a user scroll hands back to the spy", "S1: view-toggle segment boxes are identical active/inactive"; `completion.spec.ts` wrapped completion row geometry stays stable across keyboard selection | enforced |
| S2 | shared `Menu` (Radix); S1; e2e "the More menu stays where it opened" | enforced |
| S3 | fixed reveal slot (`RevealNew`); the notebook's one-line status slot (`role="status"`, ellipsis) carries every run/lifecycle/block/next notice — its `title` holds the full untruncated text — and per-cell run notes sit in the alerts slot; the notebook's floating panel head (toolbar + fixed-height directory rows inside one opaque rounded surface pinned 8px under the console header, its header gap masked) is one fixed overlay — cards scroll beneath via `scroll-margin-top`; the directory strip and the disabled Run all/Run stale controls render even while the notebook is empty, so the head and toolbar keep their box when the first section lands and the first card takes the placeholder's slot; nested radii are concentric and circular (`inner = outer − edge gap`, gap equal on both axes, via `calc()` from shared custom properties); the run page's progress card reserves its measured waiting height through the running phase (`RunDetail` layout effect + `ResizeObserver`, run-id guarded); chart displays reserve their box from the spec's declared size before the lazy vega chunk loads (`chartReservedBox` + `ChartView`, min-height so growth for legends/tooltips is possible but the box never shrinks); e2e "palette controls stay put", run editor placement test, notebook "run feedback never moves the cell", "section directory stays visible while a long notebook scrolls", "jumping to a section lands the card below the sticky head", "floating head box is constant across scroll, view switch and a run", "S3/F1: the first Add section keeps the head, toolbar and card slot in place", "toolbar spacing holds and nested radii are concentric" | partial — builder, detail and notebook covered; chart reservation covers pre-render layout, post-render growth still possible |
| S4 | `SwapLabel` and `RevealNew`; inserter rows keep a fixed height; static label check; e2e "Show latest" and "Show N new", notebook "cell controls keep their box under hover" | enforced for state/request-driven labels and counters; module workbench autosave status, Publish preparation and Run use `SwapLabel`; notebook controls are the ghost/xs scale except the seg-filters Code|Graph toggle |
| S5 | `useHeldOrder` + "Show N new"/"Show latest"; `stableList` unit tests; e2e activity, instance list and notebook sections; the directory keeps held order inside the sticky head (the side-column file list is not polled — refresh is explicit — so it needs no hold) | enforced for polled lists |
| S6 | scroll-locked `Modal`; e2e "opening a dialog does not shift the page" | enforced for dialogs |
| C1 | `Modal`/`Menu` return focus, removal falls back to a neighbour; e2e primitives and removal tests | enforced for overlays and removals |
| C2 | `Modal` asks before Escape, the close control or an outside click discards input; create flow keeps configuration mounted behind Back; application and instance notebook/activity view panels stay mounted; the side column's State/Files panels stay mounted behind `hidden` (e2e "State/Files tabs keep the file browser mounted"); section drafts autosave through a serial CAS queue with tab-local pending backup and a beforeunload guard, and a 409 conflict keeps the draft behind an explicit reload/keep-draft choice; `applicationWorkbench.spec.ts` and `notebook.spec.ts` preserve code, inputs and invalid JSON/drafts; `completion.spec.ts` Escape preserves typed parameter text | enforced for dialogs, create flow, view switching, live-state tabs and notebook drafts |
| C3 | `useHeldOrder` keeps shown order across refetches; application and notebook view panels retain editor DOM; the mounted State/Files tabs preserve the file browser's directory and selection and the variables filter across switches; chart views re-embed only when the serialized spec changes, so polls of the same execution keep zoom/brush state (`ChartView` effect keyed on spec JSON); `notebook.spec.ts` holds a dirty draft across polls and Notebook↔Activity switches | partial — polling, view continuity, live-state tabs, notebook drafts and chart interaction state covered; other scroll/selection by review |
| C4 | create steps are addressable as `?step=config`; application views use `?view=invoke` / `?view=activity`, with browser history coverage | partial — create steps and application views are addressable |
| P1 | pending guards; static `mutate` check; e2e removal test | partial — async forms by review |
| P2 | `ui.tsx` primitives; static browser-dialog check; e2e primitives test | enforced for buttons, menus, dialogs and labels |
| P3, F3 | application autosave indicator; notebook SaveStatus (Saved/Saving…/Unsaved/Couldn't save/Conflict) never claims Saved for an unlanded write, Run awaits pending saves and is blocked by an unresolved conflict; `notebook.spec.ts` autosave/conflict cases | partial — application indicator and notebook save/run honesty covered |
| P6 | app-card blocks on the application editor; danger Button variant; review | review — application editor and lists follow it |
| P5 | policy editor footer reports validation and save failures; notebook one-line status plus per-cell `.nb-note` for single-section run blocks/errors/restores; e2e "policy editor reports…", notebook status/note assertions | partial — policy editor and notebook covered |
| P4 | `material.css` motion block; static duration/reduced-motion check | enforced in CSS, not measured |
| F1 | S1, S2, `Confirm`; e2e menu and removal tests | enforced for menus and removals |
| F2 | `Confirm`; static destructive-control check | enforced for destructive controls; module/extension removal, section delete, once-section rerun (toggle lives in the cell ⋯ menu), conflict reload and recovery discard use `Confirm`; review-dialog discard has a dirty-note guard |

## Known gaps

- **S3:** only the instance builder and detail page measure local effects;
  other pages are covered by review.
- **S5/C3:** only the instance list and activity hold their order while
  polling. Expansion, selection, filters and scroll position are review-only.
- **P3/F3:** notebook save/run honesty is covered by `notebook.spec.ts`;
  other actions are review-only.
- **P5:** the policy editor reports outcomes at its footer; other forms and
  actions are review-only.
- **C4:** notebook sections are addressable as `?view=notebook&section=name`;
  other view state is not yet consistently addressable in the URL.
- **S6:** measured for dialogs only, not for menus or popovers.
- **P6:** no static check; followed by review.
- **Module workbench coverage:** module editor + caller script + stdout
  are one page (no unmount/state loss, C). Serial CAS autosave retains
  newer edits across acknowledgements; tab-local pending backups and a
  beforeunload guard protect unsaved source/debug settings (C2).
  `moduleAutosave.spec.ts` holds a PATCH while editing again, verifies
  the exact reviewed/published source, dependency-only add/remove
  reviews and recovery across SPA navigation. Run waits for module and
  debug autosaves and captures their returned versions; config edits
  mark the displayed result outdated (P). The reviewed-publish dialog
  keeps the note on 409 and only
  re-previews on explicit Refresh (P2/C2); ext secret selection writes
  the vault-ref environment default via the shared `SpecForm` editor —
  no manual secret names (C4 review).
  Static extension dependencies live inside the same `SpecForm`
  (catalog left, selected summary right) as instance/application
  configuration: catalog Add routes to the requirements CAS, summary
  Remove routes to the named dep `Confirm` (F2) — never the local
  remove/undo path — and a confirmed removal prunes the dep's debug
  config and environment bindings (`moduleCredentials.spec.ts`).
  Dep add/remove locks on a source-draft save conflict or a pending
  local-draft recovery while runtime configuration stays editable;
  publish busy/review and a running debug lock the whole form.
- **P1:** pending guards are enforced on `Confirm` and by the static
  `mutate` check; longer inline forms are review-only.

### Application editor (EP-0014)

`/applications/:id` is covered by `e2e/application.spec.ts` against a
live Vite dev server (React StrictMode) proxying a scratch dev API. The
page has three addressable views (P6): Develop (workbench and Settings),
Invoke (published-version calls), and Activity (Runs). Shared Radix tabs
hide inactive panels without unmounting their editors or inputs (C2/C3).
- `applicationWorkbench.spec.ts` verifies CodeMirror DOM identity,
  draft/call input and invalid Settings JSON survive view switches,
  browser Back restores the selected view, and hidden controls are absent
  from the accessibility tree.
- Develop has a directory-style Resources pane to the left of the editor.
  It lists configured extension aliases with operation documentation and
  mounted module aliases with nested `.star` files. Module files come
  from the referenced published version, never the mutable library draft;
  mismatched pins and unavailable versions show errors instead of falling
  back (F3). Reference browsing never inserts or executes code, changes the
  spec, or renders credentials. Native disclosures keep expansion state
  across view and viewport changes; mobile stacks the pane above code.
  Module files and extension operations open read-only references in the
  main editor frame; the sidebar contains navigation only. The draft
  editor remains mounted, and Run always executes main.star (C2/F3).
  The resource/frame top and bottom edges are measured by the workbench
  browser test; both panels scroll internally above the separate test area.
- Develop's title bar holds its actions — Run and Publish (primary),
  with `SwapLabel` Run/Running… and Publish/Publishing… (S4). There is
  no Save button: edits autosave 1200 ms after the last change (only
  shapes the PUT would accept) and Cmd/Ctrl+S saves immediately, both
  through the same snapshot pipeline as Run/Publish. The page header
  keeps a persistent `SwapLabel` save indicator — Saved / Saving… /
  Unsaved / Couldn't save — with a `Retry` button that appears after
  the indicator only while a save has failed (S3); a conflict (409)
  keeps the Reload Confirm flow instead (F3).
- Run and Publish still save first and abort on a mid-flight edit with
  "Changed while saving"; a conflicting save offers a reload Confirm
  instead of overwriting.
- The Invoke card is a separate call layer: its own input and output,
  not shared with Develop's test fields. Its Invoke button is always
  rendered and merely disables until the app is ready, so its
  appearance never moves neighboring controls. "Create API key" in its
  title bar mints an application-bound key in a `Modal`.
- Delete is a danger-styled action in the page header; list pages carry
  row-level Delete (applications) and End (running instances), each
  through `Confirm` with tone danger (F2).
- S1: state styles are paint-only; no transform/size on hover/press.
- Draft state is scoped by user + workspace + application id; navigating
  between apps remounts a clean editor under a different scope key.
- Mobile (<768px) stacks with no horizontal overflow; screenshots
  `application-ui-{desktop,mobile}.png`.
- Coverage is the browser suite plus static/unit geometry checks, not
  frame-level measurement.

Known gaps in the editor: there is no version history beyond the
published release — autosave cannot undo a bad edit except by reverting
to the published code manually; and in-app navigation is unguarded — the
console uses a plain `BrowserRouter` (no `useBlocker`), so `beforeunload`
protects only real page unloads while a draft is unsaved.

Application creation keeps the optional capability/extension form mounted
when collapsed (C2). Application-mode forms omit instance lifetime fields
and allow only vault references. Raw Advanced JSON is submitted without a
browser-number round trip.

Application Settings also carries a Modules library (`ModulePicker`,
same fixed-geometry palette as `ExtensionPicker`, S1/S3): adding a
published module writes a pinned `{name,version,sum}` mount under a
generated `@alias` (≤32 mounts, `self` reserved), and the selected
summary shows the mount alias, pin and a `load("@alias/…")` example.
Unpublished modules are visible but not addable; a malformed
`spec.modules` value is never rewritten by the visual editor — it is
left for Advanced JSON (C2/F3). Removal goes through the named
`Confirm` and leaves a same-slot placeholder with Undo (S3/F1/F2).
`applicationModule.spec.ts` covers add → pinned draft spec → reload →
Run/published invoke, proves a later library publish does not move the
pinned reference, and asserts confirmed removal drops only the named
mount.

Invocation examples use Radix keyboard tabs and the shared `CopyBlock`
with `SwapLabel` feedback (P2/S4). Their code panel has a fixed height
and local horizontal scrolling (S1/S3). Copy failures are visible without
discarding text (P5). Snippet unit tests capture the actual curl arguments,
Python request bytes and JavaScript fetch body without network access;
all preserve the exact JSON input, including large integers.

### Create flow: basics → configuration

Creating an instance or application starts with a two-column page:
basics (name, description and spec labels) on the left and, on the
right, an equal-height guide that explains the kind being created (a
persistent agent playground or a published API service). The flow is
fixed to that kind from entry to create — there is no
Instance/Application switch inside it. The second step is the existing
capability builder and shows the basics as a read-only summary, not as
a second form; its Inputs section skips the labels rows the basics
step owns.

The diagrams describe the real execution split: an instance keeps one
live process, so successive runs share globals and `/work`; an
application serves a published release where every invoke starts from a
fresh session and returns JSON under the pinned schemas.

- **S1**: the guide, step rail and diagram are paint-only; the step's
  current dot changes colour only. The dashed flows are the only motion.
- **P4**: the flow animation lives behind
  `prefers-reduced-motion: no-preference` and completes a cycle in
  420 ms (the static check in `interactionStability.test.ts` enforces
  both; e2e "guide diagram moves and stops under reduced motion"
  checks the rendered animation and packet visibility).
- **C2**: after the first entry the configuration stays mounted while
  Basics is shown, so Back never discards capability, policy or advanced
  JSON work. Basics edits are pushed into that mounted draft explicitly
  (`SpecForm.applyBasics`) instead of remounting it. e2e
  `application.spec.ts` "guide -> configuration carries basics, Back
  keeps them".
- **C4**: the step is in the URL (`/applications/new?step=config`,
  `/instances/new?step=config`), so direct links and Back land on the
  intended step. `from=` cloning keeps its source parameter.

Description has no dedicated server column; both kinds persist it as the
reserved `labels.description` entry, which `draftFromSpec` treats as a
dedicated field rather than a user label row.

### Interrupts (EP-0016)

- **S3/S4**: the application Develop/Invoke output region gains streaming
  affordances inside fixed geometry: the output pre is a fixed 180px box,
  the status slot is a reserved constrained-width tabular-nums flex slot, and the `Confirm`-gated
  Stop button occupies a constant slot (hidden via visibility, never
  unmounted). e2e `applicationStream.spec.ts` measures head/pre/Stop/meta
  bounding boxes across initial, prefix and completed phases. Develop's
  input and output live inside the editor frame, with an icon-only Run
  pinned to the editor toolbar's right edge. The resource column shares
  the frame's top and bottom in initial, running and terminal states.
  The same e2e checks aligned fields, square Run hit targets, stacked
  resources at 820px and mobile overflow.
- **S3/S4**: the nav inbox badge is a reserved-width tabular-figures slot that renders empty at zero (`InboxBadge`); the Develop/Invoke progress area (`RunProgress`/`useRunProgress`: waiting → running → terminal, `runPhase`) occupies the same output region as the result and the run page keeps its Waiting card box through the running phase — `RunDetail` measures the section while an interrupt is pending (layout effect + `ResizeObserver`, recorded per run id so a route change never carries another run's height) and applies it as `minHeight` only during the running phase; a row resumed since the last reveal keeps its box as an inert "Resolved by … · View run" placeholder (`keepGone` in `useHeldOrder`/`reconcile`, single-interrupt fetch fills the resolver label, "No longer waiting · View run" while it lands) until the reveal affordance — which appears whenever placeholders exist even with zero arrivals — clears it. e2e `interrupts.spec.ts` pins the run GET to the real waiting snapshot until the resume POST is accepted, then serves a synthetic running phase so the `run-progress` box is measured within 1px on desktop and 390px mobile without racing the terminal state, releases the route to verify the real succeeded output, and measures a resumed row's box across polling.
- **S5/C3**: inbox and application Runs tables use `useHeldOrder` + `RevealNew`; selections live in `?interrupt=` so polling can't drop them (C4 deep links fetch by id outside the page).
- **C2**: ResumeForm drafts (event text) are keyed by user+workspace+interrupt (`resumeDraftKey`) and survive polling/refetch/navigation; confirms never discard input.
- **P1/S4**: SwapLabel Resume/Resuming…/Resumed — running handler…; inputs disable while a resume is in flight and stay disabled while the handler runs.
- **P5/F3/C2**: server `already_resolved` renders "Already resumed by <label> at <time>" in place with typed input preserved; form never claims success on failure.
- **F2**: Resume, Cancel run and Retry step all pass named Confirms that show the exact event JSON (and the handler it calls) or consequence text.
- **P3**: resolving never navigates; lists and run pages refetch in place.
- **P6**: interrupts carry no presentation fields — rows label as `→ handler`; the application Settings card has no handler declarations.

### Instance notebook (EP-0022)

`/instances/:id` defaults to the Notebook view — an ordered directory of
durable section cards — with Activity as a separate tab; both panels
stay mounted so editors, drafts and scroll survive switches and polls
(C2/C3). `e2e/notebook.spec.ts` runs against a live Vite dev server
proxying a scratch dev API + worker.

- **S3/S5**: every notice that used to be a banner or report inserted
  above the cells — lost/expired/restored, run blocks, the batch report,
  the next-steps list — now lives in one sticky toolbar row inside the
  floating head (`top` sits under the console header). The row's
  one-line status slot (`role="status"`, ellipsis) picks the first
  match of running → restore → blocked → report → lifecycle →
  next → section count; its `title` carries the FULL text — every
  blocked message, every `name · status — reason` batch row, every
  next item, one per line — so nothing is hidden behind a modal (there
  is no Details button any more). Per-cell run feedback for a
  single-section run — block messages, the 409, generic errors and the
  trigger restore — lands on that cell's alerts slot as `.nb-note`
  (P5) and is cleared at the next run start; a failed batch section's
  error stays visible on its own card's output block
  (`a failed section shows its error on its card after a batch run`
  e2e). The `S3: run feedback
  never moves the cell` e2e measures the second cell's run button and
  the toolbar before/after a run.
- **S5**: polled section arrivals and reorders wait behind "Show N new"
  (`useHeldOrder` + `RevealNew`); a user-caused create/move/delete adopts
  the refetched order once it lands instead of hiding behind the reveal.
  The held-order e2e adds a section server-side and asserts the card
  stays hidden until the explicit reveal, then checks server order. The
  directory is a borderless chip strip (`flex-wrap: nowrap;
  overflow-x: auto`) so its height never changes on poll.
- **C2/F3**: each card autosaves code/description/rerun through a serial
  revision-CAS queue (800 ms debounce, latest-wins). The 409 retry
  captures the notebook revision BEFORE re-reading the section, so an
  agent write landing in that gap conflicts instead of overwriting
  unseen code (`save CAS gap` e2e: remote code and the local draft both
  survive). Run flushes ALL pending saves (the dependency closure can
  pull in any section), reads the notebook fresh for its revision,
  re-checks `once` against FRESH meta for a single explicit target (a
  stale outline cannot skip the confirmation — the Notebook-level
  Confirm covers the discovered case; upstream `once` sections re-run
  without a prompt), then asks the server for a `dry_run` plan. Only
  the plan's `planned` sections will execute, so the displayed-vs-saved
  fingerprint proof covers exactly that set — a registered autosave
  queue must exist for each (sections held behind "Show N new" or still
  loading block the run), the queue must be clean, and a per-section
  detail read must fingerprint-match BOTH the displayed draft and the
  last saved value — an agent edit landing before the revision capture
  is caught here, not executed (`run validation` + `run: an agent edit
  to an upstream section blocks the run` e2e: zero real run POSTs,
  draft preserved). The real POST reuses the same revision; a 409
  `graph_blocked` surfaces the server's diagnostic verbatim (not the
  generic revision text — `run: graph_blocked` e2e), other 409s keep
  the conflict wording. Queue state is
  re-checked after the awaited reads, and a forced 409 surfaces a
  persistent Conflict state that resolves only through explicit Reload
  server copy / Keep my draft — rendered in the cell's alerts slot
  AFTER the editor so a late conflict never moves what is being typed
  (the conflict read is also revision-first). Dirty drafts are backed
  up to tab-local storage (`pendingDraft`) with a beforeunload guard;
  the polling e2e holds a PUT in flight across polls and a
  Notebook↔Activity switch and verifies the draft survives.
- **P3**: runs execute saved code only — an unresolved save blocks the
  run with a named notice rather than silently running stale code. One
  shared run lock serializes every run control in the same tick —
  section and batch runs set it before their first await, and the
  server op lease remains the authoritative backstop. Every console
  execution is a section run — there is no scratch runner. Each cell's
  gutter carries an icon run control and ⌘/Ctrl+Enter inside a section
  editor runs THAT section (capture-phase); the `shortcut` e2e asserts
  the chord posts `/notebook/run` and never a scratch `/exec`.
- **F2**: section delete and explicit rerun of a succeeded `once`
  section go through `Confirm`; marking a section run-once lives in the
  cell's ⋯ menu ("Mark as run-once" / "Unmark run-once", fixed label per
  saved value) and shows as an `once` tag in the header — the
  `once toggle lives in the cell menu` e2e asserts the tag and the
  server `rerun` meta; the once e2e asserts the dialog before the
  second run.
- **P2**: inside `.nb` every control is a ghost/xs button (26px, icon
  + label or 26×26 icon square) — no primary fills, no default-size
  buttons; modal and Confirm footers keep the standard scale. State
  styles repaint only (S1 — the `S1: cell controls keep their box under
  hover` e2e measures gutter run, ⋯ trigger and inserter).
- **Lifecycle honesty**: a lost instance shows "Runtime lost · code is
  retained" in the status line; the first run restores an EMPTY process,
  returns a typed `instance_restored` error, and never auto-retries —
  the status line and the cell note both say the request did not run
  (the e2e counts exactly one run POST). A mid-batch loss returns an
  honest partial report (`restored: true`, `stopped_reason:
  instance_restored`) with earlier results retained and the rest
  `not_run` — the status line warns that earlier sections DID execute
  and its `title` keeps the full report, including the "do not
  blindly retry" warning (`partial restore` e2e, mocked report). Outputs
  are always visible when present (no disclosure widget), capped at
  240px; outputs from a previous incarnation are marked "previous
  runtime", and outputs whose code changed since the run are marked
  "code changed since run". Expired instances are read-only history:
  the status line says so and no run/edit affordances exist — there is
  no fake restore action.
- **Graph view**: a Code|Graph segmented control (`?nbview=graph`, C4)
  swaps the cards for a fixed-height dependency graph fed by
  `notebook.graph` — React Flow renders the dagre-laid-out sections
  and their provider→consumer variable links, per-section
  reads/issues in a side aside; both panels stay mounted so editors,
  drafts, autosave queues and scroll survive switches and polls
  (C2/C3 — `graph C2: a dirty draft survives Code→Graph→Code`). The
  graph is a DERIVED view of the live polled structure — the S5 hold
  applies to the editable card list, not here: every poll re-runs the
  deterministic dagre layout and animates to it — existing nodes
  tween ~250ms ease-out in JS (edges follow because React Flow
  recomputes edge geometry from node positions each frame), new
  nodes/edges fade in at their final spot, removed ones fade out then
  unmount, and a superseding poll retargets the in-flight tween from
  the current interpolated positions (no snap, no queue). An
  unchanged structure yields bit-identical positions, so a state-only
  poll repaints dots/labels and moves NOTHING (`graph: a state-only
  poll repaints without moving nodes or viewport`); under
  `prefers-reduced-motion` the new layout applies instantly with no
  fades (`graph: reduced motion applies a re-layout with no
  intermediate frames`). The viewport NEVER moves on a poll — fit
  runs once on first reveal, the Controls fit button and
  selection-driven pans are the only other viewport actions
  (`graph: a polled structure re-lays out live; the viewport never
  moves`, `graph: zoom persists across polls`). Selection is
  `?section=` and paint-only — ancestors/descendants and links
  highlight by colour/opacity, never geometry (`graph S1: selecting a
  node never moves a node`); a section deleted by a poll clears
  `?section=` (`graph: deleting a section removes its node and clears
  the selection`). The selected node IS the active section: a
  directory chip click selects the node (pressed + aside) and scrolls
  the canvas just enough to reveal it, a node click moves the chip's
  `aria-current`, and clearing the selection clears the chip — the
  scroll-spy is inert while the graph is visible (`graph: chip and
  node selection stay in sync`). The aside lists blocking diagnostics
  (`duplicate_define`/`missing_provider`/`cycle`) with a textual
  "blocks runs" tag — never colour alone. "Open code" returns to the
  cards and the `?section=` scroll lands on the card (`graph: Open
  code lands on the section card`); a "Run section" action in the
  aside drives the same `runOne` path as the cell gutter — once
  confirmation and save validation included — with feedback in the
  toolbar status line (`graph: Run section from the aside runs it and
  repaints without moving nodes`). Running sections repaints state
  dots without moving nodes (`graph: running sections repaints state
  without moving nodes`). Mobile stacks the aside below the canvas
  inside the same fixed box (`screenshots: notebook graph desktop/
  mobile × light/dark`).
- **C4**: `?section=name` deep-links scroll the addressed card into
  view; the directory chip strip lists ordinal, name and a state dot
  (state also in `title`/sr-only, description in `title`) in held
  order and is the single active-section indicator — a click sets
  `?section=`, marks the chip `aria-current` immediately and scrolls
  the card (or, in graph view, selects the node and reveals it on the
  canvas). In code view an IntersectionObserver whose top edge sits
  under the head marks the first intersecting chip in shown order —
  paint-only tint + name colour (S1, measured identical chip boxes);
  an explicit pick holds the spy until the programmatic scroll settles
  or the next user scroll (wheel/touch/scroll key/scrollbar) hands
  control back (`S1/S3: a chip click highlights immediately; a user
  scroll hands back to the spy`). The strip keeps the active chip
  visible through its own `scrollLeft`, never `scrollIntoView` (S6).
  Cells are flat bordered blocks on a
  40px gutter + body grid; the header row carries ordinal, name, state
  badge, the `once` tag, an inline borderless description input and
  SaveStatus/collapse/⋯ actions; collapse hides only the editor —
  alerts, output and footer stay. "Add section" appends an "Untitled
  Section" — auto-named `untitled_section[_N]` — immediately with no
  dialog, and the fixed-height `.nb-insert` rows between cells (and
  after the last) create one directly after their cell; creation
  errors go to the status line, and the row keeps its height without
  edit rights (S3). CodeMirror
  editors auto-size (`minHeight` 44px, `maxHeight` 420px) and a
  just-created section focuses its editor once. There is no scratch
  cell — every console execution belongs to a section (`no scratch
  affordance` e2e). Mobile wraps the
  toolbar to two rows and restores 44px hit targets. Screenshots
  `notebook-{desktop,mobile}-{light,dark}.png` plus `-sticky`,
  `-section`/`-notes` detail shots and the 2x `nb-corner-*-2x.png`
  close-ups under `.devin-artifacts/screenshots/`.

The section directory is no longer a strip that scrolls away with the
cards: `.nb-head` is ONE floating panel — an opaque raised surface
(`--paper` fill, `--radius-surface`, `--shadow-float`, 1px `--edge`
border, real `6px 10px` padding, no negative margins) pinned 8px under
the console header, its header gap masked by an opaque `::before` so
scrolling content never peeks through. Inside it the toolbar row and
the 30px directory row are split by one inner hairline; the whole
panel is a fixed height (`--nb-bar-h` reserves it for the cards'
`scroll-margin-top` and the observer's `rootMargin`), so the outline,
the segmented Code|Graph control and the run actions stay reachable on
a long notebook (S3 — e2e measures the strip pinned, the jumped card
landing below the head, and the panel box constant across scroll, view
switch and a run). Spacing is explicit: the toolbar's left group and
the chip row share the panel's `6px` inner padding (7px from the
border-box edge), the segmented Code|Graph toggle is the LAST toolbar
item — ≥12px clear of the "Show N new" slot, its right edge 7px inside
the panel edge like every corner-near control (the `toolbar spacing
holds` e2e measures both). Nested rounded corners are concentric and
circular — the gap is equal on both axes and inner radius = outer
radius − gap, written as `calc()` from the `--nb-head-*` custom
properties (every head control takes the panel's 12px corner minus the
uniform 7px inset = 5px; the toggle thumb takes the track's 5px minus
its 2px padding = 3px). The active chip's paint-only highlight is set by
the explicit pick or the scroll-spy as described under C4, and the
strip keeps it visible through its own `scrollLeft`, never
`scrollIntoView` (S6). A single-section run pulses its chip's dot, opacity-only and
disabled under `prefers-reduced-motion`. On the detail page's side
column, an
fs-capable instance's State panel and file browser share one "Live
state" tablist — both panels stay mounted behind `hidden` like the
Notebook/Activity views (C2/C3, covered by the artifacts e2e) — and
the bottom Results block is gone, freeing the work surface.

Known gaps: in-app navigation away from
a dirty draft is unguarded like the rest of the console — recovery is
the pending-draft offer on return plus the beforeunload guard; agent-run
output arriving by poll can change a cell's height by up to the 240px
output cap (S5, bounded).

## Console shell navigation (consoleShell.spec.ts)

- C1: mobile drawer closes via shared Modal Escape and returns focus to the
  "Open navigation" trigger; drawer also closes on route change and on
  widening past 1024px.
- C2: module source + caller-script buffers survive drawer open/Escape and
  viewport resize (390→1440→390) without remount.
- S1: nav link boxes are measured under hover — identical geometry;
  the active marker is a `::before` whose base declaration carries all
  geometry and only `opacity` changes on `aria-current`.
- S4: the Inbox count retains its reserved-width slot as it updates.
  A single `Primary` navigation landmark and `aria-current="page"` identify
  the active destination including nested detail routes.
- S6: left-placement drawer reuses the shared Modal; centered modals and
  the page-scroll lock are unchanged.
- F2: destructive flows (dep removal, draft reload) still go through
  `Confirm`.
