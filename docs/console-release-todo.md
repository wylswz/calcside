# Console release checklist

PR #49 is a checkpoint, not a release candidate. See [console user stories](console-user-stories.md) for acceptance criteria.

## Verified at this checkpoint

- Frontend typecheck, lint (warnings only), 179 unit tests, and production build passed after the navigation changes.
- Go build, vet, formatting, and `go test -race ./...` passed at the earlier checkpoint; this follow-up does not change Go.
- Application wayfinding was checked in a running console: Invoke → Auth and Settings return to code via **Editor**; run detail links separately to the editor and the Activity list. The run detail breadcrumb also fits at 390px in dark mode.
- All four visual sweep groups ran successfully, including the repaired detail and debug captures. The full browser suite is not green, and reviewing the full set of captures is still open.
- The Module browser test switched through Settings and Source, then edited, debugged, and published. Interrupt browser coverage verified Invoke's waiting phase, Inbox context, draft isolation between interrupts, and the in-place outcome.

## Before merging

- [x] Scope browser and visual sweep output selectors to the intended Notebook Test or Invoke Response pane.
- [x] Verify the Module tests exercise the new Source/Settings tabs.
- [ ] Verify workspace assertions target the visible panel without dropping behavior coverage.
- [ ] Verify the entire interrupt journey: Invoke reports the actual run phase and metrics, Inbox shows Context and a result in place, and switching or chaining interrupts never carries one interrupt's event draft into the next.
- [ ] Run the full Playwright suite and the visual sweep (`VISUAL_AUDIT=1`). Review desktop light/dark and 390px mobile, especially header wrapping, request pane fill, Resume form sizing, notebook gutter/Test panel, create flows, and Module Source/Settings. Fix any remaining gaps against the user stories.
- [ ] Review the final diff and verify generated code, migrations, and CI on the exact PR head. Do not merge while verification is incomplete.

## Before releasing

- [ ] Verify isolated fresh-database deployments on both Compose and kind using the deployment release gates.
- [ ] Publish from the verified merge commit. Mark the release **fresh deployment only** in the release notes: do not point it at a v0.0.7 or earlier database. The migration drops legacy script draft source without converting it to notebook sections.
