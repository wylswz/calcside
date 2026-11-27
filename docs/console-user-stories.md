# Console user stories

- **Status:** Draft — the acceptance reference for the console redesign
- **Scope:** every console route (`web/`)
- **Related:** [EP-0009](proposals/0009-console-for-supervising-agents.md), [EP-0016](proposals/0016-application-interrupts.md), [EP-0025](proposals/0025-application-notebooks.md), [interaction-stability.md](interaction-stability.md)

The console is a developer tool, not a consumer app. Every screen exists to move
a developer from "what am I doing" to "it is done". The stories below are written
from inside the developer's head: the mission, the current step, the next step,
the definition of done, and what to do when stuck. A screen passes review only
if it answers those five questions without the developer hunting for them.

## The five questions every screen must answer

| Question | What the screen shows |
|---|---|
| Where am I? | Resource kind, name, id and status in one compact header row. Never a hero banner. |
| What is happening now? | Live state in a fixed place: status dot, run phase, save state, stream progress. |
| What should I do next? | Exactly one primary action, placed where the eye lands after reading the state. Secondary actions are ghost buttons. |
| How do I know I'm done? | An explicit terminal state: `succeeded · 120ms`, `Published v3 · ready`, `Resolved by alice`. |
| What if it fails? | The error with its code next to the thing that failed, plus the one action that recovers (Retry, Open run, Configure extension, Reload). |

## Personas

| Persona | Mission | Lives in |
|---|---|---|
| **App developer** | Turn a piece of logic into a callable, reviewed, published application. | Applications → Notebook / Test / Invoke / Activity |
| **Agent supervisor** | Let an agent work inside a bounded instance; check what it did; intervene. | Instances → Notebook / Activity / State / Files |
| **Resolver** | Unblock runs paused at an interrupt with the right event. | Inbox, run detail |
| **Integrator** | Call a published application from another system. | Invoke (request examples, API keys) |
| **Workspace admin** | Decide what code in the workspace may reach: secrets, policies, providers, keys, members. | Workspace, Audit |

## 1. App developer

**Mission:** ship an application whose `main(input)` returns the right output for
real inputs, under the right boundary, and that other systems can call.

**Done when:** a published release is `ready`, a real invoke succeeded, and the
contract (input/output schema) matches what callers send.

### 1.1 Start an application

> As an app developer I open Applications, click **New application**, give it a
> name, and land in the notebook with a runnable `main` scaffold.

- The empty list shows one primary action ("New application") and one line saying
  what an application is. No illustration, no marketing copy.
- After creation the notebook opens with the cursor in `main`.

### 1.2 Write code across sections

> As an app developer I split logic into sections (helpers, data, handlers) and
> want to see how they depend on each other while I write.

- Sections render as editor cells with a compact head (ordinal, name, entry label,
  actions). Cells are separated by hairlines, not cards with shadows.
- The Sections explorer (left, VS Code style) lists every section with its entry
  labels; clicking scrolls the cell into view. The Graph view shows dependencies.
- Save state lives in the status bar (`Saved` / `Saving…` / `Unsaved` /
  `Couldn't save · Retry`). It never moves other content.

### 1.3 Debug any function, not just `main`

> As an app developer I want to run *any* top-level one-argument function in my
> notebook — `main`, a handler, or a throwaway `def debug(input):` I wrote to
> inspect a variable — with an input I type, and see what it returns.

An application run is "the notebook's sections plus one entry function". Main is
just the public entry; for debugging, any function with the same shape is a valid
entry.

- Every line `def <name>(<param>):` at the top level of a section gets a ▶ run
  marker in the editor gutter, exactly like VS Code's "Run | Debug" code lens.
  This includes `main` and every helper/handler/debug function with one parameter.
- Clicking ▶ saves the draft, opens the Test panel with that function selected
  as the entry, and runs it immediately with the panel's input.
- The Test panel head shows `Entry: [name ▾]` (all detected functions, `main`
  first), the input editor labelled `<name>(input)`, the Run button, and the
  output pane.
- `main` runs exactly as before: schema-validated input, durable test run,
  interrupts supported.
- Any other entry is a **debug run**: the input is passed verbatim (any JSON
  value, no input schema), the return value is shown without output-schema
  checks, and nothing is recorded as a run. `interrupt.wait` is not available in
  a debug run; the error says so.
- Debug runs never reach the published replica; only owners/editors can start
  them (same access as Test).
- Done: the output pane shows the returned JSON plus `succeeded · 12ms · 340
  steps`, or the error code and message.
- Stuck: a failing run shows the error in the output pane and the failing
  section is reachable in one click.

### 1.4 Configure the boundary

> As an app developer I grant capabilities, extensions, modules and policies and
> define the input/output contract.

- Settings replace the editor area (same frame), grouped as "heading | controls"
  bands. Catalogs (extensions, modules, policies) dock in the left explorer.
- A spec error blocks Test/Publish and names the field with a "Configure" jump.

### 1.5 Publish

> As an app developer I review what changes between the live release and my
> draft, then publish.

- Publish is the toolbar's primary action. The review dialog shows the diff and
  digests. After publishing, status goes `preparing → ready` in the header and
  the Invoke view unlocks; the developer is told "Invoke is available".

### 1.6 Invoke the published release

> As an app developer or integrator I send a real request to the published
> release and inspect the response, like in Postman.

Layout (Postman-like, top to bottom):

1. **Request bar:** `POST` badge · full URL (copyable) · release `v3` · primary
   **Send** button. One row.
