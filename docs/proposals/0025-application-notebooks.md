# EP-0025: Notebook applications

- **Status:** Proposed
- **Created:** 2026-11-24
- **Related:** [EP-0023](0023-notebook-dag.md), [EP-0016](0016-application-interrupts.md), [EP-0021](0021-stream.md)

## Decision

A notebook application is one dependency graph of ordinary sections. Entry points are functions, not section types: `main` is the public entry, and an interrupt refers to a handler by its function name. Reuse the notebook's section editor, name dependency analysis and graph, but not instance globals or cross-request freshness. An application step is one worker invocation; a section is not a durable step, checkpoint, or retry boundary. Existing `main.star` applications are unchanged.

## Develop: show entry functions, not section types

Use one `Add section` action. Show `main(input)` as the Main entry in the outline and on its defining section. For handler names statically visible as literal `handler` or `on_expire` targets, show the function name and an entry label on its defining section; a section may expose several entry functions alongside helpers. Do not label every one-argument function as a handler. Dynamically computed targets may be unknown until runtime; a historical run shows the actual function and owning section from its pinned release. Role labels are textual and separate from execution status. Selecting a section highlights dependencies; it never invokes an entry. Application Test still starts from Main by default; additionally, the section editor paints a ▶ marker in the gutter of every top-level one-argument function, which opens the Test panel and runs that entry. A non-main entry is a debug run: the input is any bounded JSON value passed verbatim (no input schema, no handler-context object), the output is bounded and secret-redacted but not output-schema validated, and nothing is recorded as a run — `interrupt.*` ops report that interrupts are only available in application runs. Debug entries are reachable only through Test; public invoke always runs Main.

## Language and single output

Each section has a stable ID and its own namespace; there is no kind field. The release must define exactly one `main` as a top-level function taking exactly one positional argument. A function used as a resume or expiry handler must have the same arity and be declared by a direct top-level `def` in a notebook section. Its name must resolve to exactly one binding across the notebook, with no same-section redefinition or cross-section shadowing. An unused duplicate name is not prohibited merely because it could be a handler; ordinary data dependencies still use the notebook's existing ambiguity rules. A handler can live alongside Main, other handlers or helpers. The function name is the durable routing ID within a release; the section ID identifies its graph node, not the handler route.

The example file layout is illustrated by section comments; the comments are not directives:

```python
# Section: format_result
def result(order_id, status):
    return {"order_id": order_id, "status": status}

# Section: review_flow
def main(input):
    order_id = input["order_id"]
    if input.get("requires_review", False):
        req = interrupt.new(expires_in_s = 3600)
        # Notify the reviewer before waiting in a real application.
        interrupt.wait(req["id"], handler = "on_decision",
                       on_expire = "on_timeout", context = {"order_id": order_id})
    return result(order_id, "accepted")

# Section: review_handlers
def on_decision(ctx):
    status = "accepted" if ctx["event"]["approved"] else "rejected"
    return result(ctx["context"]["order_id"], status)

def on_timeout(ctx):
    return result(ctx["context"]["order_id"], "expired")
```

The selected entry's return value is the only output. There is no output binding to configure and no special meaning to the last section or `print`. Retain the existing bounded JSON, redaction and schema boundary; `None` is JSON `null` if the schema allows it. A returned Stream keeps the existing per-item validation and delivery semantics. A successful `wait` yields a waiting step, not the run's output. A later entry can return the final output or wait again.

## Execution and interrupt dispatch

Resolve the requested function to its owning section. Initialize that section and its transitive dependencies once in topological order in a fresh invocation session, then call the selected function with the existing validated input or handler-context envelope. Function-body references contribute dependency edges. Page order is not a dependency; independent nodes use a stable tie-breaker. The plan is section-granular: if a section contains several functions, the closure can include dependencies of sibling functions, but their bodies are not called merely because the section initialized. Initialization runs with the gate disarmed; capability calls belong in functions invoked from the armed entry. Treat exported data as read-only across sections. The step shares one deadline, step budget, audit correlation and stream scope. Compile on the worker; the API does not evaluate user code.

Keep `InvokeRequest.Entry`: absent or `main` selects Main; handler dispatch passes the handler function name. Resolve `handler` and `on_expire` when `wait` is called, including computed names. Fail the wait before committing it if either target is missing, ambiguous, reserved (`main` or a private name), or not a valid handler declaration; do not initialize an unrelated target just to resolve it. Recheck the actual callable on dispatch. The existing dispatcher retains the handler name and runs against the original pinned release with a fresh session. It never replays Main or restores globals or `/work`. The release pins section IDs and sources and the worker derives the function-to-section index. Interrupt reservation detection scans all sections and pinned workspace modules. Public invoke still only targets Main.

## Test and graph trace

V1 supports end-to-end application Test from Main. To exercise a Handler, resume or expire an interrupt from that test run. Section-level debugging is deliberately deferred: no standalone module runs, direct handler tests with synthetic fixtures, binding previews, or helper invocation in v1. Graph selection highlights dependencies; it does not execute them.

For each recorded step, render the graph from its pinned release. Show module initialization, entry invocation and output-boundary failures as distinct phases; ordinary dependency edges do not assert that a helper function was called. Mark executed, failed, waiting, not reached, and outside-plan states appropriately. On transport loss, report unavailable or partial trace instead of inferring node outcomes. Store bounded metadata (entry function name for the step, section ID, phase, status, timing, classified error), not intermediate values, raw errors, input/context payloads, tokens, or stream items. Helper call spans and live trace transport are out of scope.

## Rollout and acceptance

1. Add notebook graph execution and the entry return boundary. Cover name resolution across sections, shared dependencies initialized once per step, invalid entry signatures, output validation and script compatibility.
2. Reuse interrupt dispatch with function names and persist step traces. Cover multiple handler functions in one section, missing or ambiguous computed targets rejected at wait, correct handler selection after republish, expiry and repeated waits.
3. Add Develop and Runs views. Verify Main and statically known handler functions remain identifiable without section types, runtime traces identify the actual entry, and graph selection never executes it.
