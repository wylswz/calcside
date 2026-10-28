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
  overwrites a field being edited.
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
| S1 | `material.css` paint-only states; static CSS/Tailwind check; e2e "controls keep their box under hover and press" | enforced |
| S2 | shared `Menu` (Radix); S1; e2e "the More menu stays where it opened" | enforced |
| S3 | fixed reveal slot (`RevealNew`); e2e "palette controls stay put", Run code toggle test | partial — builder and detail only |
| S4 | `SwapLabel` and `RevealNew`; static label check; e2e "Show latest" and "Show N new" | enforced for state/request-driven labels and counters |
| S5 | `useHeldOrder` + "Show N new"/"Show latest"; `stableList` unit tests; e2e activity and instance list | enforced for polled lists |
| S6 | scroll-locked `Modal`; e2e "opening a dialog does not shift the page" | enforced for dialogs |
| C1 | `Modal`/`Menu` return focus, removal falls back to a neighbour; e2e primitives and removal tests | enforced for overlays and removals |
| C2 | `Modal` asks before Escape, the close control or an outside click discards input; e2e "a dialog with unsaved input asks…"; extension editor keeps invalid JSON | enforced for dialogs |
| C3 | `useHeldOrder` keeps shown order across refetches; review for expansion, selection and scroll | partial — polled lists keep order |
| C4 | — | review |
| P1 | pending guards; static `mutate` check; e2e removal test | partial — async forms by review |
| P2 | `ui.tsx` primitives; static browser-dialog check; e2e primitives test | enforced for buttons, menus, dialogs and labels |
| P3, F3 | — | review |
| P5 | policy editor footer reports validation and save failures; e2e "policy editor reports…" | partial — policy editor only |
| P4 | `material.css` motion block; static duration/reduced-motion check | enforced in CSS, not measured |
| F1 | S1, S2, `Confirm`; e2e menu and removal tests | enforced for menus and removals |
| F2 | `Confirm`; static destructive-control check | enforced for destructive controls |

## Known gaps

- **S3:** only the instance builder and detail page measure local effects;
  other pages are covered by review.
- **S5/C3:** only the instance list and activity hold their order while
  polling. Expansion, selection, filters and scroll position are review-only.
- **P3/F3:** no automated check that an action matches its label or that
  stale states are honest.
- **P5:** the policy editor reports outcomes at its footer; other forms and
  actions are review-only.
- **C4:** view state is not yet consistently addressable in the URL.
- **S6:** measured for dialogs only, not for menus or popovers.
- **P1:** pending guards are enforced on `Confirm` and by the static
  `mutate` check; longer inline forms are review-only.

### Application editor (EP-0014)

`/applications/:id` is covered by `e2e/application.spec.ts` against a
live Vite dev server (React StrictMode) proxying a scratch dev API:
- S4: state/request-driven button labels use `SwapLabel` (Save draft /
  Save & Test / Save & Publish / Invoke published); the Invoke button
  is always rendered and merely disables until the app is ready, so
  its appearance never moves neighboring controls.
- S1: state styles are paint-only; no transform/size on hover/press.
- Save/Test/Publish snapshot the draft before the flight; a mid-flight
  edit aborts the chain with "Changed while saving" and keeps edits.
  A conflicting save offers a reload Confirm instead of overwriting.
- Draft state is scoped by user + workspace + application id; navigating
  between apps remounts a clean editor under a different scope key.
- Mobile (<768px) stacks with no horizontal overflow; screenshots
  `application-ui-{desktop,mobile}.png`.
- Coverage is the 7-case browser suite plus static/unit geometry
  checks, not frame-level measurement.

Application creation keeps the optional capability/extension form mounted
when collapsed (C2). The kind-switch discard prompt covers structured
configuration and invalid Advanced JSON, not only the name (C2/F2).
Application-mode forms omit instance lifetime fields and allow only vault
references. Raw Advanced JSON is submitted without a browser-number
round trip.

Invocation examples use Radix keyboard tabs and the shared `CopyBlock`
with `SwapLabel` feedback (P2/S4). Their code panel has a fixed height
and local horizontal scrolling (S1/S3). Copy failures are visible without
discarding text (P5). Snippet unit tests capture the actual curl arguments,
Python request bytes and JavaScript fetch body without network access;
all preserve the exact JSON input, including large integers.