2. **Request | Response** split (resizable feel, hairline divider):
   - Request pane tabs: **Body** (JSON editor), **Code** (curl / Python / JS
     snippets for the current body), **Auth** (how to get an API key, Create key).
   - Response pane head: status dot + `200 · succeeded · 120ms · 3 steps`, or the
     error code; stream meta while streaming; Stop while streaming.
   - Response body: the exact output JSON in a mono, scrollable surface.
3. **Run follow-up** (only when the run did not finish in one step): a compact
   strip *inside the response pane, under its head*: `Waiting on interrupt
   int_… → on_ok` with **Resume…** and **Open run**. Resuming opens the resume
   event editor in the same response pane (event JSON editor + Resume button),
   never a floating block in a corner.

- Done: the response head reads `succeeded`. Stuck: the head reads `failed ·
  <code>`, the body shows the message, and **Open run** links to the step trace.

### 1.7 Review history

> As an app developer I check past runs (test and invoke), filter by status, and
> open one to see its steps, inputs, outputs and interrupts.

- Activity is a full-bleed table: status, trigger, entry, steps, started,
  duration, waiting-on. Rows open the run detail.
- Run detail: header with run id + status; a step table (seq, entry, status,
  duration, error); interrupts with their resume form; final output.

## 2. Agent supervisor

**Mission:** give an agent a sandbox with exactly the access it needs, watch what
it does, and step in when it is blocked or wrong.

**Done when:** the agent's work produced the expected output/files, nothing was
denied unexpectedly, and the instance is ended or left to expire.

### 2.1 Create a bounded instance

> As a supervisor I create an instance with capabilities/extensions/policies and
> a TTL, then connect an agent (MCP or API key).

- The create flow is a single page with a left form and a right summary of the
  boundary; one primary action. After creation the instance opens with the
  "Connect an agent" next step and copyable MCP/SDK snippets.

### 2.2 Watch the agent work

> As a supervisor I see every execution the agent made, what it printed, which
> capability calls it made, and which were denied.

- The instance header shows status + time left. A one-line "next step" banner
  says the single most important thing (waiting for first run / latest run was
  denied / expiring / ended) with its action.
- Activity lists executions with actor (agent key vs console), duration, denials
  highlighted in rose; each row expands to output + capability calls.

### 2.3 Intervene

> As a supervisor I run or edit a notebook section myself to inspect state, or I
> end the instance.

- The notebook is the same editor vocabulary as the application notebook.
- End/Delete are danger actions behind a Confirm.

## 3. Resolver

**Mission:** unblock paused runs correctly and quickly.

**Done when:** each interrupt I own is resolved and its run continued (or I know
why it can't be).

### 3.1 Triage the queue

> As a resolver I open Inbox and see every pending interrupt I can resume, sorted
> by urgency (expiry), with the application and handler it will call.

Layout (mail-client / VS Code "list + editor"):

- **Left list pane** (fixed width, full height, own scroller): one dense row per
  interrupt — handler name, application, time left (orange under 10 min), run id
  in mono. Selected row has the accent bar, not an outline box. New arrivals
  appear behind a "Show N new" bar at the top of the list (held order).
- **Right detail pane**: header (handler title, interrupt id, run link, expires
  at); **Context** (the JSON the program passed, read-only, mono); **Event**
  editor (JSON) with **Resume** as the primary action; after resume, the outcome
  in place ("Resumed — running on_ok…" → "Run succeeded · View run").
- Empty state: "Nothing waiting for you." centered in the list pane; the detail
  pane says "Select an interrupt".
- Done: the row turns into "Resolved by you · View run" and the selection moves
  nowhere by itself. Stuck: `already_resolved` shows who won, input kept.

## 4. Integrator

> As an integrator I copy a working request for my language, get an API key bound
> to this application, and verify the call works before wiring it in.

- Covered by Invoke's **Code** and **Auth** tabs (1.6). Snippets always reflect
  the current request body.

## 5. Workspace admin

**Mission:** keep the workspace's reach (secrets, hosts, providers, keys, people)
least-privilege and auditable.

- Workspace pages share one frame: a left settings nav (Members, Policies,
  Secrets, LLM providers, API keys) and a content area with a toolbar (title,
  count, primary "New …" action) above a full-width dense table.
- Secrets never show values; keys show prefix, binding, last used.
- Audit is a filterable dense table (time, actor, capability, op, decision);
  denials are rose; rows expand to the call metadata.

## Design rules derived from the stories

1. **IDE frame everywhere.** Persistent left activity bar (icons + tooltips +
   Inbox badge) for primary navigation on desktop; a hamburger drawer only on
   narrow screens. Resource pages use header → view tabs → toolbar → panes →
   status bar.
2. **Density.** 13px body, 28px controls, 32px pane heads, 24–28px table rows on
   work surfaces; page padding 16px, not 40px. No max-width centering on work
   surfaces.
3. **Restraint.** Flat surfaces, 1px hairlines, radius ≤ 4px, no gradients/domes/
   grain/glass, shadows only on floating layers (menus, dialogs). Colour is
   functional only: blue act/ok, green live, rose denied/error, orange attention.
4. **One vocabulary.** The same pane head, toolbar, table, empty state, notice
   and code surface on every route. A screen never mixes card-with-shadow
   layouts with flush panes.
5. **Next step is explicit.** One primary button per surface, placed after the
   state it acts on; terminal states and errors carry their follow-up link.
6. **Stability.** Everything in [interaction-stability.md](interaction-stability.md)
   still holds: paint-only state feedback, reserved boxes for changing labels and
   counts, held order for live lists.
