# Console release checklist

PR #49 is a checkpoint, not a release candidate. See [console user stories](console-user-stories.md) for acceptance criteria.

## Verified at this checkpoint

- Frontend typecheck, lint (warnings only), 179 unit tests, and production build passed after the navigation changes.
- Go build, vet, formatting, and `go test -race ./...` passed. Generated code matched the repository, and SQLite and PostgreSQL migrations validated.
- Application wayfinding was checked in a running console: Invoke → Auth and Settings return to code via **Editor**; run detail links separately to the editor and the Activity list. The run detail breadcrumb also fits at 390px in dark mode.
- All four visual sweep groups passed and the captures were reviewed. At 390px the Invoke request actions share one row, and on desktop the event editor uses the remaining Response pane height. Module Source and Settings were checked on desktop and at 390px.
- The Module browser test switched through Settings and Source, then edited, debugged, and published. The interrupt journey verified Invoke's persisted duration and step count as one run went from waiting to waiting again to succeeded, without carrying the first event into the second. Inbox switching retained each separate draft and showed the resolved run in place.
- The candidate images passed both isolated PostgreSQL deployment gates: Compose and kind each passed four integration cases and three timed performance cases.

## Before merging

- [x] Scope browser and visual sweep output selectors to the intended Notebook Test or Invoke Response pane.
- [x] Verify the Module tests exercise the new Source/Settings tabs.
- [x] Verify workspace assertions target the visible panel without dropping behavior coverage.
- [x] Verify the entire interrupt journey: Invoke reports the actual run phase and metrics, Inbox shows Context and a result in place, and switching or chaining interrupts never carries one interrupt's event draft into the next.
- [x] Run the visual sweep (`VISUAL_AUDIT=1`) and review desktop light/dark and 390px mobile.
- [x] Run the full Playwright suite with its configured single worker and the visual audit enabled: 160 passed at `c9a9f8a`.
- [x] Review the final code diff and verify generated code, migrations, and CI: all six PR checks passed at `c9a9f8a`.

## Before releasing

- [x] Verify isolated fresh-database deployments on both Compose and kind using the deployment release gates.
- [ ] Publish from the verified merge commit. Mark the release **fresh deployment only** in the release notes: do not point it at a v0.0.7 or earlier database. The migration drops legacy script draft source without converting it to notebook sections.
