# EP-0009: A console for supervising agents

- **Status:** Draft
- **Scope:** web console (`web/`), execution records, capability denials

## Problem

The console looked Bauhaus but was organized like a database admin tool:
one page per table (Instances, Policies, API Keys, Secrets, Audit), and an
instance page whose largest element is a code editor for a human.

Calcside's actual division of labour is different:

| Party | Does | Needs from the console |
|---|---|---|
| Human | Decides what an instance may do; checks the work; intervenes | See the boundary, see what happened and why, act quickly |
| Agent | Writes and runs code through the API (usually via an API key) | Nothing directly; it never sees the console |
| Calcside | Enforces the boundary: capabilities, policies, limits, audit | A faithful display of its decisions |

So the human is mostly a **supervisor**, not the programmer. The editor is
an intervention tool, not the centre of the page.

### What supervision research says

- Licklider (1960) and Engelbart (1962) framed computers as partners that
  augment human judgement, not replace it. The human sets goals and judges
  results.
- Bainbridge, *Ironies of Automation* (1983): a human left only to monitor
  an automated process loses the context needed to take over, and cannot
  hold attention on a display where little happens. The display must make
  abnormal events stand out on their own and give the context to act.
- Klein, Woods, Bradshaw, Hoffman, Feltovich, *Ten challenges for making
  automation a "team player"* (2004): joint human–agent work needs the
  agent to be **observable** (its state and actions can be seen),
  **predictable**, and **directable**.
- Lee and See, *Trust in Automation* (2004): trust should be calibrated to
  actual reliability, which requires seeing outcomes, including failures.
- Norman's gulf of evaluation: the user must be able to tell what happened
  after an action without translating raw records.

### What the console got wrong

Measured against those needs, with seeded data:

1. **Agent runs could not be reviewed.** Execution output was not stored,
   only its byte count. The human could see the code but not what the agent
   saw.
2. **Cause and effect were split up.** The code (Executions tab), the
   capability calls it made (Audit tab) and the files it produced (Files
   panel) lived in three places, joined only by an opaque exec ID.
3. **Who acted was invisible.** Nothing distinguished a run by the agent
   (API key) from the human's own console run.
4. **The display misreported denials.** `net.get` to a host outside
   `allow_hosts` was refused by the instance's own `net` settings, but the
   gate recorded it as `allow` with an error, so it showed a blue "allow".
5. **The human's own look-arounds were mixed into the agent's record.**
   Opening the Files panel writes `console` fs events that sat among the
   agent's calls with nothing to tell them apart.
6. **Colour and shape were ornament.** Blue meant link, primary button,
   running, allow, ok and `fs`. Red meant delete, deny, error and `net` (as
   if network access were inherently dangerous). Yellow meant warning,
   expired, dev mode and `ext`. The circle/square/triangle marks followed
   Kandinsky's 1923 shape–colour exercise, which has no meaning in this
   domain. The numbered nav and section eyebrows came from the slide deck.

## What Bauhaus actually teaches here

The Bauhaus did not start from a style; it started from use.

- **Study the activity first.** The 1923 Haus am Horn kitchen (Benita Otte,
  Ernst Gebhardt) was laid out from the cook's sequence of work and the
  light; its form followed from that study. The console's form should follow
  the supervisor's work: check the boundary, review what happened, act.
- **Colour is for orientation, not decoration.** Hinnerk Scheper's 1926
  colour plan for the Dessau building assigned colours to functional areas
  so people could find their way; red marked the director's office for its
  significance. A colour must mean one thing everywhere.
- **Show the construction.** The Dessau workshop wing's glass curtain wall
  lets the structure and the work inside be seen from outside. The console
  should show the real mechanism (grant → run → calls → files) instead of
  hiding it behind tables.
- **Clarity over a priori aesthetics.** Moholy-Nagy (1923): "Typography is a
  tool of communication… Legibility-communication must never be impaired by
  an a priori esthetics." Hierarchy comes from content: what matters most is
  largest; numbers are tabular; code is monospaced; labels are plain words.
