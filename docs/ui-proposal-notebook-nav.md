# UI Proposal: persistent section navigation + file browser reorganization

> Status: proposal — not implemented.
> Targets the notebook UI delivered by EP-0022 (`feat/instance-notebooks`,
> not yet on `main`). If the EP-0022 branch has moved since this was
> written, re-check the component/CSS references below before
> implementing.

## Current layout (verified against `feat/instance-notebooks`)

Page: `web/src/pages/InstanceDetail.tsx` + `instance-detail.css`

```
detail-grid (grid: minmax(0, 1fr) 340px)
├─ work-surface
│   ├─ view-tabs: Notebook | Activity   (hidden 切换，双面板常挂载 C2/C3)
│   ├─ Notebook
│   │   ├─ .nb-toolbar   sticky (top:57px, 36px) — run actions | status | details
│   │   ├─ .nb-directory chip strip — NOT sticky, scrolls away with the cards
│   │   ├─ .nb-cards     section cards + inserters
│   │   └─ .nb-scratch   scratch runner (bottom)
│   └─ Results block (bottom): Files > ArtifactBrowser   ← problem ②
└─ side-col (340px)
    ├─ StatePanel: State > Variables(filter+table) + Memory   ← "变量 inspect"
    ├─ Boundary / Agent / …
```

Two facts this proposal builds on:

- `.nb-directory` (order + name + state dot chips) is **not sticky** —
  on a long notebook it scrolls out of view together with the cards.
- The file browser lives in the **Results block at the bottom of the
  work-surface** (`ArtifactBrowser`), far from the variables inspect
  in the side column.

## Proposal ① — persistent top-level section navigation

### Option A (recommended): two-row sticky header

The sticky toolbar becomes two rows; the section directory rides in
row 2 and stays visible while the cards scroll under it.

```
┌───────────────────────────────────────────────────────────┐
│ row1 [Run all][Run stale][+Add]  one-line status  [Details][Show N new] │  unchanged
├───────────────────────────────────────────────────────────┤
│ row2  1.load_data ●  2.transform ●  3.plot ●  4.scratch ▶ │  nb-directory, persistent
└───────────────────────────────────────────────────────────┘
```

- **row2 = existing `nb-directory`**, made sticky below the toolbar
  (`top: 95px`, or merged into the toolbar container as a second
  flex row). Existing chip content stays: order, name, state dot
  (fresh/stale/failed/not_run), held order (S5).
- **scroll-spy**: the chip for the section in the current viewport is
  highlighted (`IntersectionObserver` + rAF throttle), **paint-only**
  (S1 — no layout/size/transform change).
- **running state**: the currently running section's chip gets a pulse
  dot, synced with the existing `Running…` label.
- **jump**: click scrolls to the card (existing behavior) — add
  `scroll-margin-top: ~76px` to section cards so the sticky header
  never covers the target.
- **scratch chip**: a trailing `scratch` chip makes the bottom
  scratch runner reachable from anywhere in a long notebook.
- Interaction stability: S1 (paint-only highlight), S3 (fixed-height
  overlay — cards scroll *under* it, nothing is inserted above them),
  S5 (held order preserved), C2/C3 (directory is part of the mounted
  notebook panel).

### Option B (heavier, not recommended for now)

A page-level section index next to the `view-tabs`, visible across
Notebook/Activity views. Useful only when section count is large and
users cross-reference activity/audit; not worth the cost yet.

## Proposal ② — file browser next to the variables inspect

### Organization: "Live state" side panel with tabs

```
side-col
├─ [ State ] [ Files ]              ← tabs; both stay mounted (hidden
│    State: Variables + filter + Memory (unchanged)   switching, C2/C3)
│    Files: ArtifactBrowser (breadcrumb + list + export + limits)
├─ Boundary
├─ Agent
└─ …
```

Rationale:

- **Semantics**: files are runtime instance state (VFS) just like
  variables and memory — grouping them under one "live state" panel
  matches how users think about them.
- **Feasibility at 340px**: the variables table already works in the
  side column; a file list (name/size) fits the same width. Rows
  truncate with `title` hover; keep breadcrumb + export controls as
  they are.
- **work-surface payoff**: removing the bottom Results block gives the
  notebook + scratch the full column height — a long notebook is no
  longer squeezed by the file area.
- **Stability**: tabs use the same `hidden`-attribute keep-mounted
  pattern as `view-tabs` (C2/C3); the file list is polled data, so
  wrap it in `useHeldOrder` ("Show N new") per S5, or at minimum keep
  the shown order stable across polls.
- **Mobile** (`detail-grid` collapses to 1fr): the side column drops
  below the work-surface; tabs still work.

### Open decisions (resolve before implementing)

1. Tabs vs stacked sections: tabs save vertical space and group
   better — **leaning tabs**.
2. Memory block stays under Variables (current), not a third tab.
3. Side column width 340px: widen to ~380px only if file rows need
   it (affects the whole page — do not widen casually).
4. Export flow stays a dialog triggered inside the Files tab.

## Interaction-stability impact checklist

| Invariant | Change |
|---|---|
| S1 | tab active state + scroll-spy highlight paint-only (no size/transform/spacing) |
| S2 | no menu chases its trigger; chips are static buttons |
| S3 | sticky nav rows are fixed-height overlays; cards get `scroll-margin-top`; no content inserted above cells |
| S4 | labels use the existing `SwapLabel` pattern (Running… etc.) |
| S5 | directory and file list both keep `useHeldOrder` |
| C2/C3 | State/Files tabs stay mounted (`hidden`), like `view-tabs` |
| P1/F3 | run/autosave flow untouched; run actions remain in row 1 only |

Update the status table in `docs/interaction-stability.md` (S1/S3/S5
rows) when implementing.

## Test plan

- `web/e2e/notebook.spec.ts`:
  - sticky directory row visible after scrolling a long notebook;
  - chip click scrolls the target card into view *below* the sticky
    header (scroll-margin);
  - scroll-spy highlights the in-view section (paint-only).
- file browser:
  - State/Files tab switch keeps the file list scroll position and
    selection (C2/C3);
  - polled arrivals sit behind "Show N new" (S5).
- existing suites (`pnpm -C web test`, `test:browser`,
  `test:visual`) stay green.

## Acceptance criteria

- [ ] Section directory always reachable while the notebook scrolls.
- [ ] Jump-to-section never hides the card behind the sticky header.
- [ ] Files live beside Variables under one mounted panel; notebook
      work-surface is freed of the bottom Results block.
- [ ] No interaction-stability invariant regresses (S1/S3/S5 rows
      updated in `docs/interaction-stability.md`).