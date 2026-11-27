# Console release checklist

PR #49 is a checkpoint, not a release candidate. See [console user stories](console-user-stories.md) for acceptance criteria.

## Verified at this checkpoint

- Frontend typecheck, lint (warnings only), 179 unit tests, and production build passed.
- Go build, vet, formatting, and `go test -race ./...` passed.
- The browser visual sweep passed the list and interrupt groups. The detail group failed because `.app-out-pre:not(.app-call-out)` now matches both mounted output panes. The debug screenshot group did not run.

## Before merging

- [ ] Scope the visual sweep's output selectors to the intended Notebook or Invoke pane. Verify the Module tests exercise the new Source/Settings tabs and that workspace assertions target the visible panel without dropping behavior coverage.
- [ ] Verify the entire interrupt journey: Invoke reports the actual run phase and metrics, Inbox shows Context and a result in place, and switching or chaining interrupts never carries one interrupt's event draft into the next.
- [ ] Run the full Playwright suite and the visual sweep (`VISUAL_AUDIT=1`). Review desktop light/dark and 390px mobile, especially header wrapping, request pane fill, Resume form sizing, notebook gutter/Test panel, create flows, and Module Source/Settings. Fix any remaining gaps against the user stories.
- [ ] Review the final diff and verify generated code, migrations, and CI on the exact PR head. Do not merge while verification is incomplete.

## Before releasing

- [ ] Verify isolated fresh-database deployments on both Compose and kind using the deployment release gates.
- [ ] Publish from the verified merge commit. Mark the release **fresh deployment only** in the release notes: do not point it at a v0.0.7 or earlier database. The migration drops legacy script draft source without converting it to notebook sections.