- **Standard parts.** One small set of components, used the same way on
  every page, instead of per-page variations.

## Design

### Words

The console uses the words the API already uses and adds none:

- **instance**, **run** (an execution), **call** (a capability call),
  **file**, **policy**, **secret**, **API key**.
- A call's outcome is one of **ok**, **failed** (the operation returned an
  error) or **denied** (refused by the instance's settings or a policy).
- A run is by **agent** (API key) or **you** (console session). A call made
  while you browse files is a **console** call.

### Colour and shape

Three signal colours, each with one meaning and a redundant shape for
colour-blind users. Everything else is black, white and greys.

| Colour | Shape | Meaning |
|---|---|---|
| Red | square | **Denied** — the boundary refused something. Always worth a look. |
| Yellow | triangle | **Failed or running out** — a run or call errored, or the instance expires soon. |
| Blue | circle | **You** — things you did, and things you can act on (links, the primary action). |

Normal outcomes (ok, running, allow) are not coloured: success is the
common case and should not compete for attention (Bainbridge). Capabilities
are not colour-coded at all.

### Layout of the instance page

The page follows the supervisor's sequence, top to bottom:

1. **Header** — the instance's name (its labels, e.g. `task=recon`; the ID
   when it has none), status, time left, and actions (Keep alive, Delete).
2. **Allowed** — one compact block stating the boundary in plain terms:
   each granted capability with its settings (`net` → hosts and methods,
   `fs` → quota, `ext` → aliases and sources), policies, secrets (names and
   domains, never values), env, and limits. Always visible, because every
   other judgement depends on it.
3. **Activity** — a reverse-chronological list of runs. Each run shows who
   (agent / you), when, outcome, duration and a one-line summary, with
   counts of denied and failed calls. Expanding a run shows its code, its
   output and error, and its calls in order, each with its outcome and, for
   denials, the reason. Console calls are grouped separately and muted.
   Filters: all / needs attention (denied or failed).
4. **Side column** — the results: **Files** (browse, preview, export) and
   **State** (variables and memory).
5. **Run code** and **Agent prompt** are tools, available from the header,
   opening a panel. Running code here creates a run by **you**.

### Other pages

- **Instances**: name, status and time left, what is allowed (e.g.
  `fs · net 2 hosts · ext tavily`), last activity. Delete moves to the
  instance page.
- **Audit**: the same call rows and outcomes as the instance page, across
  instances, with an outcome filter.
- **Policies, Secrets, API keys**: setup pages, visually separated from the
  work pages in the navigation; same table and form components.
- The New instance dialog is framed as deciding what the instance may do.
  Capabilities are plain toggles with descriptions; no colour coding.

## API and runtime changes

1. **Store run output and error.** `Execution` gains `via`
   (`session | api_key | anonymous`, the existing `AuthKind`).
   `ExecutionDetail` gains `output`, `output_truncated` and `error`
   (`ExecError`, nullable). Output is already secret-redacted on the node;
   the API tier stores at most 64 KiB. Code is already stored with the same
   retention, so this adds no new class of data.
2. **Refusals by the instance's own settings are denials.** `net` returns a
   `DeniedError` (phase `before`, hook `net`) when a host is not in
   `allow_hosts`, a method is not in `methods`, or a secret is not allowed
   for the host or requires https. The gate then records `deny` with the
   reason, so the audit log, the API and the console agree. Script-visible
   error text keeps the original reason.
3. Console calls keep `exec_id = "console"`; the console labels them.

## Security

- No new data class: output is redacted the same way as the live response
  and the API tier bounds it before storage.
- Secret values never appear; the Allowed block shows names and domains.
- Denials becoming `deny` makes policy-relevant events easier to find; no
  enforcement changes.

## Out of scope

- Live streaming of runs.
- Per-run file attribution beyond the paths in call arguments.
- Instance activity summaries in the list API.
